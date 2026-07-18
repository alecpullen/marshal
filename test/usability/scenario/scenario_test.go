package scenario

import (
	"context"
	"testing"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

type stubActor struct {
	actions []actor.Action
	pos     int
}

func (s *stubActor) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.actions) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}
	a := s.actions[s.pos]
	s.pos++
	return a, nil
}

func TestRunnerCountsKeystrokes(t *testing.T) {
	act := &stubActor{
		actions: []actor.Action{
			{Type: actor.ActionType, Text: "hi"},
			{Type: actor.ActionKey, Key: "enter"},
		},
	}
	sc := Scenario{
		Name:    "stub",
		Actor:   act,
		Success: SuccessCriterion{},
	}

	r := NewRunner(RunnerConfig{BinaryPath: "cat", WorkDir: t.TempDir()})
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Keystrokes != 3 { // h, i, enter
		t.Fatalf("Keystrokes = %d, want 3", res.Keystrokes)
	}
	if !res.Success {
		t.Fatalf("Success = false, want true")
	}
}
