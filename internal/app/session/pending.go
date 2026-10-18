package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// AccessState is the effective access tuple for one session: the interaction
// mode governing approval decisions plus whether the conversation holds the
// system-access modifier. It is a value snapshot (see State.AccessState), so a
// reader cannot observe a half-applied pair.
type AccessState struct {
	Mode   string `json:"mode"`
	System bool   `json:"system"`
}

// AccessIntent is what a caller asked for before the session decided whether
// anything is actually missing: which capabilities the call needs and why.
// The session, not the caller, derives the request's missing flags from the
// live access pair.
type AccessIntent struct {
	Editing bool
	System  bool
	Reason  string
}

// AccessRequest is the displayable, immutable metadata for one outstanding
// access grant request. It carries no channel: the canonical
// PendingToolCall owns settlement (see State.RegisterAccess).
type AccessRequest struct {
	RequestID      string      `json:"requestId"`
	SessionID      string      `json:"sessionId"`
	Actor          string      `json:"actor,omitempty"`
	StepID         StepID      `json:"stepId,omitempty"`
	Reason         string      `json:"reason"`
	Current        AccessState `json:"current"`
	MissingEditing bool        `json:"missingEditing"`
	MissingSystem  bool        `json:"missingSystem"`
}

// AccessChoice is the user's answer to an AccessRequest. Current is the pair
// the chooser displayed; a mismatch with the live pair means the request is
// stale and must be refreshed rather than applied.
type AccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Approved  bool        `json:"approved"`
	Mode      string      `json:"mode,omitempty"`
}

// AccessStatus classifies how one access request settled.
type AccessStatus string

const (
	AccessAvailable AccessStatus = "already_available"
	AccessGranted   AccessStatus = "granted"
	AccessDenied    AccessStatus = "denied"
	AccessCanceled  AccessStatus = "canceled"
	AccessFailed    AccessStatus = "failed"
	AccessRefresh   AccessStatus = "refresh_required"
	AccessExpired   AccessStatus = "expired"
)

// AccessResult is the outcome of an access request. Effective is the access
// pair in force when the request settled, so a caller never has to re-read
// the session to know what it is operating under.
type AccessResult struct {
	RequestID string       `json:"requestId,omitempty"`
	SessionID string       `json:"sessionId"`
	Status    AccessStatus `json:"status"`
	Effective AccessState  `json:"effective"`
	Message   string       `json:"message,omitempty"`
}

// Prepare validates everything, returning an infallible, bounded memory-only
// policy commit. It must not mutate State, acquire the access gate, do I/O,
// reload, or call a provider. A nil Apply is invalid.
type AccessCommit struct {
	Effective AccessState
	Apply     func()
}
type AccessPrepare func(AccessRequest, AccessChoice) (AccessCommit, error)

// UserApprovalDecision is the user's response to a pending tool approval
// request. Approved indicates the user allowed the call; Edited carries the
// user's modified command text (if any) for tools that support editing.
// Access carries the settled access result for a typed access request; it is
// nil for ordinary approvals.
type UserApprovalDecision struct {
	Approved bool
	Edited   string
	Access   *AccessResult
}

// QuestionOption is one selectable answer to a Question. Plain strings
// from older payloads decode as {Label: s} via the custom unmarshaller.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// UnmarshalJSON accepts either a plain string (legacy / simple form) or
// an object with label + optional description.
func (o *QuestionOption) UnmarshalJSON(b []byte) error {
	// json.Unmarshal treats the null literal as a no-op success for every
	// target type, so without this guard a null option would take the
	// string branch below and decode as an empty label — a blank selectable
	// row whose recorded answer is "". The declared schema rejects null, so
	// the decoder must too.
	if strings.TrimSpace(string(b)) == "null" {
		return fmt.Errorf("question option: must be a string or an object with label/description, got null")
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		o.Label = s
		o.Description = ""
		return nil
	}
	type alias QuestionOption
	if err := json.Unmarshal(b, (*alias)(o)); err != nil {
		return err
	}
	// An object with no label is not a usable option; reject it so the
	// shape stays string-or-{label,...} rather than silently empty.
	if o.Label == "" {
		return fmt.Errorf("question option: missing label")
	}
	return nil
}

// Question is a single clarifying question presented to the user. Options
// triggers select/multi-select rendering in the TUI; Multi selects between
// NewSelect and NewMultiSelect. Every options question always exposes an
// 'Other…' sentinel in the TUI, which reveals a free-text input for a custom
// answer not in the option list; the sentinel and its input are unconditional
// (there is no per-question switch to disable them).
type Question struct {
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multi    bool             `json:"multi,omitempty"`
}

