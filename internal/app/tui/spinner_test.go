package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestSpinnerFramesWrap(t *testing.T) {
	s := NewSpinner()
	first := s.Next()
	for i := 0; i < 9; i++ {
		s.Next()
	}
	second := s.Next()
	if first != second {
		t.Fatalf("after 10 calls (len(frames)), Next() = %q, want %q (full wrap)", second, first)
	}
}

func TestSpinnerFramesAreNotEmpty(t *testing.T) {
	s := NewSpinner()
	for i := 0; i < 20; i++ {
		f := s.Next()
		if f == "" {
			t.Fatalf("frame %d is empty", i)
		}
	}
}

func TestSpinnerHiddenBefore200ms(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	now := time.Unix(100, 0) // anchors time at 0
	m := New(state)
	m.now = func() time.Time { return now }
	m.spinnerFrame = "⠋"
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking, StartedAt: now})

	// With StartedAt == now (0ms elapsed), the gate should hide the spinner.
	frame := m.activeSpinnerFrame(session.ActivityThinking)
	if frame != "" {
		t.Fatalf("activeSpinnerFrame = %q, want \"\" (hidden before 200ms)", frame)
	}
}

func TestSpinnerShownAfter200ms(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	now := time.Unix(100, 0) // anchors time at 0
	m := New(state)
	m.now = func() time.Time { return now }
	m.spinnerFrame = "⠋"
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking, StartedAt: now.Add(-300 * time.Millisecond)})

	// With 300ms elapsed (>= 200ms), the gate should show the spinner.
	frame := m.activeSpinnerFrame(session.ActivityThinking)
	if frame != "⠋" {
		t.Fatalf("activeSpinnerFrame = %q, want \"⠋\" (shown after 200ms)", frame)
	}
}

func TestSpinnerAlwaysHiddenForIdle(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.spinnerFrame = "⠋"
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle, StartedAt: time.Unix(0, 0)})

	frame := m.activeSpinnerFrame(session.ActivityIdle)
	if frame != "" {
		t.Fatalf("activeSpinnerFrame for idle = %q, want \"\"", frame)
	}
}

func TestSpinnerTickCmdArmsCorrectly(t *testing.T) {
	// Verify spinnerTickCmd produces a spinnerTickMsg and re-arms.
	cmd := spinnerTickCmd()
	if cmd == nil {
		t.Fatal("spinnerTickCmd() returned nil")
	}

	// Execute the command (synchronous tea.Tick in tests fires immediately
	// when tick duration is zero — but 80ms will time out. Instead verify
	// the command is a tea.Tick by checking the message type.
	msg := cmd()
	if _, ok := msg.(spinnerTickMsg); !ok {
		t.Fatalf("spinnerTickCmd() produced %T, want spinnerTickMsg", msg)
	}
}

func TestSpinnerTickMsgAdvancesFrameWhenBusy(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = true
	m.spinnerFrame = ""

	updated, cmd := m.Update(spinnerTickMsg{})
	m2 := updated.(Model)

	if m2.spinnerFrame == "" {
		t.Fatal("spinnerFrame should be non-empty after spinnerTickMsg when busy")
	}
	if cmd == nil {
		t.Fatal("spinnerTickMsg should re-arm when busy")
	}
	// The re-armed command should produce another spinnerTickMsg.
	if msg := cmd(); msg != nil {
		if _, ok := msg.(spinnerTickMsg); !ok {
			t.Fatalf("re-armed command produced %T, want spinnerTickMsg", msg)
		}
	}
}

func TestSpinnerTickMsgDoesNotAdvanceOrReArmWhenNotBusy(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = false
	m.spinnerFrame = ""

	updated, cmd := m.Update(spinnerTickMsg{})
	m2 := updated.(Model)

	if m2.spinnerFrame != "" {
		t.Fatal("spinnerFrame should remain empty when not busy")
	}
	if cmd != nil {
		t.Fatal("spinnerTickMsg should not re-arm when not busy")
	}
}

func TestAgentTickNoLongerAdvancesSpinner(t *testing.T) {
	// Verify that the agentTickMsg handler no longer calls m.spinner.Next().
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = true
	m.spinnerFrame = ""

	updated, _ := m.Update(agentTickMsg{})
	m2 := updated.(Model)

	// spinnerFrame should remain empty because agentTickMsg no longer advances it.
	if m2.spinnerFrame != "" {
		t.Fatal("agentTickMsg should no longer advance spinnerFrame")
	}
}
