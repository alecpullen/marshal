package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
)

// SubagentStatus is the lifecycle state of a registered subagent view.
type SubagentStatus int

const (
	SubagentRunning SubagentStatus = iota
	SubagentDone
	SubagentFailed
)

// SubagentView is the TUI-facing snapshot of one subagent's work. It is the
// summary card rendered in the parent transcript in place of the subagent's
// full tool log, and the handle the drill-down view uses to reach the child
// session's live transcript.
//
// Child is the subagent's own session.State. It is retained (not copied) so
// the drill-down view streams the child's transcript live; for pipeline/SDD
// subagents, which share the parent's state, Child may be nil and the card
// renders status/summary only.
type SubagentView struct {
	ID        int64
	Label     string
	Status    SubagentStatus
	Child     *State
	StartedAt time.Time
	EndedAt   time.Time
	// ToolCalls counts completed tool calls observed in the child transcript
	// at card-render time; the TUI refreshes it while Status == Running.
	ToolCalls int
	// CurrentTool is the in-flight tool's display label while Status ==
	// SubagentRunning, refreshed at card-render time like ToolCalls. Empty
	// when the child is idle or finished.
	CurrentTool string
	// Summary is the subagent's final report, captured when it finishes so
	// the completed card (and the drilled-in view header) can show it.
	Summary string

	// TokensUsed is the running total of prompt + completion tokens
	// observed from the child runner. It is updated while the subagent is
	// running and retained on the completed card.
	TokensUsed int

	// Role, Provider, Model, and Fallback record the dispatched agent's
	// route provenance so the TUI can display what model is actually
	// running (or ran) each subagent. Populated by the pipeline
	// dispatcher; defaults to zero values for non-pipeline subagents.
	Role     routing.AgentRole
	Provider string
	Model    string
	Fallback bool

	// Cancel is the per-child context cancel function. It is stored on
	// the view so a drilled-in user can stop a running subagent without
	// cancelling the whole parent turn.
	Cancel context.CancelFunc

	// SalvagedReason is non-empty when the subagent hit a budget ceiling
	// and its summary is partial. The TUI card renders this as a marker.
	SalvagedReason string

	// Error carries the failure text when Status == SubagentFailed so
	// agent.await can report why the child failed. Empty
	// for running or successful subagents.
	Error string

	// SubagentWorkFacts is the child's stable identity and result reference,
	// embedded so the card carries the same lightweight facts the conversation
	// work index stores. It never carries the full report body.
	SubagentWorkFacts
}

// SubagentMeta is the optional metadata passed to RegisterSubagentWithMeta.
type SubagentMeta struct {
	Role     routing.AgentRole
	Provider string
	Model    string
	Fallback bool
	// Origin identifies the call that dispatched the subagent: the turn, the
	// step and the tool call. It is provenance, kept so a restored result can
	// still name where it came from.
	Origin SubagentOrigin
}

// SubagentOrigin identifies the call that dispatched a subagent. It is stored
// verbatim as provenance and never parsed to reconstruct a child's identity.
type SubagentOrigin struct {
	TurnID     string
	StepID     string
	ToolCallID string
}

// SubagentWorkFacts is the lightweight, stable identity and result reference of
// one subagent's work. It is embedded in SubagentView and copied into the
// conversation's work-view index; it never carries the full report body, only a
// reference to the persisted message that does.
type SubagentWorkFacts struct {
	// WorkKey is the opaque, stable key of this child's work, minted at
	// registration. It is never parsed: it exists to pair an acknowledgment with
	// a result, not to reconstruct the child's identity.
	WorkKey string
	// RuntimeEpoch is the conversation epoch the work belongs to.
	RuntimeEpoch string
	// Origin is the dispatching call.
	Origin SubagentOrigin
	// ResultRevision is the terminal result's content revision, empty while the
	// child is running. A re-run settles at a new revision, so an old
	// acknowledgment cannot silently hide it.
	ResultRevision string
	// Report references the persisted subagent-report message; a zero MessageID
	// means the report is explicitly unavailable, never fabricated.
	Report db.WorkReference
	// StopRequested records that an explicit stop was accepted for this child. A
	// request that arrives only after the child settled cannot set it.
	StopRequested bool
	// Outcome is the finer terminal cause ("completed", "stopped", "failed"),
	// empty while running. It complements SubagentStatus rather than replacing
	// it: the status keeps its existing three-valued meaning.
	Outcome string
}

