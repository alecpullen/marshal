package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testItems() []Item {
	return []Item{
		{Group: "anthropic", Label: "sonnet", Detail: "anthropic/sonnet-5", Value: "sonnet"},
		{Group: "ollama", Label: "llama-local", Detail: "ollama/llama3.3", Badge: "local", Value: "llama-local"},
		{Group: "ollama", Label: "qwen-coder", Detail: "ollama/qwen2.5", Badge: "● now local", Value: "qwen-coder"},
	}
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestNewStartsOnCurrentBadge(t *testing.T) {
	m := New("Switch model", "", testItems())
	if got := m.items[m.matches[m.cursor]].Value; got != "qwen-coder" {
		t.Fatalf("cursor should start on the ● item, got %q", got)
	}
}

func TestFilterNarrowsAndEnterPicks(t *testing.T) {
	m := New("Switch model", "", testItems())
	for _, r := range "llama-loc" {
		m.Update(key(r))
	}
	if len(m.matches) != 1 {
		t.Fatalf("filter should narrow to 1, got %d", len(m.matches))
	}
	cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit a command")
	}
	picked, ok := cmd().(PickedMsg)
	if !ok || picked.Value != "llama-local" {
		t.Fatalf("want PickedMsg{llama-local}, got %#v", cmd())
	}
}

func TestEscCancels(t *testing.T) {
	m := New("x", "", testItems())
	cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc should emit a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatalf("want CancelledMsg, got %#v", cmd())
	}
}

func TestEnterOnNoMatchesIsNoop(t *testing.T) {
	m := New("x", "", testItems())
	for _, r := range "zzzzzz" {
		m.Update(key(r))
	}
	if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter with no matches must be a no-op")
	}
	if !strings.Contains(ansi.Strip(m.View(80, 24)), "no matches") {
		t.Fatal("view should say no matches")
	}
}

func TestViewGroupsWhenUnfilteredFlatWhenFiltered(t *testing.T) {
	m := New("Switch model", "session only", testItems())
	v := ansi.Strip(m.View(80, 24))
	if !strings.Contains(v, "ollama") || !strings.Contains(v, "anthropic") {
		t.Fatalf("unfiltered view should show group headers:\n%s", v)
	}
	if !strings.Contains(v, "session only") {
		t.Fatalf("footer text missing:\n%s", v)
	}
	m.Update(key('q'))
	v = ansi.Strip(m.View(80, 24))
	if strings.Contains(v, "anthropic\n") {
		t.Fatalf("filtered view should be flat (no headers):\n%s", v)
	}
	if !strings.Contains(v, "qwen-coder") {
		t.Fatalf("filtered view should keep matches:\n%s", v)
	}
}

func TestArrowsMoveCursorAndSkipHeaders(t *testing.T) {
	m := New("x", "", testItems())
	m.cursor = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cursor != 1 {
		t.Fatalf("down should move to next item, got %d", m.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cursor != 0 {
		t.Fatalf("up should clamp at 0, got %d", m.cursor)
	}
}