// Answer is the user's response to one Question. When the user hits Esc on
// a question, the TUI records the literal string AnswerUnanswered so the
// agent can see exactly which questions were skipped.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AnswerUnanswered is the sentinel Answer.Answer value recorded when the
// user hits Esc on a question without picking a value. The runner detects
// this sentinel and treats the question as skipped (see runner.go's
// allUnanswered tracking).
const AnswerUnanswered = "Unanswered"

// UnansweredAnswers returns one AnswerUnanswered answer per question, in
// order. Used by shutdown paths and by transports that cannot ask
// interactively (ACP) or pre-fill dismissals (TUI question form).
func UnansweredAnswers(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for i, q := range questions {
		answers[i] = Answer{Question: q.Question, Answer: AnswerUnanswered}
	}
	return answers
}

// PendingQuestion carries one or more Questions from the agent awaiting
// user response. The runner blocks on ResponseChan; the TUI sends exactly
// one value, one Answer per Question (in the same order).
type PendingQuestion struct {
	Questions    []Question
	ResponseChan chan []Answer
	responded    sync.Once
}

// Respond sends answers to the response channel exactly once (guarded by
// sync.Once). It is safe to call multiple times; only the first call
// produces a send. The send is non-blocking so unbuffered channels with no
// receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
func (p *PendingQuestion) Respond(a []Answer) {
	if p == nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- a:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

type PendingToolCall struct {
	ID      string
	Name    string
	Args    string
	Command string
	Risk    string
	Reason  string
	Diff    string
	Schema  string // Details / schema / description of the tool
	// StepID is the step (model response) that requested the call, so the
	// approval panel can show that step's narration as the reason. 0 = none.
	StepID       StepID
	ResponseChan chan UserApprovalDecision
	// Access, when non-nil, marks this as a typed capability request rather
	// than an ordinary tool approval. The metadata is display-only (it
	// carries no channel); settlement goes through the session's private
	// helper so a grant can never be acknowledged before it was applied.
	Access    *AccessRequest
	responded sync.Once
}

// Respond sends a UserApprovalDecision to the response channel exactly once
// (guarded by sync.Once). It is safe to call multiple times; only the first
// call produces a send. The send is non-blocking so unbuffered channels with
// no receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
//
// A typed access request is settled only by the session's private settlement
// helper (finishAccessLocked), which is the single point that may acknowledge
// an applied grant; Respond is a no-op for it so an untyped or stale
// responder cannot report success.
func (p *PendingToolCall) Respond(d UserApprovalDecision) {
	if p == nil {
		return
	}
	if p.Access != nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- d:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

type ActiveToolCall struct {
	Name      string
	Args      string
	Path      string
	Output    string
	StartedAt time.Time
	// StepID is the step that requested the call; ToolCallID is the call's
	// ID (provider's, or synthesised in envelope mode). An empty ToolCallID
	// keys the single legacy slot.
	StepID     StepID
	ToolCallID string
}

// SetPendingApproval installs the pending tool approval and publishes an
// event-safe snapshot, or clears the slot when tc is nil.
//
// The access gate is taken first so an ordinary call and a capability request
// cannot race for the single slot. While a typed access request occupies it,
// a generic nil clear is ignored (the session's own settlement helper is the
// only thing that may clear it) and a replacement ordinary approval is denied
// rather than silently shown in place of the request the user is answering.
// When no access request occupies the slot, existing behavior is unchanged —
// including an ordinary P1→P2 replacement.
func (s *State) SetPendingApproval(tc *PendingToolCall) {
	s.accessMu.Lock()
	s.mu.Lock()
	occupiedAccess := s.pendingApproval != nil && s.pendingApproval.Access != nil
	if occupiedAccess {
		// The user is answering a typed access request: only the session's
		// own settlement helper may clear it, and nothing may take its
		// place. A generic nil clear is ignored; a competing ordinary
		// approval is denied on its own channel after the locks are
		// released, so the denial never runs under the access gate.
		s.mu.Unlock()
		s.accessMu.Unlock()
		if tc != nil {
			tc.Respond(UserApprovalDecision{Approved: false})
		}
		return
	}
	s.pendingApproval = tc
	var snap *PendingToolCall
	if tc != nil {
		snap = &PendingToolCall{
			ID:           tc.ID,
			Name:         tc.Name,
			Args:         tc.Args,
			Command:      tc.Command,
			Risk:         tc.Risk,
			Reason:       tc.Reason,
			Diff:         tc.Diff,
			Schema:       tc.Schema,
			StepID:       tc.StepID,
			ResponseChan: tc.ResponseChan,
			// Access metadata travels with a typed display copy, but never a
			// channel: only the session's private helper may settle one.
			Access: copyAccessRequest(tc.Access),
		}
	}
	s.mu.Unlock()
	s.accessMu.Unlock()
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: snap})
}

