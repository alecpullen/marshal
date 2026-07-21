package session

import (
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

// Question is a single clarifying question presented to the user. Options
// triggers select/multi-select rendering in the TUI; Multi selects between
// NewSelect and NewMultiSelect. AllowOther enables the "Other" affordance
// that lets the user type a free-text answer that's not in the option list.
type Question struct {
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Multi      bool     `json:"multi,omitempty"`
	AllowOther bool     `json:"allow_other,omitempty"`
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
	StartedAt time.Time
}

func (s *State) SetPendingApproval(tc *PendingToolCall) {
	s.mu.Lock()
	s.pendingApproval = tc
	var snap *PendingToolCall
	if tc != nil {
		copy := *tc
		snap = &copy
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
		copy := *q
		snap = &copy
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: snap})
}

func (s *State) PendingQuestion() *PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingQuestion
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
