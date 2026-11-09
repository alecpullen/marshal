// internal/app/tui/notes_seam_test.go — every transcript write in the TUI goes
// through the session's AddNote, and the TUI logs through its own logger
// (plan 6).
package tui

import (
	"log/slog"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestNotesGoThroughTheSession pins that a system note reaches the transcript
// through the session: the stub records one plain note with no role, and the
// state's last message carries the same text.
func TestNotesGoThroughTheSession(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	stub := &stubSession{}
	var notes []acpwire.Note
	stub.onAddNote = func(note acpwire.Note) { notes = append(notes, note) }
	m := testModelStub(t, state, stub)

	// No command registry: /help can only note that it has none.
	m.dispatchCommand("/help")

	const want = "Command registry not available."
	if len(notes) != 1 {
		t.Fatalf("recorded %d notes, want 1: %+v", len(notes), notes)
	}
	note := notes[0]
	if note.Role != "" {
		t.Errorf("note role = %q, want empty", note.Role)
	}
	if note.ContentType != acpwire.ContentTypePlain {
		t.Errorf("note content type = %q, want %q", note.ContentType, acpwire.ContentTypePlain)
	}
	if note.Text != want {
		t.Errorf("note text = %q, want %q", note.Text, want)
	}
	if got := lastMessageContent(state.Messages()); got != want {
		t.Errorf("last message = %q, want %q", got, want)
	}
}

// TestTheNoRunnerEchoIsAUserNote pins decision 1: with no runner the typed
// line is still echoed as the user's message, through a user note, and no turn
// starts.
func TestTheNoRunnerEchoIsAUserNote(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	stub := &stubSession{}
	var notes []acpwire.Note
	stub.onAddNote = func(note acpwire.Note) { notes = append(notes, note) }
	m := testModelStub(t, state, stub)

	updated, _ := m.Update(tea.KeyPressMsg{Text: "hi"})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	if m.busy {
		t.Fatal("model.busy = true, want false when no runner is configured")
	}
	if len(notes) != 1 {
		t.Fatalf("recorded %d notes, want 1: %+v", len(notes), notes)
	}
	note := notes[0]
	if note.Role != acpwire.RoleUser {
		t.Errorf("note role = %q, want %q", note.Role, acpwire.RoleUser)
	}
	if note.Text != "hi" {
		t.Errorf("note text = %q, want %q", note.Text, "hi")
	}
}

// TestWithLoggerSetsTheModelsLogger pins WithLogger and the never-nil log.
func TestWithLoggerSetsTheModelsLogger(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	logger := slog.New(slog.DiscardHandler)

	m := testModel(t, state, WithLogger(logger))
	if m.log() != logger {
		t.Fatal("log() did not return the logger WithLogger set")
	}

	// Without the option the model still logs somewhere, to a discard logger.
	if plain := testModel(t, state); plain.log() == nil {
		t.Fatal("log() = nil for a model with no WithLogger")
	}
}
