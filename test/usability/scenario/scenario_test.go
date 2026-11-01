package scenario

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestRunnerFailsOnTerminalModelError(t *testing.T) {
	// The child emits a screen-changing control the model rejects (DECSTBM).
	// Even though the actor would report success, the runner must fail: an
	// untrustworthy screen can never produce a passing scenario.
	bin := filepath.Join(t.TempDir(), "helper.sh")
	body := "#!/bin/sh\nprintf 'ok\\033[2;5r'\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	r := NewRunner(RunnerConfig{BinaryPath: bin, WorkDir: t.TempDir()})
	res, err := r.Run(context.Background(), Scenario{Name: "decode-error", Actor: &stubActor{}})
	if err == nil {
		t.Fatalf("Run error = nil, want the terminal model error; result = %+v", res)
	}
	if !strings.Contains(err.Error(), "terminal model") {
		t.Fatalf("Run error = %v, want the terminal model error", err)
	}
	if res.Success {
		t.Fatalf("Success = true, want false when the screen cannot be decoded")
	}
	if res.Error == "" {
		t.Fatalf("result.Error is empty, want the decoding error recorded")
	}
}
