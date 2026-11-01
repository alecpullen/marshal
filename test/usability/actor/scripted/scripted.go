package scripted

import (
	"context"
	"fmt"
	"strings"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

// Scripted is a deterministic actor driven by a list of steps.
type Scripted struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
	pos   int
}

// Step is one scripted interaction. Exactly one of Send, SendKey or WaitOnly
// must be set: a step either sends input or waits for a visible outcome
// without sending anything.
type Step struct {
	Send     string  `json:"send"`
	SendKey  string  `json:"send_key"`
	WaitFor  WaitFor `json:"wait_for"`
	WaitOnly bool    `json:"wait_only,omitempty"`
}

// WaitFor describes the screen condition required before this step executes.
// Every populated field must match: the predicates are conjunctive.
type WaitFor struct {
	ScreenContains    string           `json:"screen_contains"`
	ScreenNotContains string           `json:"screen_not_contains,omitempty"`
	State             UIStatePredicate `json:"state"`
}

// UIStatePredicate matches fields in screen.UIState. nil fields are ignored.
type UIStatePredicate struct {
	HelpOpen        *bool `json:"help_open,omitempty"`
	PendingApproval *bool `json:"pending_approval,omitempty"`
	PendingQuestion *bool `json:"pending_question,omitempty"`
	Busy            *bool `json:"busy,omitempty"`
}

// hasCondition reports whether at least one predicate is specified.
func (wf WaitFor) hasCondition() bool {
	return wf.ScreenContains != "" ||
		wf.ScreenNotContains != "" ||
		wf.State.HelpOpen != nil ||
		wf.State.PendingApproval != nil ||
		wf.State.PendingQuestion != nil ||
		wf.State.Busy != nil
}

// validate rejects steps whose action is missing or ambiguous, and wait-only
// steps that wait for nothing.
func (st Step) validate(pos int) error {
	actions := 0
	if st.Send != "" {
		actions++
	}
	if st.SendKey != "" {
		actions++
	}
	if st.WaitOnly {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("scripted: step %d must set exactly one of send, send_key or wait_only, got %d", pos, actions)
	}
	if st.WaitOnly && !st.WaitFor.hasCondition() {
		return fmt.Errorf("scripted: wait-only step %d has no condition", pos)
	}
	return nil
}

// Act returns the next action if the current screen satisfies the next step's wait condition.
func (s *Scripted) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.Steps) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}

	step := s.Steps[s.pos]
	if err := step.validate(s.pos); err != nil {
		// Do not advance: an invalid step can never be satisfied.
		return actor.Action{}, err
	}
	if !matchesWaitFor(sc, step.WaitFor) {
		// Wait condition not met yet; ask harness to wait and try again.
		return actor.Action{Type: actor.ActionNoOp}, nil
	}

	s.pos++
	if step.WaitOnly {
		// The postcondition held on this viewport: report a no-op so the
		// following observation falls through to the done branch.
		return actor.Action{Type: actor.ActionNoOp}, nil
	}
	if step.Send != "" {
		return actor.Action{Type: actor.ActionType, Text: step.Send}, nil
	}
	return actor.Action{Type: actor.ActionKey, Key: step.SendKey}, nil
}

func matchesWaitFor(sc screen.Screen, wf WaitFor) bool {
	if wf.ScreenContains != "" && !strings.Contains(sc.Content, wf.ScreenContains) {
		return false
	}
	if wf.ScreenNotContains != "" && strings.Contains(sc.Content, wf.ScreenNotContains) {
		return false
	}
	if wf.State.HelpOpen != nil && sc.State.HelpOpen != *wf.State.HelpOpen {
		return false
	}
	if wf.State.PendingApproval != nil && sc.State.PendingApproval != *wf.State.PendingApproval {
		return false
	}
	if wf.State.PendingQuestion != nil && sc.State.PendingQuestion != *wf.State.PendingQuestion {
		return false
	}
	if wf.State.Busy != nil && sc.State.Busy != *wf.State.Busy {
		return false
	}
	return true
}
