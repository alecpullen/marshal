package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// UserApprovalDecision is the user's response to a pending tool approval
// request. Approved indicates the user allowed the call; Edited carries the
// user's modified command text (if any) for tools that support editing.
type UserApprovalDecision struct {
	Approved bool
	Edited   string
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
	ID           string
	Name         string
	Args         string
	Command      string
	Risk         string
	Reason       string
	Diff         string
	Schema       string // Details / schema / description of the tool
	ResponseChan chan UserApprovalDecision
	responded    sync.Once
}

// Respond sends a UserApprovalDecision to the response channel exactly once
// (guarded by sync.Once). It is safe to call multiple times; only the first
// call produces a send. The send is non-blocking so unbuffered channels with
// no receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
func (p *PendingToolCall) Respond(d UserApprovalDecision) {
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

type ActiveToolCall struct {
	Name      string
	Args      string
	Path      string
	Output    string
	StartedAt time.Time
}

func (s *State) SetPendingApproval(tc *PendingToolCall) {
	s.mu.Lock()
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
			ResponseChan: tc.ResponseChan,
		}
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: snap})
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

// PendingChildQuestion carries one or more Questions from a background
// subagent to its parent, awaiting an answer. The child blocks on
// ResponseChan; the parent (or, on escalation, the user) sends exactly one
// value, one Answer per Question (in the same order). ResponseChan MUST be
// buffered (cap 1): Respond uses a non-blocking send, so an unbuffered
// channel would silently drop a late answer after the child timed out.
type PendingChildQuestion struct {
	ChildID      int64
	ChildDesc    string
	Questions    []Question
	ResponseChan chan []Answer
	responded    sync.Once
}

// Respond sends answers to the response channel exactly once (guarded by
// sync.Once). Non-blocking send then close, mirroring PendingQuestion.
func (p *PendingChildQuestion) Respond(a []Answer) {
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

// PushChildQuestion appends a subagent question to the parent's FIFO queue
// and publishes the queue HEAD as the EventChildQuestionChanged snapshot —
// the head is the answerable question, so subscribers (TUI/ACP) and the
// parent loop-top hint always target it. Concurrent children queue behind
// one another; the earlier asker is never displaced (spec §7).
func (s *State) PushChildQuestion(q *PendingChildQuestion) {
	s.mu.Lock()
	s.childQuestions = append(s.childQuestions, q)
	head := s.childQuestionHeadLocked()
	snap := snapshotChildQuestionLocked(head)
	s.mu.Unlock()
	s.publishEvent(EventChildQuestionChanged, Event{PendingChildQuestion: snap})
}

// ResolveChildQuestion removes exactly the given question from the queue
// (by identity) and publishes the new head. Resolving a question that is
// not queued is a no-op — a timed-out child teardown can never wipe a
// sibling's still-waiting question.
func (s *State) ResolveChildQuestion(q *PendingChildQuestion) {
	s.mu.Lock()
	for i, queued := range s.childQuestions {
		if queued == q {
			s.childQuestions = append(s.childQuestions[:i], s.childQuestions[i+1:]...)
			break
		}
	}
	head := s.childQuestionHeadLocked()
	snap := snapshotChildQuestionLocked(head)
	s.mu.Unlock()
	s.publishEvent(EventChildQuestionChanged, Event{PendingChildQuestion: snap})
}

// PendingChildQuestion returns the queue head — the subagent question the
// parent should answer first. nil when no child is waiting.
func (s *State) PendingChildQuestion() *PendingChildQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childQuestionHeadLocked()
}

// ChildQuestions returns a snapshot of the FIFO queue, head first. Used by
// the loop-top hint and parent.respond to report queue depth and by
// ResolvePendingForShutdown to answer every waiter.
func (s *State) ChildQuestions() []*PendingChildQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*PendingChildQuestion(nil), s.childQuestions...)
}

// childQuestionHeadLocked returns the queue head, or nil when empty.
// Callers must hold s.mu.
func (s *State) childQuestionHeadLocked() *PendingChildQuestion {
	if len(s.childQuestions) == 0 {
		return nil
	}
	return s.childQuestions[0]
}

// snapshotChildQuestionLocked builds the event-safe copy of a queued
// question (Questions copied; ResponseChan shared deliberately so a
// subscriber can answer). Callers must hold s.mu.
func snapshotChildQuestionLocked(q *PendingChildQuestion) *PendingChildQuestion {
	if q == nil {
		return nil
	}
	return &PendingChildQuestion{
		ChildID:      q.ChildID,
		ChildDesc:    q.ChildDesc,
		Questions:    append([]Question(nil), q.Questions...),
		ResponseChan: q.ResponseChan,
	}
}

func (s *State) SetActiveToolCall(atc ActiveToolCall) {
	s.mu.Lock()
	s.activeToolCall = &atc
	copy := atc
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: &copy})
}

func (s *State) ActiveToolCall() (ActiveToolCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ActiveToolCall{}, false
	}
	return *s.activeToolCall, true
}

func (s *State) ClearActiveToolCall() {
	s.mu.Lock()
	s.activeToolCall = nil
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: nil})
}

func (s *State) AppendActiveToolCallOutput(delta string) {
	s.mu.Lock()
	if s.activeToolCall == nil {
		s.mu.Unlock()
		return
	}
	s.activeToolCall.Output += delta
	s.mu.Unlock()
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
func (s *State) SetActiveToolCallArgs(args string) {
	s.mu.Lock()
	if s.activeToolCall == nil {
		s.mu.Unlock()
		return
	}
	s.activeToolCall.Args = args
	copy := *s.activeToolCall
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
