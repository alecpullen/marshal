package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

func TestEnterOnNoMatchesShowsFeedback(t *testing.T) {
	m := New("x", "", testItems())
	for _, r := range "zzzzzz" {
		m.Update(key(r))
	}
	if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter with no matches must not emit a command")
	}
	if m.ErrMsg() == "" {
		t.Fatal("enter on no matches should set a feedback message")
	}
	if !strings.Contains(ansi.Strip(m.View(80, 24)), "no matches") {
		t.Fatal("view should say no matches")
	}
}

// TestNoMatchesFeedbackClearsOnArrow verifies the "No matches to select"
// feedback is not sticky: pressing up/down clears it, so it doesn't linger
// after the user starts navigating again.
func TestNoMatchesFeedbackClearsOnArrow(t *testing.T) {
	m := New("x", "", testItems())
	for _, r := range "zzzzzz" {
		m.Update(key(r))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.ErrMsg() == "" {
		t.Fatal("precondition: enter on no matches should set feedback")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.ErrMsg() != "" {
		t.Fatalf("up should clear the feedback, got %q", m.ErrMsg())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.ErrMsg() == "" {
		t.Fatal("precondition: feedback should be set again")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.ErrMsg() != "" {
		t.Fatalf("down should clear the feedback, got %q", m.ErrMsg())
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

func TestAllowCustomPicksFilterValue(t *testing.T) {
	items := []Item{
		{Label: "Ollama", Value: "ollama"},
		{Label: "OpenRouter", Value: "openrouter"},
	}
	m := New("Pick", "", items)
	m.SetAllowCustom(true)

	m.filter.SetValue("custom-model-id")
	m.refilter()

	msg := m.Update(tea.KeyPressMsg{Text: "enter"})
	picked, ok := msg().(PickedMsg)
	if !ok {
		t.Fatalf("expected PickedMsg, got %T", msg())
	}
	if picked.Value != "custom-model-id" {
		t.Fatalf("PickedMsg.Value = %q, want custom-model-id", picked.Value)
	}
}

func TestAllowCustomExactMatchNoCustomItem(t *testing.T) {
	items := []Item{
		{Label: "Ollama", Value: "ollama"},
	}
	m := New("Pick", "", items)
	m.SetAllowCustom(true)

	m.filter.SetValue("ollama")
	m.refilter()

	for _, idx := range m.matches {
		if idx == -1 {
			t.Fatal("exact match should not show custom item")
		}
	}
}

// TestViewHonorsMaxHeight mirrors settings.TestBrowserViewHonorsMaxHeight:
// picker.Model is used directly as a dock.Panel (see model.go's
// openPicker/openSDDPlanPicker), so its View must independently honor the
// dock.Panel contract ("renders at most maxHeight rows") at every maxHeight
// dock.MaxRows can produce (floor 6) as well as smaller values a direct or
// future caller might supply.
func TestViewHonorsMaxHeight(t *testing.T) {
	for _, maxHeight := range []int{0, 1, 2, 3, 4, 6} {
		m := New("Switch model", "footer text", testItems())
		view := m.View(80, maxHeight)
		height := 0
		if view != "" {
			height = lipgloss.Height(view)
		}
		if height > maxHeight {
			t.Errorf("View(80, %d) rendered %d rows", maxHeight, height)
		}
	}
}

// TestPinnedItemStaysFirstWhileFiltering verifies a pinned item is always
// the first match regardless of filter text, so "new X" affordances stay
// discoverable while the user types.
func TestPinnedItemStaysFirstWhileFiltering(t *testing.T) {
	items := []Item{
		{Label: "New custom provider…", Detail: "OpenAI-compatible endpoint", Value: "new-custom", Pinned: true},
		{Label: "Ollama", Value: "ollama"},
		{Label: "OpenAI", Value: "openai"},
	}
	m := New("Pick", "", items)
	if got := m.items[m.matches[0]].Value; got != "new-custom" {
		t.Fatalf("pinned item should be first unfiltered, got %q", got)
	}
	// Filter matches the pinned label: it stays first and is not duplicated.
	for _, r := range "custom" {
		m.Update(key(r))
	}
	count := 0
	for pos, idx := range m.matches {
		if m.items[idx].Value == "new-custom" {
			count++
			if pos != 0 {
				t.Fatalf("pinned item should stay at top while filtering, at pos %d", pos)
			}
		}
	}
	if count != 1 {
		t.Fatalf("pinned item must appear exactly once, got %d", count)
	}
	// Filter that excludes the pinned label: it still stays on top.
	m2 := New("Pick", "", items)
	for _, r := range "openai" {
		m2.Update(key(r))
	}
	if got := m2.items[m2.matches[0]].Value; got != "new-custom" {
		t.Fatalf("pinned item should stay first even when the filter excludes it, got %q", got)
	}
	// Enter picks the pinned row.
	cmd := m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if picked, ok := cmd().(PickedMsg); !ok || picked.Value != "new-custom" {
		t.Fatalf("enter should pick the pinned row, got %#v", cmd())
	}
}

func TestAllowCustomDisabledNoSentinel(t *testing.T) {
	items := []Item{{Label: "Ollama", Value: "ollama"}}
	m := New("Pick", "", items)

	m.filter.SetValue("nonexistent")
	m.refilter()

	for _, idx := range m.matches {
		if idx == -1 {
			t.Fatal("allowCustom=false should never produce sentinel -1")
		}
	}
}

func TestPickerFillsDockWidth(t *testing.T) {
	m := New("Pick", "", testItems())
	out := m.View(120, 20)
	firstLine, _, _ := strings.Cut(out, "\n")
	if got := ansi.StringWidth(firstLine); got < 100 {
		t.Fatalf("picker should fill the dock width, first line is %d cols wide: %q", got, out)
	}
}

func TestPasteIntoFilter(t *testing.T) {
	m := New("Switch model", "", testItems())
	m.Update(tea.PasteMsg{Content: "llama-local"})
	if got := m.filter.Value(); got != "llama-local" {
		t.Fatalf("filter value = %q, want %q", got, "llama-local")
	}
	if len(m.matches) != 1 {
		t.Fatalf("paste should refilter to 1 match, got %d", len(m.matches))
	}
	if m.items[m.matches[0]].Value != "llama-local" {
		t.Fatalf("paste should match llama-local, got %v", m.items[m.matches[0]])
	}
}

// TestSetErrorRendersAndClears verifies that SetError surfaces the message
// in the picker view, and that any subsequent keypress clears it (so the
// error disappears as the user corrects their input). Paste events also
// clear the error.
func TestSetErrorRendersAndClears(t *testing.T) {
	m := New("Pick", "", testItems())
	m.SetError("name cannot be empty")
	if got := m.ErrMsg(); got != "name cannot be empty" {
		t.Fatalf("ErrMsg = %q, want %q", got, "name cannot be empty")
	}
	view := ansi.Strip(m.View(80, 24))
	if !strings.Contains(view, "name cannot be empty") {
		t.Fatalf("view should surface the error:\n%s", view)
	}

	// Any keypress clears the error.
	m.Update(key('a'))
	if got := m.ErrMsg(); got != "" {
		t.Fatalf("typing should clear the error, got %q", got)
	}

	// Paste also clears the error.
	m.SetError("another error")
	m.Update(tea.PasteMsg{Content: "x"})
	if got := m.ErrMsg(); got != "" {
		t.Fatalf("paste should clear the error, got %q", got)
	}

	// SetError("") explicitly clears.
	m.SetError("yet another")
	m.SetError("")
	if got := m.ErrMsg(); got != "" {
		t.Fatalf("SetError(\"\") should clear, got %q", got)
	}
}
