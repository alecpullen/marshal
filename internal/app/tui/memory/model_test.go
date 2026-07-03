package memory

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/db"
)

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}

func TestNewLoadsMemories(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}

	m := New(database, projectID)

	if len(m.memories) != 1 || m.memories[0].Content != "Uses SQLite" {
		t.Fatalf("memories = %#v, want one loaded memory", m.memories)
	}
}

func TestEscReturnsClosedMsg(t *testing.T) {
	database, projectID := newTestDB(t)
	m := New(database, projectID)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("expected ClosedMsg, got %T", cmd())
	}
}

func TestCursorMovesWithinBounds(t *testing.T) {
	database, projectID := newTestDB(t)
	for i, content := range []string{"first", "second"} {
		if err := database.SaveMemory(projectID, "fact", content, "sess-1", time.Unix(int64(100+i), 0)); err != nil {
			t.Fatalf("SaveMemory failed: %v", err)
		}
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (clamped at top)", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (clamped at bottom)", m.cursor)
	}
}

func TestSKeyMarksSelectedMemoryStale(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)

	if m.memories[0].Confidence != db.MemoryConfidenceStale {
		t.Fatalf("in-memory confidence = %q, want stale", m.memories[0].Confidence)
	}

	stored, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if stored[0].Confidence != db.MemoryConfidenceStale {
		t.Fatalf("stored confidence = %q, want stale", stored[0].Confidence)
	}
}

func TestCKeyMarksSelectedMemoryConfirmed(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)

	if m.memories[0].Confidence != db.MemoryConfidenceConfirmed {
		t.Fatalf("in-memory confidence = %q, want confirmed", m.memories[0].Confidence)
	}
}

func TestViewShowsMemoriesAndEmptyState(t *testing.T) {
	database, projectID := newTestDB(t)
	m := New(database, projectID)

	view := m.View()
	if !strings.Contains(view, "No memories yet.") {
		t.Fatalf("View() missing empty-state message:\n%s", view)
	}

	if err := database.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m = New(database, projectID)
	view = m.View()
	if !strings.Contains(view, "TUI built with Bubble Tea") {
		t.Fatalf("View() missing memory content:\n%s", view)
	}
}