// SubagentSnapshot is an immutable, lock-free projection of the subagent
// registry at one moment: the conversation epoch, the source revision, the
// views, the ids still settling their terminal index write, and any bounded
// save errors. The views carry no Cancel callback and no mutable Child pointer;
// activity facts are read eagerly under the child's own locks.
type SubagentSnapshot struct {
	Epoch      string
	Revision   uint64
	Views      []SubagentView
	Settling   []string
	SaveErrors map[string]string
}

// SubagentEvent is published on the subagent broker when a subagent is
// registered or transitions status, so the TUI re-renders the card without
// polling. Epoch and Revision identify the source state the View belongs to, so
// a subscriber can discard a stale projection.
type SubagentEvent struct {
	View     SubagentView
	Epoch    string
	Revision uint64
}

// subagentSeq allocates process-unique subagent IDs. IDs only need to be
// unique within a parent session's lifetime; an atomic counter avoids a
// State.mu-held increment and keeps registration cheap.
var subagentSeq atomic.Int64

// RegisterSubagentStopGroup associates a child's own explicit-stop group with
// its subagent view id, so a targeted child stop reaches exactly that child's
// attached commands and nothing else. The group is allocated separately from the
// parent operation's because a background child outlives its parent's turn.
func (s *State) RegisterSubagentStopGroup(id int64, token *TurnStopToken) {
	if s == nil || token == nil {
		return
	}
	s.mu.Lock()
	if s.subagentStopGroups == nil {
		s.subagentStopGroups = make(map[int64]*TurnStopToken)
	}
	s.subagentStopGroups[id] = token
	s.mu.Unlock()
}

// bindSubagentWork binds a child's subagent card to its work item in this
// conversation's end gate (Task 8), so the child is part of the membership an
// end acceptance checks — with its own distinct ID even when it is shown under
// a parent — and so a stop pass can target it. When work is a creation item the
// caller registered (EnterSubagentWork), that item is the child's: the card names
// it instead of allocating a second one. An empty work registers the card's own
// item, which its finish releases — the path a pipeline or review worker takes,
// since those never enter through EnterSubagent.
func (s *State) bindSubagentWork(work WorkID, id int64, label string) {
	if s == nil {
		return
	}
	s.end.bindChildCard(work, id, label)
}

// releaseSubagentWork drops a finished child's work item, so a completed child
// stops being active work. A child that entered through EnterSubagent is
// released by its paired ExitSubagent instead; this path covers the workers
// whose card is their whole registration (pipeline and review children).
func (s *State) releaseSubagentWork(id int64) {
	if s == nil {
		return
	}
	s.end.releaseChildCard(id)
}

// ClearSubagentStopGroup drops a finished child's stop group so a later card
// reusing the id cannot inherit it. Called on normal completion: a child that
// finished on its own is never stopped by its explicit-stop wrapper.
func (s *State) ClearSubagentStopGroup(id int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.subagentStopGroups, id)
	s.mu.Unlock()
}

// RegisterSubagent records a new running subagent and returns its view. The
// returned view's Child pointer is the live child state; the caller owns
// finishing the registration via FinishSubagent. Publishing happens after
// the mutex is released, matching SetWorkspace.
func (s *State) RegisterSubagent(label string, child *State) SubagentView {
	return s.registerSubagent(SubagentMeta{}, "", label, child)
}

// RegisterSubagentWithMeta records a new running subagent and returns its
// view, carrying the dispatched agent's route provenance. It mirrors
// RegisterSubagent but additionally records Role, Provider, Model, and
// Fallback so the TUI can display what model is actually running (or ran)
// the subagent.
func (s *State) RegisterSubagentWithMeta(label string, child *State, meta SubagentMeta) SubagentView {
	return s.registerSubagent(meta, "", label, child)
}

// RegisterSubagentWithMetaAndWork records a new running subagent whose creation
// work item the caller already registered (EnterSubagentWork), so the card names
// that child's own item: a targeted stop reaches the child the user pointed at,
// and one child is never registered as two units of work. It mirrors
// RegisterSubagentWithMeta in every other respect.
func (s *State) RegisterSubagentWithMetaAndWork(work WorkID, label string, child *State, meta SubagentMeta) SubagentView {
	return s.registerSubagent(meta, work, label, child)
}