// copyAccessRequest returns a private copy of displayable request metadata, so
// a published or stored snapshot cannot be mutated through the canonical
// request. Nil stays nil.
func copyAccessRequest(req *AccessRequest) *AccessRequest {
	if req == nil {
		return nil
	}
	copied := *req
	return &copied
}

func (s *State) PendingApproval() *PendingToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingApproval
}

func (s *State) SetPendingQuestion(q *PendingQuestion) {
	s.mu.Lock()
	s.pendingQuestion = q
	var snap *PendingQuestion
	if q != nil {
		snap = &PendingQuestion{
			Questions:    append([]Question(nil), q.Questions...),
			ResponseChan: q.ResponseChan,
		}
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: snap})
}

func (s *State) PendingQuestion() *PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingQuestion
}

// SetActiveToolCall registers an in-flight call under its ToolCallID (an
// empty ID uses the legacy single slot) and makes it the "latest" one that
// ActiveToolCall, output appends and args updates act on.
func (s *State) SetActiveToolCall(atc ActiveToolCall) {
	s.mu.Lock()
	if s.activeTools == nil {
		s.activeTools = map[string]*ActiveToolCall{}
	}
	stored := atc
	s.activeTools[atc.ToolCallID] = &stored
	s.activeToolCall = &stored
	copy := atc
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: &copy})
}

// ActiveToolCall returns the latest in-flight call.
func (s *State) ActiveToolCall() (ActiveToolCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ActiveToolCall{}, false
	}
	return *s.activeToolCall, true
}

// ActiveToolCalls returns every in-flight call, oldest first.
func (s *State) ActiveToolCalls() []ActiveToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ActiveToolCall, 0, len(s.activeTools))
	for _, a := range s.activeTools {
		out = append(out, *a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ToolCallID < out[j].ToolCallID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// ClearActiveToolCallID removes one in-flight call. When others remain, the
// most recently started becomes the latest and is published; the event
// carries nil only once nothing is running.
func (s *State) ClearActiveToolCallID(id string) {
	s.mu.Lock()
	cur, ok := s.activeTools[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.activeTools, id)
	if s.activeToolCall == cur {
		s.activeToolCall = nil
		for _, a := range s.activeTools {
			if s.activeToolCall == nil || a.StartedAt.After(s.activeToolCall.StartedAt) {
				s.activeToolCall = a
			}
		}
	}
	var snap *ActiveToolCall
	if s.activeToolCall != nil {
		c := *s.activeToolCall
		snap = &c
	}
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: snap})
}

// ClearActiveToolCall drops every in-flight call.
func (s *State) ClearActiveToolCall() {
	s.mu.Lock()
	s.activeTools = nil
	s.activeToolCall = nil
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: nil})
}

func (s *State) AppendActiveToolCallOutput(id, delta string) {
	s.mu.Lock()
	cur := s.activeToolLocked(id)
	if cur == nil {
		s.mu.Unlock()
		return
	}
	cur.Output += delta
	s.mu.Unlock()
}

// activeToolLocked finds the in-flight call with the given ID. An empty or
// unknown ID falls back to the latest call, for sources that carry no ID.
func (s *State) activeToolLocked(id string) *ActiveToolCall {
	if a, ok := s.activeTools[id]; ok && id != "" {
		return a
	}
	return s.activeToolCall
}

// SetActiveToolCallArgs updates only the Args field of the in-flight tool
// call, leaving Name, Path, Output and StartedAt intact. StartedAt in
// particular drives the elapsed-time display, so replacing the whole struct
// would restart the timer on every update.
//
// It exists so a long blocking call can keep the user informed about what it
// is still waiting on — agent.await counts its outstanding children down as
// they finish. Mirrors AppendActiveToolCallOutput's in-place mutation, but
// publishes EventActiveToolChanged: that one stays silent because streaming
// output would flood the broker, whereas an args update fires at most once
// per child completion and should reach ACP clients as well as the TUI.
func (s *State) SetActiveToolCallArgs(id, args string) {
	s.mu.Lock()
	cur := s.activeToolLocked(id)
	if cur == nil {
		s.mu.Unlock()
		return
	}
	cur.Args = args
	copy := *cur
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: &copy})
}

