package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
)

// newInteractionDispatchModel wires the canonical runtime patch seam over the
// supplied session state, plus the real command registry.
func newInteractionDispatchModel(t *testing.T, state *session.State, opts ...Option) Model {
	t.Helper()
	opts = append([]Option{WithInteractionSetter(func(p interaction.Patch) (interaction.State, error) {
		return state.ApplyInteraction(p)
	})}, opts...)
	return newDispatchModel(t, state, opts...)
}

func TestInteractionCommandTransitionTable(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := newInteractionDispatchModel(t, state)

	dispatch := func(cmd string) {
		t.Helper()
		updated, _ := m.dispatchCommand(cmd)
		m = asModel(t, updated)
	}
	assert := func(label string, mode interaction.Mode, auto, system bool) {
		t.Helper()
		got := state.InteractionState()
		if got.Mode != mode || got.Auto != auto || got.System != system {
			t.Fatalf("%s: state = %+v, want mode=%s auto=%v system=%v", label, got, mode, auto, system)
		}
		if string(m.approvalMode) != string(mode) {
			t.Fatalf("%s: displayed mode = %q, want %q", label, m.approvalMode, mode)
		}
	}

	dispatch("/auto on")
	assert("/auto on", interaction.ModeAct, true, false)

	dispatch("/system on")
	assert("/system on", interaction.ModeAct, true, true)

	dispatch("/ask")
	assert("/ask", interaction.ModeAsk, false, true)

	dispatch("/act")
	assert("/act", interaction.ModeAct, false, true)

	dispatch("/auto")
	assert("/auto toggle", interaction.ModeAct, true, true)

	dispatch("/auto off")
	assert("/auto off", interaction.ModeAct, false, true)

	// /auto on from Ask enters Act in one action.
	dispatch("/ask")
	dispatch("/auto on")
	assert("/auto on from Ask", interaction.ModeAct, true, true)

	dispatch("/mode ask")
	assert("/mode ask", interaction.ModeAsk, false, true)

	dispatch("/mode act")
	assert("/mode act", interaction.ModeAct, false, true)

	dispatch("/system off")
	assert("/system off", interaction.ModeAct, false, false)
}

func TestInteractionCommandFailureDoesNotClaimSuccess(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	before := state.InteractionState()
	m := newDispatchModel(t, state, WithInteractionSetter(func(interaction.Patch) (interaction.State, error) {
		return interaction.State{}, errors.New("runtime refused the patch")
	}))

	updated, _ := m.dispatchCommand("/ask")
	m = asModel(t, updated)

	if got := state.InteractionState(); got != before {
		t.Fatalf("failed command changed access: %+v -> %+v", before, got)
	}
	for _, msg := range state.Messages() {
		if strings.Contains(msg.Content, "Switched to") {
			t.Fatalf("failed command claimed success: %q", msg.Content)
		}
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Could not change interaction state") {
		t.Fatalf("failure receipt = %v, want an explicit failure", msgs)
	}
}

func TestRetiredModeCommandReceiptsDoNotChangeAccess(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	on := true
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("seed Auto: %v", err)
	}
	before := state.InteractionState()
	m := newInteractionDispatchModel(t, state)

	for _, cmd := range []string{"/plan", "/default", "/edit", "/copilot"} {
		updated, _ := m.dispatchCommand(cmd)
		m = asModel(t, updated)
		msgs := state.Messages()
		if len(msgs) == 0 {
			t.Fatalf("%s produced no receipt", cmd)
		}
		last := msgs[len(msgs)-1].Content
		if !strings.Contains(last, "removed") {
			t.Fatalf("%s receipt = %q, want replacement guidance", cmd, last)
		}
		if strings.Contains(last, "Switched to") {
			t.Fatalf("%s receipt claimed a mode change: %q", cmd, last)
		}
	}
	if got := state.InteractionState(); got != before {
		t.Fatalf("retired commands changed access: %+v -> %+v", before, got)
	}
}

func TestAskReceiptReportsAlreadyRunningWork(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := newInteractionDispatchModel(t, state)
	m.busy = true

	updated, _ := m.dispatchCommand("/ask")
	m = asModel(t, updated)

	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Work already running continues") {
		t.Fatalf("Ask receipt = %v, want it to report continued running work", msgs)
	}
}
