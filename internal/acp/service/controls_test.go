package service

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
)

// closedCode returns the acpwire error code err carries, or "" when it is not
// an *acpwire.Error.
func closedCode(err error) acpwire.ErrorCode {
	var werr *acpwire.Error
	if !errors.As(err, &werr) {
		return ""
	}
	return werr.Code
}

func TestWithoutTheSettersTheCallsFail(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	before := state.InteractionState()

	mode := "act"
	if _, err := sess.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode}); !errors.Is(err, errNoInteraction) {
		t.Errorf("SetInteraction error = %v, want %v", err, errNoInteraction)
	}
	if _, err := sess.SetEffort(ctx, "low"); !errors.Is(err, errNoEffort) {
		t.Errorf("SetEffort error = %v, want %v", err, errNoEffort)
	}
	if got := state.InteractionState(); got != before {
		t.Errorf("interaction state = %+v, want it unchanged at %+v", got, before)
	}
}

func TestEffortAppliesValuesAreTheSessions(t *testing.T) {
	if acpwire.EffortAppliesNextStep != session.EffortAppliesNextStep {
		t.Errorf("acpwire.EffortAppliesNextStep = %q, want session's %q",
			acpwire.EffortAppliesNextStep, session.EffortAppliesNextStep)
	}
	if acpwire.EffortAppliesNextTurn != session.EffortAppliesNextTurn {
		t.Errorf("acpwire.EffortAppliesNextTurn = %q, want session's %q",
			acpwire.EffortAppliesNextTurn, session.EffortAppliesNextTurn)
	}
}

func TestAClosedSessionControlsNothing(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	host, old := New(Deps{
		State: state,
		SetInteraction: func(interaction.Patch) (interaction.State, error) {
			return interaction.State{}, nil
		},
		SetEffort: func(string) (string, error) {
			return session.EffortAppliesNextStep, nil
		},
	})
	// The queue and the latch are armed before the swap, so the test can see
	// whether a closed session touched them.
	if err := old.Steer(ctx, "queued"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	state.SetWatchResume("ci", true)
	host.swap(newState(t, "s2"))

	mode := "act"
	_, interactionErr := old.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode})
	_, effortErr := old.SetEffort(ctx, "low")
	errCalls := []struct {
		name string
		err  error
	}{
		{"Steer", old.Steer(ctx, "late")},
		{"ClearSteering", old.ClearSteering(ctx)},
		{"SetInteraction", interactionErr},
		{"SetEffort", effortErr},
		{"SetSudoUnlock", old.SetSudoUnlock(ctx, true)},
		{"ClearFinishedRun", old.ClearFinishedRun(ctx)},
	}
	for _, call := range errCalls {
		if got := closedCode(call.err); got != acpwire.CodeSessionClosed {
			t.Errorf("%s error code = %q, want %q", call.name, got, acpwire.CodeSessionClosed)
		}
	}

	if got, ok := old.PopSteering(ctx); ok {
		t.Errorf("PopSteering() = %q, true, want false", got)
	}
	if old.CancelSubagent(ctx, 1) {
		t.Error("CancelSubagent() = true, want false")
	}
	if name, repeat, ok := old.TakeWatchResume(ctx); ok || name != "" || repeat {
		t.Errorf("TakeWatchResume() = %q, %v, true, want false", name, repeat)
	}

	if got := state.SteeringQueue(); len(got) != 1 || got[0] != "queued" {
		t.Errorf("SteeringQueue() = %v, want the text the closed session left", got)
	}
	if name, repeat, ok := state.TakeWatchResume(); !ok || name != "ci" || !repeat {
		t.Errorf("the closed session took the latch: %q, %v, %v", name, repeat, ok)
	}
}