// registerSubagent is the shared body of RegisterSubagent/WithMeta/AndWork. work
// is the caller's child-creation item, or "" when the caller registered none.
func (s *State) registerSubagent(meta SubagentMeta, work WorkID, label string, child *State) SubagentView {
	s.mu.Lock()
	epoch := s.subagentEpochLocked()
	rev := s.bumpSubagentRevisionLocked()
	v := SubagentView{
		ID:        subagentSeq.Add(1),
		Label:     label,
		Status:    SubagentRunning,
		Child:     child,
		StartedAt: time.Now(),
		Role:      meta.Role,
		Provider:  meta.Provider,
		Model:     meta.Model,
		Fallback:  meta.Fallback,
	}
	// The child's stable work identity is minted here, once, and never derived
	// from a mutable id: the opaque key is what pairs an acknowledgment with a
	// result across a resume, which a display id cannot.
	v.WorkKey = rand.Text()
	v.RuntimeEpoch = epoch
	v.Origin = meta.Origin
	s.subagents = append(s.subagents, v)
	s.subagentDone[v.ID] = make(chan struct{})
	broker := s.subagentBroker
	s.mu.Unlock()
	// Register the child with the conversation's end gate: a live child is work
	// an end must be able to see and stop, and its card is what names it. The
	// binding happens after the registration above so the view exists before its
	// work item is named, and it is idempotent per subagent id.
	s.bindSubagentWork(work, v.ID, label)
	s.publishSubagentEvent(broker, v, epoch, rev)
	// The work index upsert is deliberately outside State.mu: it spans a
	// serialized database round trip, which must never be held while the rest
	// of the session waits.
	if err := s.recordSubagentWorkResult(v, rev); err != nil {
		s.logger.Warn("record subagent work result failed", "error", err, "subagent_id", v.ID)
	}
	return v
}

// subagentEpochLocked returns the conversation's single subagent epoch, minting
// it on first use so a State built as a literal still gets one. Callers must
// hold s.mu.
func (s *State) subagentEpochLocked() string {
	if s.subagentEpoch == "" {
		s.subagentEpoch = rand.Text()
	}
	return s.subagentEpoch
}

// bumpSubagentRevisionLocked advances the subagent source revision by one and
// returns it. Callers must hold s.mu.
func (s *State) bumpSubagentRevisionLocked() uint64 {
	s.subagentRevision++
	return s.subagentRevision
}

// publishSubagentEvent publishes a copy of a subagent view tagged with the
// source epoch and revision it belongs to. Publishing happens after the state
// lock is released, matching SetWorkspace and the rest of the session.
func (s *State) publishSubagentEvent(broker *pubsub.Broker[SubagentEvent], view SubagentView, epoch string, revision uint64) {
	if broker == nil {
		return
	}
	broker.Publish("subagent", SubagentEvent{View: view, Epoch: epoch, Revision: revision})
}

// SetSubagentCancel stores the cancel function for a running subagent
// so the TUI can interrupt that specific child.
func (s *State) SetSubagentCancel(id int64, cancel context.CancelFunc) {
	s.mu.Lock()
	var updated SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].Cancel = cancel
			updated = s.subagents[i]
			found = true
			break
		}
	}
	var epoch string
	var rev uint64
	if found {
		rev = s.bumpSubagentRevisionLocked()
		epoch = s.subagentEpochLocked()
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if found {
		s.publishSubagentEvent(broker, updated, epoch, rev)
	}
}

// SetSubagentSalvaged records the reason a subagent was salvaged on its
// view. Called by the agent.run handler before FinishSubagent so the TUI
// card can render a salvage marker.
func (s *State) SetSubagentSalvaged(id int64, reason string) {
	s.mu.Lock()
	var updated SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].SalvagedReason = reason
			updated = s.subagents[i]
			found = true
			break
		}
	}
	var epoch string
	var rev uint64
	if found {
		rev = s.bumpSubagentRevisionLocked()
		epoch = s.subagentEpochLocked()
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if found {
		// Publish a copy so subscribers see the updated reason. The view is
		// still running until FinishSubagent is called.
		s.publishSubagentEvent(broker, updated, epoch, rev)
	}
}

