package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var ctrlS = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}

// TestCtrlSIsSuppressedWhenMouseCaptureIsDisabled pins AUD-14: with
// [tui].mouse_capture = false the terminal owns the mouse and View keeps
// MouseModeNone, so Ctrl+S must not announce a capture it did not perform. It
// says where the setting lives instead.
func TestCtrlSIsSuppressedWhenMouseCaptureIsDisabled(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).Config.TUI.MouseCapture = false

	updated, _, handled := m.handleKeypress(ctrlS)
	if !handled {
		t.Fatal("Ctrl+S was not handled")
	}
	m = updated.(Model)

	if m.mouseReleased {
		t.Fatal("Ctrl+S flipped the session override while capture is off in config")
	}
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("MouseMode = %v, want MouseModeNone", got)
	}
	msgs := stateOf(m).Messages()
	if n := countContaining(msgs, "[tui].mouse_capture"); n != 1 {
		t.Fatalf("got %d notes naming the config key, want exactly one", n)
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "Mouse captured") || strings.Contains(msg.Content, "Mouse released") {
			t.Fatalf("capture is off in config, but the note claims a handover: %q", msg.Content)
		}
	}

	// A second press changes nothing either: there is nothing to toggle.
	updated, _, _ = m.handleKeypress(ctrlS)
	m = updated.(Model)
	if m.mouseReleased {
		t.Fatal("a second Ctrl+S flipped the override while capture is off in config")
	}
}

// TestCtrlSToggleStillWorksWithMouseCaptureEnabled pins that the honest path
// did not narrow the working one: with capture on (the default), Ctrl+S still
// releases the mouse to the terminal and takes it back.
func TestCtrlSToggleStillWorksWithMouseCaptureEnabled(t *testing.T) {
	m := newTestModel(t)
	if !m.sess.Config().TUI.MouseCapture {
		t.Fatal("this test needs capture enabled by default")
	}

	updated, _, handled := m.handleKeypress(ctrlS)
	if !handled {
		t.Fatal("Ctrl+S was not handled")
	}
	m = updated.(Model)
	if !m.mouseReleased {
		t.Fatal("Ctrl+S did not release the mouse")
	}
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("after Ctrl+S MouseMode = %v, want MouseModeNone", got)
	}
	if n := countContaining(stateOf(m).Messages(), "Mouse released to the terminal"); n != 1 {
		t.Fatalf("got %d release notes, want exactly one", n)
	}

	updated, _, _ = m.handleKeypress(ctrlS)
	m = updated.(Model)
	if m.mouseReleased {
		t.Fatal("a second Ctrl+S did not reclaim the mouse")
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("after the second Ctrl+S MouseMode = %v, want MouseModeCellMotion", got)
	}
}