// SkillGateChoice is the user's response to a pending skill-gate prompt.
type SkillGateChoice int

const (
	SkillGateAllowOnce  SkillGateChoice = iota
	SkillGateAllowSkill                 // allow always: this skill, this session
	SkillGateAllowAll                   // allow always: all skills this session (gate off)
	SkillGateDeny
)

// PendingSkillGate carries one skill-load gate prompt from the agent
// awaiting user response. The runner blocks on ResponseChan; the TUI (or
// the ACP permission bridge) sends exactly one choice. Mirrors
// PendingToolCall's once-only Respond protocol.
type PendingSkillGate struct {
	Skill        string
	Description  string
	Reason       string
	ResponseChan chan SkillGateChoice
	responded    sync.Once
}

// Respond sends a choice to the response channel exactly once (guarded by
// sync.Once). It is safe to call multiple times; only the first call
// produces a send. The send is non-blocking so unbuffered channels with no
// receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
func (p *PendingSkillGate) Respond(c SkillGateChoice) {
	if p == nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- c:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

func (s *State) SetPendingSkillGate(sg *PendingSkillGate) {
	s.mu.Lock()
	s.pendingSkillGate = sg
	var snap *PendingSkillGate
	if sg != nil {
		snap = &PendingSkillGate{
			Skill:        sg.Skill,
			Description:  sg.Description,
			Reason:       sg.Reason,
			ResponseChan: sg.ResponseChan,
		}
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingSkillGateChanged, Event{PendingSkillGate: snap})
}

func (s *State) PendingSkillGate() *PendingSkillGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingSkillGate
}

// SudoAuthDecision is the user's response to a pending privileged-command
// authentication prompt. Password is the secret read from the masked TUI
// input; the caller MUST zero it immediately after use. OK=false means the
// user denied (Esc), and Password is nil.
type SudoAuthDecision struct {
	OK       bool
	Password []byte
}

// PendingSudoAuth carries one privileged-command authentication prompt from
// the shell.sudo tool awaiting the user's password. The handler blocks on
// ResponseChan; the TUI sends exactly one decision. Mirrors PendingToolCall's
// once-only Respond protocol.
//
// The password never travels on an Event: SetPendingSudoAuth publishes a
// snapshot carrying only ResponseChan, exactly as SetPendingApproval does
// (pending.go:171). The secret exists solely in the decision value on that
// channel, and the handler zeroes it immediately after Run returns.
type PendingSudoAuth struct {
	Command string
	Reason  string
	StepID  StepID
	// AllowUnlock reports whether the session has opt-in unlock enabled, so
	// the modal can say which sudoers knob governs the grace window.
	AllowUnlock bool
	// Attempt is the 1-based attempt number; >1 means a previous password was
	// rejected.
	Attempt int
	// Error is the message from the previous failed attempt ("" on the first).
	Error        string
	ResponseChan chan SudoAuthDecision
	responded    sync.Once
}

// Respond sends a decision to the response channel exactly once (guarded by
// sync.Once). Safe to call multiple times; only the first call produces a
// send. The send is non-blocking so an unbuffered channel with no receiver
// does not deadlock. The channel is closed after the send so the handler can
// detect that no more responses are coming.
func (p *PendingSudoAuth) Respond(d SudoAuthDecision) {
	if p == nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- d:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

// SetPendingSudoAuth installs the pending prompt and publishes an event-safe
// snapshot. The snapshot carries only ResponseChan, never a password, so no
// subscriber can observe a credential.
func (s *State) SetPendingSudoAuth(sa *PendingSudoAuth) {
	s.mu.Lock()
	s.pendingSudoAuth = sa
	var snap *PendingSudoAuth
	if sa != nil {
		snap = &PendingSudoAuth{
			Command:      sa.Command,
			Reason:       sa.Reason,
			StepID:       sa.StepID,
			AllowUnlock:  sa.AllowUnlock,
			Attempt:      sa.Attempt,
			Error:        sa.Error,
			ResponseChan: sa.ResponseChan,
		}
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingSudoAuthChanged, Event{PendingSudoAuth: snap})
}

// PendingSudoAuth returns the pending privileged-auth prompt, or nil.
func (s *State) PendingSudoAuth() *PendingSudoAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingSudoAuth
}