// CancelSubagent performs a TARGETED stop of a running subagent: it asks the
// conversation's command service to stop that child's own attached commands
// FIRST, then cancels the child's context. It returns true if a running
// subagent was found and a cancel function existed. The subagent handler is
// responsible for finishing the view when the child returns.
//
// This is the explicit-stop path only. A child that finishes on its own never
// reaches it: its completion goroutine calls the plain context cancel, so normal
// completion terminates nothing. A child's previously detached jobs are
// conversation-owned and keep running.
func (s *State) CancelSubagent(id int64) bool {
	s.mu.Lock()
	idx := -1
	for i := range s.subagents {
		if s.subagents[i].ID == id && s.subagents[i].Status == SubagentRunning {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return false
	}
	cancel := s.subagents[idx].Cancel
	token := s.subagentStopGroups[id]
	if cancel == nil {
		s.mu.Unlock()
		return false
	}
	// The exact running child and its cancel handle are located, so the request
	// is accepted: record it on the view before the child's context is
	// cancelled, so a settlement that follows can tell an accepted stop from a
	// spontaneous cancel. A request that arrives only after the child settled
	// never reaches this branch and cannot relabel the result.
	s.subagents[idx].StopRequested = true
	rev := s.bumpSubagentRevisionLocked()
	epoch := s.subagentEpochLocked()
	updated := s.subagents[idx]
	broker := s.subagentBroker
	s.mu.Unlock()
	// Service acceptance precedes cancellation: the child's attached commands
	// are marked stopping (and the child's turn is refused any new admission)
	// before the child's context is cancelled, so a command the child launched
	// is never left running with no stop request against it. Detached
	// child-owned jobs are conversation-owned and are never stop targets here.
	if token != nil {
		_, _ = token.Stop(fmt.Sprintf("subagent %d stopped", id))
	}
	cancel()
	s.publishSubagentEvent(broker, updated, epoch, rev)
	return true
}

// AddSubagentTokens increments the token counter for a running subagent
// and republishes the updated view.
func (s *State) AddSubagentTokens(id int64, n int) {
	s.mu.Lock()
	var updated SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].TokensUsed += n
			updated = s.subagents[i]
			found = true
			break
		}
	}
	var epoch string
	var rev uint64
	if found {
		rev = s.bumpSubagentRevisionLocked()
		epoch = s.subagentEpochLocked()
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if found {
		s.publishSubagentEvent(broker, updated, epoch, rev)
	}
}

// maxRetainedChildStates caps how many completed subagent Child State
// pointers are retained for drill-down. Older completed children have
// their Child pointer nilled so the full transcript/audit-log is GC'd.
// The summary, status, and metadata (small strings) are always retained.
const maxRetainedChildStates = 10

// FinishSubagent marks a subagent view finished (or failed, when
// err != nil), records its end time and final summary, closes its done
// channel so WaitSubagent callers unblock, and republishes so the card
// flips from the running spinner to its terminal state.
//
// M-3: after marking the subagent finished, nil out the Child pointer on
// older completed subagents beyond maxRetainedChildStates so their full
// State objects (transcript, audit log, thinking log) can be garbage
// collected. The summary and status are retained for the card display.
func (s *State) FinishSubagent(id int64, summary string, err error) {
	s.mu.Lock()
	idx := -1
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return
	}
	// The settling flag is set before the terminal mutation so a snapshot read
	// that races the mutation still sees the child as mid-settlement rather
	// than as a settled view whose final-index write has not even started.
	if s.subagentSettling == nil {
		s.subagentSettling = make(map[int64]bool)
	}
	s.subagentSettling[id] = true

	s.subagents[idx].EndedAt = time.Now()
	s.subagents[idx].Summary = summary
	outcome := "completed"
	if err != nil {
		s.subagents[idx].Status = SubagentFailed
		s.subagents[idx].Error = err.Error()
		outcome = "failed"
		// "stopped" is claimed only for an accepted stop request whose cancel
		// the child actually observed. A request that arrived late, or an
		// unrelated failure or panic, cannot relabel the result.
		if s.subagents[idx].StopRequested && errors.Is(err, context.Canceled) {
			outcome = "stopped"
		}
	} else {
		s.subagents[idx].Status = SubagentDone
	}
	s.subagents[idx].Outcome = outcome
	rev := s.bumpSubagentRevisionLocked()
	s.subagents[idx].ResultRevision = strconv.FormatUint(rev, 10)

	// M-3: release Child State pointers on older completed subagents.
	var completed int
	for i := len(s.subagents) - 1; i >= 0; i-- {
		if s.subagents[i].Status != SubagentRunning && s.subagents[i].Child != nil {
			completed++
			if completed > maxRetainedChildStates {
				s.subagents[i].Child = nil
			}
		}
	}
	updated := s.subagents[idx]
	done := s.subagentDone[id]
	delete(s.subagentDone, id)
	epoch := s.subagentEpochLocked()
	broker := s.subagentBroker
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
	// The child is no longer active work. A child registered by its creation
	// (EnterSubagent) is released by its paired ExitSubagent; this call
	// releases the item only when the child's card was its whole registration,
	// so every item is released exactly once.
	s.releaseSubagentWork(id)
	s.publishSubagentEvent(broker, updated, epoch, rev)
	// The first serialized final-index save attempt. The actual final
	// state/report and its attention are already on the view; a failed save
	// leaves them there and records the failure rather than reverting.
	if saveErr := s.recordSubagentWorkResult(updated, rev); saveErr != nil {
		s.logger.Error("save final subagent work result failed", "error", saveErr, "subagent_id", id)
	}
	s.mu.Lock()
	delete(s.subagentSettling, id)
	s.mu.Unlock()
}

