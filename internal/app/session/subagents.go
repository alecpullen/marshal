package session

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

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
	// agent.await / agent.output can report why the child failed. Empty
	// for running or successful subagents.
	Error string
}

// SubagentMeta is the optional metadata passed to RegisterSubagentWithMeta.
type SubagentMeta struct {
	Role     routing.AgentRole
	Provider string
	Model    string
	Fallback bool
}

// SubagentEvent is published on the subagent broker when a subagent is
// registered or transitions status, so the TUI re-renders the card without
// polling.
type SubagentEvent struct {
	View SubagentView
}

// subagentSeq allocates process-unique subagent IDs. IDs only need to be
// unique within a parent session's lifetime; an atomic counter avoids a
// State.mu-held increment and keeps registration cheap.
var subagentSeq atomic.Int64

// RegisterSubagent records a new running subagent and returns its view. The
// returned view's Child pointer is the live child state; the caller owns
// finishing the registration via FinishSubagent. Publishing happens after
// the mutex is released, matching SetWorkspace.
func (s *State) RegisterSubagent(label string, child *State) SubagentView {
	v := SubagentView{
		ID:        subagentSeq.Add(1),
		Label:     label,
		Status:    SubagentRunning,
		Child:     child,
		StartedAt: time.Now(),
	}
	s.mu.Lock()
	s.subagents = append(s.subagents, v)
	s.subagentDone[v.ID] = make(chan struct{})
	broker := s.subagentBroker
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("subagent", SubagentEvent{View: v})
	}
	return v
}

// RegisterSubagentWithMeta records a new running subagent and returns its
// view, carrying the dispatched agent's route provenance. It mirrors
// RegisterSubagent but additionally records Role, Provider, Model, and
// Fallback so the TUI can display what model is actually running (or ran)
// the subagent.
func (s *State) RegisterSubagentWithMeta(label string, child *State, meta SubagentMeta) SubagentView {
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
	s.mu.Lock()
	s.subagents = append(s.subagents, v)
	s.subagentDone[v.ID] = make(chan struct{})
	broker := s.subagentBroker
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("subagent", SubagentEvent{View: v})
	}
	return v
}

// SetSubagentCancel stores the cancel function for a running subagent
// so the TUI can interrupt that specific child.
func (s *State) SetSubagentCancel(id int64, cancel context.CancelFunc) {
	s.mu.Lock()
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].Cancel = cancel
			break
		}
	}
	s.mu.Unlock()
}

// SetSubagentSalvaged records the reason a subagent was salvaged on its
// view. Called by the agent.run handler before FinishSubagent so the TUI
// card can render a salvage marker.
func (s *State) SetSubagentSalvaged(id int64, reason string) {
	s.mu.Lock()
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].SalvagedReason = reason
			break
		}
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if broker != nil {
		// Publish a copy so subscribers see the updated reason. The view
		// is still running until FinishSubagent is called.
		if v, ok := s.Subagent(id); ok {
			broker.Publish("subagent", SubagentEvent{View: v})
		}
	}
}

// CancelSubagent cancels the context of a running subagent with the
// given view ID. It returns true if a running subagent was found and a
// cancel function existed. The subagent handler is responsible for
// finishing the view when the child returns.
func (s *State) CancelSubagent(id int64) bool {
	s.mu.Lock()
	var cancel context.CancelFunc
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id && s.subagents[i].Status == SubagentRunning {
			cancel = s.subagents[i].Cancel
			found = true
			break
		}
	}
	s.mu.Unlock()
	if found && cancel != nil {
		cancel()
		return true
	}
	return false
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
	broker := s.subagentBroker
	s.mu.Unlock()
	if found && broker != nil {
		broker.Publish("subagent", SubagentEvent{View: updated})
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
	var updated SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].EndedAt = time.Now()
			s.subagents[i].Summary = summary
			if err != nil {
				s.subagents[i].Status = SubagentFailed
				s.subagents[i].Error = err.Error()
			} else {
				s.subagents[i].Status = SubagentDone
			}
			updated = s.subagents[i]
			found = true
			break
		}
	}
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
	var done chan struct{}
	if found {
		done = s.subagentDone[id]
		delete(s.subagentDone, id)
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
	if found && broker != nil {
		broker.Publish("subagent", SubagentEvent{View: updated})
	}
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
	case "file.write_patch":
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
// copy the entire buffer on every agent.output call or TUI frame.
const maxReasoningTailBytes = 8192

// SubagentActivityTail returns up to n lines summarising what a running
// subagent is currently doing. It prefers the trailing end of streamed
// reasoning, then recent audit-log result summaries. It lives here (rather
// than duplicated in the agent and TUI packages) so the two consumers —
// agent.output and the TUI card — cannot drift apart.
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
