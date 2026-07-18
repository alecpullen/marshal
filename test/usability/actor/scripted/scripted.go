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

// Step is one scripted interaction.
type Step struct {
	Send    string  `json:"send"`
	SendKey string  `json:"send_key"`
	WaitFor WaitFor `json:"wait_for"`
}

// WaitFor describes the screen condition required before this step executes.
type WaitFor struct {
	ScreenContains string           `json:"screen_contains"`
	State          UIStatePredicate `json:"state"`
}

// UIStatePredicate matches fields in screen.UIState. nil fields are ignored.
type UIStatePredicate struct {
	HelpOpen        *bool `json:"help_open,omitempty"`
	PendingApproval *bool `json:"pending_approval,omitempty"`
	PendingQuestion *bool `json:"pending_question,omitempty"`
	Busy            *bool `json:"busy,omitempty"`
}

// Act returns the next action if the current screen satisfies the next step's wait condition.
func (s *Scripted) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.Steps) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}

	step := s.Steps[s.pos]
	if !matchesWaitFor(sc, step.WaitFor) {
		// Wait condition not met yet; ask harness to wait and try again.
		return actor.Action{Type: actor.ActionNoOp}, nil
	}

	s.pos++
	if step.Send != "" {
		return actor.Action{Type: actor.ActionType, Text: step.Send}, nil
	}
	if step.SendKey != "" {
		return actor.Action{Type: actor.ActionKey, Key: step.SendKey}, nil
	}
	return actor.Action{}, fmt.Errorf("step %d has no send or send_key", s.pos)
}

func matchesWaitFor(sc screen.Screen, wf WaitFor) bool {
	if wf.ScreenContains != "" && !strings.Contains(sc.Content, wf.ScreenContains) {
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
