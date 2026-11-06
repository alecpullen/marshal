package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/theme"
)

// stripBold removes the SGR bold open/close sequences that the selection
// style deliberately emits under NO_COLOR (the marker + bold weight carry
// the cursor). Anything else left behind is an escape this test must flag.
func stripBold(s string) string {
	s = strings.ReplaceAll(s, "\x1b[1m", "")
	return strings.ReplaceAll(s, "\x1b[m", "")
}

// listed builds one memory as the session lists it: the panel no longer reads
// a database, so tests hand it entries.
func listed(id int64, kind, content string, created time.Time) acpwire.MemoryEntry {
	return acpwire.MemoryEntry{
		ID:         id,
		Kind:       kind,
		Content:    content,
		Confidence: "high",
		CreatedAt:  acpwire.Millis(created),
	}
}

// recordingDelete returns a delete function that records the ids it was
// asked to delete.
func recordingDelete() (func(id int64) error, *[]int64) {
	ids := make([]int64, 0, 1)
	return func(id int64) error {
		ids = append(ids, id)
		return nil
	}, &ids
}

// newPanel builds a browser over entries whose deletes succeed.
func newPanel(entries ...acpwire.MemoryEntry) *BrowserPanel {
	del, _ := recordingDelete()
	return NewPanel(entries, nil, del)
}

func keyRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func keyCtrlD() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
}

func TestMemoryPanelListsAndPicks(t *testing.T) {
	p := newPanel(listed(1, "fact", "Uses SQLite", time.Unix(100, 0)))
	v := p.View(80, 12)
	if !strings.Contains(v, "Memory") {
		t.Fatalf("panel title missing:\n%s", v)
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an entry must emit ShowMsg")
	}
	shown, ok := cmd().(ShowMsg)
	if !ok {
		t.Fatalf("want ShowMsg, got %T", cmd())
	}
	if shown.Entry.ID != 1 || shown.Entry.Content != "Uses SQLite" {
		t.Fatalf("ShowMsg carried %+v, want the picked entry", shown.Entry)
	}
}

func TestMemoryPanelDeleteNeedsConfirm(t *testing.T) {
	del, deleted := recordingDelete()
	p := NewPanel([]acpwire.MemoryEntry{listed(1, "fact", "Uses SQLite", time.Unix(100, 0))}, nil, del)
	if cmd := p.Update(keyCtrlD()); cmd != nil { // first ctrl+d arms
		t.Fatal("first ctrl+d must only arm the confirm")
	}
	if len(*deleted) != 0 {
		t.Fatalf("armed delete must not call the session: %v", *deleted)
	}
	cmd := p.Update(keyCtrlD()) // second ctrl+d deletes
	if cmd == nil {
		t.Fatal("second ctrl+d must delete and emit DeletedMsg")
	}
	if _, ok := cmd().(DeletedMsg); !ok {
		t.Fatalf("want DeletedMsg, got %T", cmd())
	}
	if len(*deleted) != 1 || (*deleted)[0] != 1 {
		t.Fatalf("deleted ids = %v, want [1]", *deleted)
	}
}

func TestMemoryPanelDeleteReportsSessionError(t *testing.T) {
	boom := fmt.Errorf("session closed")
	del := func(int64) error { return boom }
	p := NewPanel([]acpwire.MemoryEntry{listed(1, "fact", "Uses SQLite", time.Unix(100, 0))}, nil, del)
	p.Update(keyCtrlD())
	cmd := p.Update(keyCtrlD())
	if cmd == nil {
		t.Fatal("second ctrl+d must report the failure")
	}
	msg, ok := cmd().(DeletedMsg)
	if !ok {
		t.Fatalf("want DeletedMsg, got %T", cmd())
	}
	if msg.Err != boom {
		t.Fatalf("DeletedMsg.Err = %v, want the session's error", msg.Err)
	}
}

