package inproc

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

func TestSteerQueuesAndPopTakesTheOldest(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if err := sess.Steer(ctx, "a"); err != nil {
		t.Fatalf("Steer(a): %v", err)
	}
	if err := sess.Steer(ctx, "b"); err != nil {
		t.Fatalf("Steer(b): %v", err)
	}
	queue := state.SteeringQueue()
	if len(queue) != 2 || queue[0] != "a" || queue[1] != "b" {
		t.Fatalf("SteeringQueue() = %v, want [a b]", queue)
	}

	for _, want := range []string{"a", "b"} {
		got, ok := sess.PopSteering(ctx)
		if !ok {
			t.Fatalf("PopSteering() = false, want %q", want)
		}
		if got != want {
			t.Fatalf("PopSteering() = %q, want %q", got, want)
		}
	}
	if got, ok := sess.PopSteering(ctx); ok {
		t.Fatalf("PopSteering() on an empty queue = %q, true, want false", got)
	}
}

func TestClearSteeringEmptiesTheQueue(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if err := sess.Steer(ctx, "a"); err != nil {
		t.Fatalf("Steer(a): %v", err)
	}
	if err := sess.Steer(ctx, "b"); err != nil {
		t.Fatalf("Steer(b): %v", err)
	}
	if err := sess.ClearSteering(ctx); err != nil {
		t.Fatalf("ClearSteering: %v", err)
	}
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("SteeringQueue() = %v, want empty", got)
	}
}

func TestSetInteractionPassesThePatch(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	var recorded interaction.Patch
	_, sess := New(Deps{
		State: state,
		SetInteraction: func(p interaction.Patch) (interaction.State, error) {
			recorded = p
			return interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 5}, nil
		},
	})

	mode := "act"
	system := true
	got, err := sess.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode, System: &system})
	if err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}

	if recorded.Mode == nil || *recorded.Mode != interaction.ModeAct {
		t.Errorf("recorded mode = %v, want act", recorded.Mode)
	}
	if recorded.System == nil || !*recorded.System {
		t.Errorf("recorded system = %v, want true", recorded.System)
	}
	if recorded.Auto != nil {
		t.Errorf("recorded auto = %v, want nil", *recorded.Auto)
	}

	want := acpwire.Interaction{Mode: "act", Auto: true, System: true, Revision: 5}
	if got != want {
		t.Errorf("SetInteraction() = %+v, want %+v", got, want)
	}
}

func TestSetInteractionReturnsTheSettersError(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	refused := errors.New("interaction refused")
	_, sess := New(Deps{
		State: state,
		SetInteraction: func(interaction.Patch) (interaction.State, error) {
			return interaction.State{}, refused
		},
	})

	mode := "act"
	_, err := sess.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode})
	if !errors.Is(err, refused) {
		t.Fatalf("SetInteraction error = %v, want %v", err, refused)
	}
}

func TestSetEffortReportsWhenItApplies(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	var recorded string
	_, sess := New(Deps{
		State: state,
		SetEffort: func(level string) (string, error) {
			recorded = level
			return session.EffortAppliesNextStep, nil
		},
	})

	applies, err := sess.SetEffort(ctx, "low")
	if err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if recorded != "low" {
		t.Errorf("recorded level = %q, want low", recorded)
	}
	if applies != acpwire.EffortAppliesNextStep {
		t.Errorf("SetEffort() = %q, want %q", applies, acpwire.EffortAppliesNextStep)
	}
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

func TestSetSudoUnlockReachesStatus(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if err := sess.SetSudoUnlock(ctx, true); err != nil {
		t.Fatalf("SetSudoUnlock(true): %v", err)
	}
	if !sess.Status().SudoUnlock {
		t.Error("Status().SudoUnlock = false, want true")
	}
	if err := sess.SetSudoUnlock(ctx, false); err != nil {
		t.Fatalf("SetSudoUnlock(false): %v", err)
	}
	if sess.Status().SudoUnlock {
		t.Error("Status().SudoUnlock = true, want false")
	}
}

func TestCancelSubagentStopsOnlyARunningChild(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	child := newState(t, "child")
	view := state.RegisterSubagent("task 1", child)
	ran := false
	state.SetSubagentCancel(view.ID, func() { ran = true })

	_, sess := New(Deps{State: state})
	if !sess.CancelSubagent(ctx, view.ID) {
		t.Fatal("CancelSubagent reported false for a running child with a cancel function")
	}
	if !ran {
		t.Error("cancel function did not run")
	}
	if sess.CancelSubagent(ctx, 999) {
		t.Error("CancelSubagent reported true for an unknown ID")
	}
}

func TestClearFinishedRunClearsProgressAndEvents(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	state.SetSDDProgress(session.SDDProgress{PlanName: "p"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventTaskDone})

	_, sess := New(Deps{State: state})
	if err := sess.ClearFinishedRun(ctx); err != nil {
		t.Fatalf("ClearFinishedRun: %v", err)
	}
	if got := state.SDDProgress().PlanName; got != "" {
		t.Errorf("SDDProgress().PlanName = %q, want empty", got)
	}
	if got := state.RunEvents(); len(got) != 0 {
		t.Errorf("RunEvents() = %v, want empty", got)
	}
}

func TestTakeWatchResumeTakesTheLatchOnce(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	state.SetWatchResume("ci", true)

	_, sess := New(Deps{State: state})
	name, repeat, ok := sess.TakeWatchResume(ctx)
	if !ok || name != "ci" || !repeat {
		t.Fatalf("TakeWatchResume() = %q, %v, %v, want ci, true, true", name, repeat, ok)
	}
	if _, _, ok := sess.TakeWatchResume(ctx); ok {
		t.Error("TakeWatchResume() took the latch twice")
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
	host.Swap(newState(t, "s2"))

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
