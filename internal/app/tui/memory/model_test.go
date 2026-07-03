package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestCKeyRefreshesUpdatedAtInMemory(t *testing.T) {
	database, projectID := newTestDB(t)
	now := time.Unix(100, 0).UTC()
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", now); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)
	before := m.memories[0].UpdatedAt

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)

	if !m.memories[0].UpdatedAt.After(before) {
		t.Fatalf("in-memory UpdatedAt = %s, want after %s", m.memories[0].UpdatedAt, before)
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

func TestViewKeepsFrameLinesBoundedAndClosed(t *testing.T) {
	database, projectID := newTestDB(t)
	longContent := strings.Repeat("bubble-tea-memory-", 8)
	if err := database.SaveMemory(projectID, "architecture", longContent, "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)
	m.footer = "Update failed: " + strings.Repeat("database-locked ", 8)

	view := m.View()
	assertBoundedFrame(t, view)

	if strings.Contains(view, longContent) {
		t.Fatalf("View() rendered untruncated memory content:\n%s", view)
	}
	if strings.Contains(view, m.footer) {
		t.Fatalf("View() rendered untruncated footer content:\n%s", view)
	}
}

func assertBoundedFrame(t *testing.T, view string) {
	t.Helper()

	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("View() returned no lines")
	}

	expectedWidth := utf8.RuneCountInString(lines[0])
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if got := utf8.RuneCountInString(line); got != expectedWidth {
			t.Fatalf("line width = %d, want %d:\n%q\nfull view:\n%s", got, expectedWidth, line, view)
		}
		first, _ := utf8.DecodeRuneInString(line)
		last, _ := utf8.DecodeLastRuneInString(line)
		switch first {
		case '│':
			if last != '│' {
				t.Fatalf("content line missing right border:\n%q\nfull view:\n%s", line, view)
			}
		case '├':
			if last != '┤' {
				t.Fatalf("separator line missing right border:\n%q\nfull view:\n%s", line, view)
			}
		case '┌':
			if last != '┐' {
				t.Fatalf("top border missing right corner:\n%q\nfull view:\n%s", line, view)
			}
		case '└':
			if last != '┘' {
				t.Fatalf("bottom border missing right corner:\n%q\nfull view:\n%s", line, view)
			}
		}
	}
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return database
}

func createTestProject(t *testing.T, database *db.DB) int64 {
	t.Helper()
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return projectID
}

func TestMemoryViewportScrollsAndClamps(t *testing.T) {
	database := openTestDB(t)
	pid := createTestProject(t, database)
	for i := 0; i < 50; i++ {
		if err := database.SaveMemory(pid, "fact", fmt.Sprintf("memory %d", i), "test-session", time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	m := New(database, pid)
	m.SetSize(80, 24)

	// Move cursor to bottom.
	for i := 0; i < 60; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	if m.cursor != 49 {
		t.Fatalf("cursor = %d, want 49", m.cursor)
	}
	if m.offset <= 0 {
		t.Fatalf("offset did not move: %d", m.offset)
	}

	view := m.View()
	if strings.Contains(view, "memory 0") {
		t.Fatal("first memory should be scrolled out of view")
	}
}