func TestMemoryPanelDeleteWithoutSessionReportsNoDatabase(t *testing.T) {
	p := NewPanel([]acpwire.MemoryEntry{listed(1, "fact", "Uses SQLite", time.Unix(100, 0))}, nil, nil)
	p.Update(keyCtrlD())
	cmd := p.Update(keyCtrlD())
	if cmd == nil {
		t.Fatal("second ctrl+d must report the failure")
	}
	msg, ok := cmd().(DeletedMsg)
	if !ok {
		t.Fatalf("want DeletedMsg, got %T", cmd())
	}
	if msg.Err == nil || msg.Err.Error() != "no database" {
		t.Fatalf("DeletedMsg.Err = %v, want \"no database\"", msg.Err)
	}
}

func TestMemoryPanelDeleteArmedResetsOnMove(t *testing.T) {
	del, deleted := recordingDelete()
	p := NewPanel([]acpwire.MemoryEntry{
		listed(1, "fact", "first", time.Unix(100, 0)),
		listed(2, "fact", "second", time.Unix(101, 0)),
	}, nil, del)
	p.Update(keyCtrlD()) // arm
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd := p.Update(keyCtrlD()); cmd != nil {
		t.Fatal("ctrl+d after cursor move must re-arm, not delete")
	}
	if len(*deleted) != 0 {
		t.Fatalf("re-armed delete must not call the session: %v", *deleted)
	}
}

func TestMemoryPanelFilterD_DoesNotArmDelete(t *testing.T) {
	p := newPanel(listed(1, "fact", "Uses SQLite", time.Unix(100, 0)))
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
	p := newPanel(
		listed(1, "fact", "Uses SQLite", time.Unix(100, 0)),
		listed(2, "architecture", "TUI built with Bubble Tea", time.Unix(101, 0)),
	)
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
	p := newPanel(
		listed(1, "fact", "Uses SQLite", time.Unix(100, 0)),
		listed(2, "architecture", "TUI built with Bubble Tea", time.Unix(101, 0)),
	)
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
	p := newPanel()
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc must emit a command")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("want ClosedMsg, got %T", cmd())
	}
}

func TestRenderEntryFormatsMemory(t *testing.T) {
	out := RenderEntry(listed(1, "fact", "Uses SQLite\nPersistent store", time.Unix(100, 0)))
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
	p := NewPanel(nil, fmt.Errorf("disk on fire"), nil)
	v := p.View(80, 12)
	if !strings.Contains(v, "load failed") {
		t.Fatalf("footer must report load failure:\n%s", v)
	}
	if strings.Contains(v, "no matches") {
		t.Fatalf("load failure must not be reported as no matches:\n%s", v)
	}
}

func TestMemoryPanelLongTitleKeepsMetadataVisible(t *testing.T) {
	longTitle := strings.Repeat("a", 200)
	p := newPanel(listed(1, "fact", longTitle, time.Unix(100, 0)))
	v := p.View(80, 12)
	if !strings.Contains(v, "fact") {
		t.Fatalf("metadata kind must remain visible:\n%s", v)
	}
	if !strings.Contains(v, "ago") {
		t.Fatalf("metadata age must remain visible:\n%s", v)
	}
}

func TestMemoryPanelRendersWithinTinyHeight(t *testing.T) {
	p := newPanel(listed(1, "fact", "Uses SQLite", time.Unix(100, 0)))
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
	th := theme.LoadWithConfig("warm-sunset", theme.ModeDark, theme.DepthFlat, nil)
	if _, ok := th.FGDefault.(lipgloss.NoColor); !ok {
		t.Fatal("expected monochrome theme with NO_COLOR=1")
	}
	theme.Reload(th)

	p := newPanel(listed(1, "fact", "Uses SQLite", time.Unix(100, 0)))
	v := stripBold(p.View(80, 12))
	if strings.Contains(v, "\x1b[") {
		t.Fatalf("NO_COLOR panel view must not contain ANSI escapes (bold selection allowed):\n%q", v)
	}
}
