package memory

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
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
	if err := database.CreateSession("sess-1", projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return database, projectID
}

func keyRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func keyCtrlD() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
}

func TestMemoryPanelListsAndPicks(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	v := p.View(80, 12)
	if !strings.Contains(v, "Memory") {
		t.Fatalf("panel title missing:\n%s", v)
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an entry must emit ShowMsg")
	}
	if _, ok := cmd().(ShowMsg); !ok {
		t.Fatalf("want ShowMsg, got %T", cmd())
	}
}

func TestMemoryPanelDeleteNeedsConfirm(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	if cmd := p.Update(keyCtrlD()); cmd != nil { // first ctrl+d arms
		t.Fatal("first ctrl+d must only arm the confirm")
	}
	cmd := p.Update(keyCtrlD()) // second ctrl+d deletes
	if cmd == nil {
		t.Fatal("second ctrl+d must delete and emit DeletedMsg")
	}
	if _, ok := cmd().(DeletedMsg); !ok {
		t.Fatalf("want DeletedMsg, got %T", cmd())
	}
}

func TestMemoryPanelDeleteArmedResetsOnMove(t *testing.T) {
	database, projectID := newTestDB(t)
	for _, content := range []string{"first", "second"} {
		if err := database.SaveMemory(projectID, "fact", content, "sess-1", time.Unix(100, 0)); err != nil {
			t.Fatalf("SaveMemory failed: %v", err)
		}
	}
	p := NewPanel(database, projectID)
	p.Update(keyCtrlD()) // arm
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd := p.Update(keyCtrlD()); cmd != nil {
		t.Fatal("ctrl+d after cursor move must re-arm, not delete")
	}
}

func TestMemoryPanelFilterD_DoesNotArmDelete(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	p.Update(keyRune('d'))
	if got := p.filter.Value(); got != "d" {
		t.Fatalf("filter value = %q, want %q", got, "d")
	}
	v := p.View(80, 12)
	if strings.Contains(v, "press ctrl+d again") {
		t.Fatalf("delete must not be armed after typing d:\n%s", v)
	}
}

func TestMemoryPanelFiltersEntries(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	if err := database.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", time.Unix(101, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	p.Update(keyRune('s'))
	p.Update(keyRune('q'))
	p.Update(keyRune('l'))
	v := p.View(80, 12)
	if !strings.Contains(v, "SQLite") {
		t.Fatalf("filtered view missing matching entry:\n%s", v)
	}
	if strings.Contains(v, "Bubble Tea") {
		t.Fatalf("filtered view should not show non-matching entry:\n%s", v)
	}
}

func TestMemoryPanelPasteIntoFilter(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	if err := database.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", time.Unix(101, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	p.Update(tea.PasteMsg{Content: "sqlite"})
	if got := p.filter.Value(); got != "sqlite" {
		t.Fatalf("filter value = %q, want %q", got, "sqlite")
	}
	v := p.View(80, 12)
	if !strings.Contains(v, "SQLite") {
		t.Fatalf("pasted filter should match SQLite entry:\n%s", v)
	}
	if strings.Contains(v, "Bubble Tea") {
		t.Fatalf("pasted filter should exclude non-matching entry:\n%s", v)
	}
}

func TestMemoryPanelEscEmitsClosedMsg(t *testing.T) {
	database, projectID := newTestDB(t)
	p := NewPanel(database, projectID)
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc must emit a command")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("want ClosedMsg, got %T", cmd())
	}
}

func TestRenderEntryFormatsMemory(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite\nPersistent store", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	mems, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	out := RenderEntry(database, mems[0].ID)
	if !strings.Contains(out, "Uses SQLite") {
		t.Fatalf("RenderEntry missing title:\n%s", out)
	}
	if !strings.Contains(out, "fact") {
		t.Fatalf("RenderEntry missing kind metadata:\n%s", out)
	}
	if !strings.Contains(out, "Persistent store") {
		t.Fatalf("RenderEntry missing body:\n%s", out)
	}
}

func TestMemoryPanelLoadErrorShownInFooter(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	p := NewPanel(database, projectID)
	v := p.View(80, 12)
	if !strings.Contains(v, "load failed") {
		t.Fatalf("footer must report load failure:\n%s", v)
	}
	if strings.Contains(v, "no matches") {
		t.Fatalf("load failure must not be reported as no matches:\n%s", v)
	}
}

func TestMemoryPanelLongTitleKeepsMetadataVisible(t *testing.T) {
	database, projectID := newTestDB(t)
	longTitle := strings.Repeat("a", 200)
	if err := database.SaveMemory(projectID, "fact", longTitle, "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	v := p.View(80, 12)
	if !strings.Contains(v, "fact") {
		t.Fatalf("metadata kind must remain visible:\n%s", v)
	}
	if !strings.Contains(v, "ago") {
		t.Fatalf("metadata age must remain visible:\n%s", v)
	}
}

func TestMemoryPanelRendersWithinTinyHeight(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	for _, h := range []int{1, 2, 3} {
		v := p.View(80, h)
		if v == "" {
			t.Fatalf("View(%d) must not return empty", h)
		}
		if lipgloss.Height(v) > h {
			t.Fatalf("View(%d) rendered height %d:\n%s", h, lipgloss.Height(v), v)
		}
		if !strings.Contains(v, "Memory") {
			t.Fatalf("panel title missing at height %d:\n%s", h, v)
		}
	}
}

func TestMemoryPanelNoColorOmitsANSIEscapes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	// Reload the package-wide theme so theme.Current() reports monochrome.
	th := theme.LoadWithConfig("warm-sunset", theme.ModeDark, nil)
	if _, ok := th.FGDefault.(lipgloss.NoColor); !ok {
		t.Fatal("expected monochrome theme with NO_COLOR=1")
	}
	theme.Reload(th)

	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	p := NewPanel(database, projectID)
	v := p.View(80, 12)
	if strings.Contains(v, "\x1b[") {
		t.Fatalf("NO_COLOR panel view must not contain ANSI escapes:\n%q", v)
	}
}