// Subagents returns a copy of the registered subagent views in registration
// order. The Child pointers inside reference live states; callers read them
// under those states' own locks.
func (s *State) Subagents() []SubagentView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SubagentView, len(s.subagents))
	copy(out, s.subagents)
	return out
}

// Constants naming the lightweight work-index entries a subagent produces.
// They classify the entry, never the child: identity is WorkKey.
const (
	subagentWorkKind  = "agent"
	subagentWorkOwner = "subagent"
)

// subagentSaveFailure is one recorded subagent work-index save failure and the
// source revision it belonged to, so a later successful save can clear exactly
// the failures it covered.
type subagentSaveFailure struct {
	Revision uint64
	Message  string
}

// decimalAgentID renders an agent id as the decimal source id the work index
// stores. The id is display data, never a key, so it is copied verbatim rather
// than parsed.
func decimalAgentID(id int64) string { return strconv.FormatInt(id, 10) }

// subagentWorkState maps a view's lifecycle status onto the work index's
// terminal-state vocabulary.
func subagentWorkState(status SubagentStatus) string {
	switch status {
	case SubagentDone:
		return "completed"
	case SubagentFailed:
		return "failed"
	default:
		return "running"
	}
}

// subagentSummaryPreview bounds a final summary for the work index: the first
// line, capped, never the full report. safeWorkResult cleans it further.
func subagentSummaryPreview(summary string) string {
	summary = strings.TrimSpace(summary)
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		summary = summary[:i]
	}
	const maxSummaryPreview = 240
	if len(summary) > maxSummaryPreview {
		summary = summary[:maxSummaryPreview]
	}
	return summary
}

// subagentWorkResult builds the lightweight work-index entry for a subagent
// view: stable key/epoch/origin/timing, a bounded summary preview and the report
// reference — never a duplicate of the full report body.
func subagentWorkResult(v SubagentView) db.WorkResultRecord {
	return db.WorkResultRecord{
		TaskKey:        v.WorkKey,
		SourceID:       decimalAgentID(v.ID),
		RuntimeEpoch:   v.RuntimeEpoch,
		Kind:           subagentWorkKind,
		Label:          v.Label,
		Owner:          subagentWorkOwner,
		TurnID:         v.Origin.TurnID,
		StepID:         v.Origin.StepID,
		ToolCallID:     v.Origin.ToolCallID,
		State:          subagentWorkState(v.Status),
		Outcome:        v.Outcome,
		StartedAt:      v.StartedAt,
		EndedAt:        v.EndedAt,
		ResultRevision: v.ResultRevision,
		Detail:         subagentSummaryPreview(v.Summary),
		Reference:      v.Report,
	}
}

// recordSubagentWorkResult upserts a subagent's lightweight work-index entry and
// persists the serialized work view. It runs entirely outside State.mu: the
// write spans a database round trip, which must never be held while the rest of
// the session waits. A failure is recorded per source id against the revision it
// belonged to and returned.
func (s *State) recordSubagentWorkResult(v SubagentView, revision uint64) error {
	if v.WorkKey == "" {
		return nil
	}
	record := safeWorkResult(subagentWorkResult(v))
	s.workViewSaveMu.Lock()
	s.mu.Lock()
	replaced := false
	for i := range s.workView.AgentResults {
		if s.workView.AgentResults[i].TaskKey == record.TaskKey {
			s.workView.AgentResults[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		s.workView.AgentResults = append(s.workView.AgentResults, record)
	}
	s.mu.Unlock()
	err := s.persistWorkViewLocked()
	s.workViewSaveMu.Unlock()
	if err != nil {
		s.recordSubagentSaveError(v.ID, revision, err.Error())
		return err
	}
	if s.clearSubagentSaveErrors(revision) {
		// A successful save covered these revisions, so the source moved: wake
		// subscribers to re-read rather than leaving a stale projection up.
		s.wakeSubagentSource(v.ID)
	}
	return nil
}

// recordSubagentSaveError records a bounded save failure for a source id against
// the revision it belonged to.
func (s *State) recordSubagentSaveError(id int64, revision uint64, msg string) {
	s.mu.Lock()
	if s.subagentSaveErrors == nil {
		s.subagentSaveErrors = make(map[int64]subagentSaveFailure)
	}
	s.subagentSaveErrors[id] = subagentSaveFailure{Revision: revision, Message: msg}
	s.mu.Unlock()
}

// clearSubagentSaveErrors drops the recorded failures covered by a save that
// reached revision, and reports whether it cleared any. It never touches a
// failure from a later revision, which the save did not cover.
func (s *State) clearSubagentSaveErrors(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := false
	for id, f := range s.subagentSaveErrors {
		if f.Revision <= revision {
			delete(s.subagentSaveErrors, id)
			cleared = true
		}
	}
	return cleared
}

// wakeSubagentSource publishes a source wake for one settled child after its
// index save caught up: it advances the source revision and republishes the
// view so subscribers re-read the work projection.
func (s *State) wakeSubagentSource(id int64) {
	s.mu.Lock()
	rev := s.bumpSubagentRevisionLocked()
	epoch := s.subagentEpochLocked()
	var view SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			view = s.subagents[i]
			found = true
			break
		}
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if found {
		s.publishSubagentEvent(broker, view, epoch, rev)
	}
}

// SubagentSnapshot returns an immutable projection of the subagent registry:
// the conversation epoch, the source revision, the views, the ids still settling
// their final-index save, and any bounded save errors. The views are copied under
// State.mu, then each live child's activity facts are read under the child's own
// locks with State.mu released. No Cancel callback and no mutable Child pointer
// is ever published.
func (s *State) SubagentSnapshot() SubagentSnapshot {
	s.mu.Lock()
	epoch := s.subagentEpochLocked()
	rev := s.subagentRevision
	views := make([]SubagentView, len(s.subagents))
	copy(views, s.subagents)
	var settlingIDs []int64
	for id, settling := range s.subagentSettling {
		if settling {
			settlingIDs = append(settlingIDs, id)
		}
	}
	s.mu.Unlock()

	// Read each live child's own facts outside the parent lock, then drop the
	// pointers: the projection must never hand out a Cancel callback or a
	// mutable Child.
	for i := range views {
		if child := views[i].Child; child != nil {
			views[i].ToolCalls = child.CompletedToolCallCount()
			views[i].CurrentTool = child.CurrentToolLabel()
		}
		views[i].Child = nil
		views[i].Cancel = nil
	}

	sort.Slice(settlingIDs, func(i, j int) bool { return settlingIDs[i] < settlingIDs[j] })
	settling := make([]string, 0, len(settlingIDs))
	for _, id := range settlingIDs {
		settling = append(settling, decimalAgentID(id))
	}

	// A work view that is not dirty has been written successfully, so every
	// recorded failure is covered by that save and must not be reported. This is
	// the same reconciliation RetryWorkViewSave reaches through the serialized
	// save: once it succeeds, the recorded failures are gone.
	s.workViewSaveMu.Lock()
	clean := !s.workViewDirty && len(s.workViewPendingAcks) == 0
	s.workViewSaveMu.Unlock()
	var saveErrors map[string]string
	if !clean {
		s.mu.Lock()
		if len(s.subagentSaveErrors) > 0 {
			saveErrors = make(map[string]string, len(s.subagentSaveErrors))
			for id, f := range s.subagentSaveErrors {
				saveErrors[decimalAgentID(id)] = f.Message
			}
		}
		s.mu.Unlock()
	}
	return SubagentSnapshot{
		Epoch:      epoch,
		Revision:   rev,
		Views:      views,
		Settling:   settling,
		SaveErrors: saveErrors,
	}
}

// BindSubagentReport links a subagent's already-appended report to its work
// index. It finds the exact report text, with the subagent-report content type,
// that was appended for this live id within its registration interval, and
// records the persisted Message.DBID — never the transient in-memory id. If
// persistence is enabled but the message was never persisted, the reference
// stays explicitly unavailable and an error is returned: the session never
// claims a durable report link it does not have.
func (s *State) BindSubagentReport(id int64, report string) error {
	if strings.TrimSpace(report) == "" {
		return fmt.Errorf("session: subagent %d report is empty", id)
	}
	s.mu.Lock()
	idx := -1
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("session: unknown subagent id %d", id)
	}
	start := s.subagents[idx].StartedAt
	end := s.subagents[idx].EndedAt
	var dbid int64
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.ContentType != ContentTypeSubagentReport || m.Content != report {
			continue
		}
		if m.CreatedAt.Before(start) {
			continue
		}
		if !end.IsZero() && m.CreatedAt.After(end) {
			continue
		}
		dbid = m.DBID
		break
	}
	ref := db.WorkReference{MessageID: dbid}
	if child := s.subagents[idx].Child; child != nil {
		ref.ChildSessionID = child.SessionID()
	}
	if dbid != 0 {
		// Only a real persisted row id becomes a reference. A failed match
		// leaves any earlier good link intact rather than overwriting it with
		// a fabricated zero.
		s.subagents[idx].Report = ref
	}
	rev := s.bumpSubagentRevisionLocked()
	epoch := s.subagentEpochLocked()
	updated := s.subagents[idx]
	persistenceEnabled := s.persistenceEnabled()
	s.mu.Unlock()

	s.publishSubagentEvent(s.subagentBroker, updated, epoch, rev)

	if persistenceEnabled && dbid == 0 {
		msg := fmt.Sprintf("subagent %d report is not durably persisted", id)
		s.recordSubagentSaveError(id, rev, msg)
		// Persist the index with the unavailable reference as evidence, then
		// report honestly that no durable link exists.
		if err := s.recordSubagentWorkResult(updated, rev); err != nil {
			s.logger.Error("save subagent report index failed", "error", err, "subagent_id", id)
		}
		s.logger.Error("bind subagent report: report message not persisted", "subagent_id", id)
		return errors.New(msg)
	}
	if err := s.recordSubagentWorkResult(updated, rev); err != nil {
		s.logger.Error("save subagent report index failed", "error", err, "subagent_id", id)
		return err
	}
	return nil
}

// normalizeRestoredAgentWork rewrites the restored agent-work index into history
// only: a stored entry that was still running when the previous process ended had
// no terminal result to restore, so it becomes an explicit unknown/owner-gone
// entry that keeps its ORIGINAL start time. It is never turned into a live
// SubagentView and grants no stop capability, and it never stamps the current
// time as if the work had just started. No cross-runtime id is parsed or matched.
func (s *State) normalizeRestoredAgentWork() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.workView.AgentResults {
		r := &s.workView.AgentResults[i]
		if r.State != "running" {
			continue
		}
		r.State = "unknown"
		if r.Outcome == "" {
			r.Outcome = "owner-gone"
		}
	}
}

// HasRunningSubagent reports whether any subagent is currently in the
// SubagentRunning state.
func (s *State) HasRunningSubagent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.subagents {
		if v.Status == SubagentRunning {
			return true
		}
	}
	return false
}

// Subagent returns the view with the given ID, or false if unknown.
func (s *State) Subagent(id int64) (SubagentView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.subagents {
		if v.ID == id {
			return v, true
		}
	}
	return SubagentView{}, false
}

// CompletedToolCallCount reports how many tool calls have finished in this
// session's audit log. The parent uses it to keep a subagent card's "N tool
// calls" figure current while the child is still running.
func (s *State) CompletedToolCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.auditLog)
}

// CurrentToolLabel returns a concise, human-readable label for the tool this
// session is currently running, or "" when idle. The parent uses it to show
// what a running subagent is doing on its card. For file-edit tools the label
// names the file being edited; other tools fall back to their display name.
func (s *State) CurrentToolLabel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ""
	}
	atc := *s.activeToolCall
	switch atc.Name {
	case "file.write_patch", "file.edit":
		if atc.Path != "" {
			return "editing " + atc.Path
		}
	}
	return atc.Name
}

// SetSubagentBroker wires the broker RegisterSubagent/FinishSubagent publish
// to. Mirrors SetWorkspaceBroker.
func (s *State) SetSubagentBroker(b *pubsub.Broker[SubagentEvent]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subagentBroker = b
}

// WaitSubagent blocks until the subagent with the given ID finishes (or the
// caller's context ends) and returns its final view. A subagent that has
// already finished returns immediately, so callers can fetch results from
// earlier turns. Unknown IDs return an error.
func (s *State) WaitSubagent(ctx context.Context, id int64) (SubagentView, error) {
	s.mu.Lock()
	for i := range s.subagents {
		if s.subagents[i].ID != id {
			continue
		}
		v := s.subagents[i]
		done := s.subagentDone[id]
		s.mu.Unlock()
		if v.Status != SubagentRunning || done == nil {
			return v, nil
		}
		select {
		case <-done:
			v, _ = s.Subagent(id)
			return v, nil
		case <-ctx.Done():
			return SubagentView{}, ctx.Err()
		}
	}
	s.mu.Unlock()
	return SubagentView{}, fmt.Errorf("session: unknown subagent id %d", id)
}

// WaitAnySubagent blocks until the first of ids finishes and returns its view.
//
// It exists because the concurrency cap (EnterSubagent) is a hard error: a
// parent at the cap that could only wait for one specific child or for *all*
// of them had no way to reclaim the single slot that frees first, so it
// waited for the whole batch. This is the first-to-finish primitive that lets
// it proceed as soon as any slot opens.
//
// A child that has already finished wins immediately — FinishSubagent deletes
// its done channel, so there is nothing left to select on and blocking would
// hang forever.
//
// The fan-in spawns one goroutine per still-running child. N is bounded by
// the session's cap (default 3, ceiling 8), so this is cheaper and far more
// readable than reflect.Select. The result channel is buffered to len so no
// loser can block on send, and stop is closed on return so every loser is
// released rather than parked until its child eventually finishes.
func (s *State) WaitAnySubagent(ctx context.Context, ids []int64) (SubagentView, error) {
	if len(ids) == 0 {
		return SubagentView{}, fmt.Errorf("session: WaitAnySubagent requires at least one id")
	}

	type waiter struct {
		id   int64
		done chan struct{}
	}

	s.mu.Lock()
	var waiters []waiter
	for _, id := range ids {
		var found *SubagentView
		for i := range s.subagents {
			if s.subagents[i].ID == id {
				v := s.subagents[i]
				found = &v
				break
			}
		}
		if found == nil {
			continue
		}
		done := s.subagentDone[id]
		if found.Status != SubagentRunning || done == nil {
			s.mu.Unlock()
			return *found, nil
		}
		waiters = append(waiters, waiter{id: id, done: done})
	}
	s.mu.Unlock()

	if len(waiters) == 0 {
		return SubagentView{}, fmt.Errorf("session: no known subagents among %v", ids)
	}

	winner := make(chan int64, len(waiters))
	stop := make(chan struct{})
	defer close(stop)

	for _, w := range waiters {
		w := w
		go func() {
			select {
			case <-w.done:
				winner <- w.id
			case <-ctx.Done():
			case <-stop:
			}
		}()
	}

	select {
	case id := <-winner:
		v, _ := s.Subagent(id)
		return v, nil
	case <-ctx.Done():
		return SubagentView{}, ctx.Err()
	}
}

// maxReasoningTailBytes caps how much of the in-progress reasoning buffer
// SubagentActivityTail scans. The reasoning buffer can grow unboundedly
// within one model call; without this cap, splitting it into lines would
// copy the entire buffer on every agent.await peek or TUI frame.
const maxReasoningTailBytes = 8192

// SubagentActivityTail returns up to n lines summarising what a running
// subagent is currently doing. It prefers the trailing end of streamed
// reasoning, then recent audit-log result summaries. It lives here (rather
// than duplicated in the agent and TUI packages) so the two consumers —
// agent.await's peek and the TUI card — cannot drift apart.
//
// M-4: only the trailing maxReasoningTailBytes of the reasoning buffer are
// scanned, so a very long reasoning stream does not cause the tail to
// copy the entire buffer on every call.
func (s *State) SubagentActivityTail(n int) []string {
	if n <= 0 {
		return nil
	}
	// Narration is what the child says it is doing, in its own words. It is
	// a better answer to "what is this agent up to" than streamed
	// reasoning, which is verbose and caught mid-thought. Falls through to
	// reasoning, then audit summaries, when the model sends no preamble.
	if msgs := s.Messages(); len(msgs) > 0 {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].ContentType != ContentTypeNarration {
				continue
			}
			lines := strings.Split(strings.TrimSpace(msgs[i].Content), "\n")
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			return lines
		}
	}
	ip := s.InProgress()
	if ip.Reasoning != "" {
		// M-4: only scan the trailing portion to avoid splitting a
		// potentially huge buffer on every call.
		tail := ip.Reasoning
		if len(tail) > maxReasoningTailBytes {
			tail = tail[len(tail)-maxReasoningTailBytes:]
			// Drop the partial first line so we start at a line boundary.
			if idx := strings.IndexByte(tail, '\n'); idx >= 0 {
				tail = tail[idx+1:]
			}
		}
		lines := strings.Split(strings.TrimSpace(tail), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return lines
	}
	log := s.AuditLog()
	if len(log) == 0 {
		return nil
	}
	var out []string
	for i := len(log) - 1; i >= 0 && len(out) < n; i-- {
		if log[i].ResultSummary != "" {
			out = append([]string{log[i].ResultSummary}, out...)
		}
	}
	return out
}
