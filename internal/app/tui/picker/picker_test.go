package picker

import (
	"fmt"
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

// cursorValue reports the value under the cursor, "<custom>" for the synthetic
// custom row, and "" when nothing is selected.
func cursorValue(m *Model) string {
	if m.cursor < 0 || m.cursor >= len(m.matches) {
		return ""
	}
	idx := m.matches[m.cursor]
	if idx == -1 {
		return "<custom>"
	}
	return m.items[idx].Value
}

// moveTo points the cursor at the first match carrying want.
func moveTo(t *testing.T, m *Model, want string) {
	t.Helper()
	for pos, idx := range m.matches {
		if idx == -1 {
			if want == "<custom>" {
				m.cursor = pos
				return
			}
			continue
		}
		if m.items[idx].Value == want {
			m.cursor = pos
			return
		}
	}
	t.Fatalf("no match with value %q, matches %v", want, m.matches)
}

// TestReplaceItemsPreservesSelectionAndFilterCursor pins the refresh contract:
// rows may be replaced, reordered, or re-labelled without moving the user's
// choice, discarding the filter text, or moving the text cursor.
func TestReplaceItemsPreservesSelectionAndFilterCursor(t *testing.T) {
	t.Run("reordered rows", func(t *testing.T) {
		m := New("Models", "", []Item{
			{Label: "alpha", Value: "a"},
			{Label: "beta", Value: "b"},
			{Label: "gamma", Value: "g"},
		})
		moveTo(t, m, "b")
		if !m.ReplaceItems([]Item{
			{Label: "gamma", Value: "g"},
			{Label: "alpha", Value: "a"},
			{Label: "beta", Value: "b"},
		}) {
			t.Fatal("ReplaceItems should keep the selection")
		}
		if got := cursorValue(m); got != "b" {
			t.Fatalf("selected value = %q, want b", got)
		}
		if got := m.ErrMsg(); got != "" {
			t.Fatalf("errMsg = %q, want empty", got)
		}
	})

	t.Run("identical labels distinct values", func(t *testing.T) {
		m := New("Models", "", []Item{
			{Label: "sonnet", Detail: "anthropic", Value: "anthropic/sonnet"},
			{Label: "sonnet", Detail: "openai", Value: "openai/sonnet"},
		})
		moveTo(t, m, "openai/sonnet")
		if !m.ReplaceItems([]Item{
			{Label: "sonnet", Detail: "openai", Value: "openai/sonnet"},
			{Label: "sonnet", Detail: "anthropic", Value: "anthropic/sonnet"},
		}) {
			t.Fatal("ReplaceItems should keep the selection")
		}
		if got := cursorValue(m); got != "openai/sonnet" {
			t.Fatalf("selected value = %q, want openai/sonnet", got)
		}
	})

	t.Run("custom input", func(t *testing.T) {
		m := New("Models", "", []Item{{Label: "known", Value: "known"}})
		m.SetAllowCustom(true)
		m.SetFilter("my-own-model")
		if got := cursorValue(m); got != "<custom>" {
			t.Fatalf("precondition: selected row = %q, want the custom row", got)
		}
		if !m.ReplaceItems([]Item{{Label: "other", Value: "other"}}) {
			t.Fatal("ReplaceItems should keep the custom selection")
		}
		if got := cursorValue(m); got != "<custom>" {
			t.Fatalf("selected row = %q, want the custom row", got)
		}
		if got := m.FilterValue(); got != "my-own-model" {
			t.Fatalf("filter = %q, want my-own-model", got)
		}
		cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter on the kept custom row should emit a command")
		}
		if picked, ok := cmd().(PickedMsg); !ok || picked.Value != "my-own-model" {
			t.Fatalf("want PickedMsg{my-own-model}, got %#v", cmd())
		}
	})

	t.Run("middle text cursor", func(t *testing.T) {
		m := New("Models", "", []Item{
			{Label: "alpha", Value: "a"},
			{Label: "alphabet", Value: "ab"},
		})
		m.SetFilter("alp")
		m.filter.SetCursor(2)
		before := m.filter.Position()
		m.ReplaceItems([]Item{
			{Label: "alphabet", Value: "ab"},
			{Label: "alpha", Value: "a"},
		})
		if got := m.FilterValue(); got != "alp" {
			t.Fatalf("filter = %q, want alp", got)
		}
		if got := m.filter.Position(); got != before {
			t.Fatalf("text cursor moved to %d, want %d", got, before)
		}
	})

	t.Run("items are copied", func(t *testing.T) {
		m := New("Models", "", []Item{{Label: "alpha", Value: "a"}})
		next := []Item{{Label: "beta", Value: "b"}}
		m.ReplaceItems(next)
		next[0].Label = "mutated"
		if got := m.Items(); len(got) != 1 || got[0].Label != "beta" {
			t.Fatalf("items = %#v, want the beta row untouched", got)
		}
	})
}

// TestReplaceItemsRequiresExplicitChoiceWhenMissing pins the other half of the
// contract: when the selected row disappears the picker shows no selection and
// waits for the user, rather than sliding onto whichever row took its place.
func TestReplaceItemsRequiresExplicitChoiceWhenMissing(t *testing.T) {
	items := []Item{
		{Label: "alpha", Value: "a"},
		{Label: "beta", Value: "b"},
	}

	t.Run("disappearance", func(t *testing.T) {
		m := New("Models", "", items)
		moveTo(t, m, "b")
		if m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}}) {
			t.Fatal("ReplaceItems should report the vanished selection")
		}
		if m.cursor != -1 {
			t.Fatalf("cursor = %d, want -1 (nothing selected)", m.cursor)
		}
		if got := m.ErrMsg(); got != errMissingSelection {
			t.Fatalf("errMsg = %q, want %q", got, errMissingSelection)
		}
		view := m.View(80, 24)
		if !strings.Contains(ansi.Strip(view), errMissingSelection) {
			t.Fatalf("view should show the missing-selection error:\n%s", ansi.Strip(view))
		}
		if strings.Contains(ansi.Strip(view), "▸") {
			t.Fatalf("no row may be marked selected:\n%s", ansi.Strip(view))
		}
		if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
			t.Fatalf("enter must not pick without a selection, got %#v", cmd())
		}
	})

	t.Run("repeated refresh", func(t *testing.T) {
		m := New("Models", "", items)
		moveTo(t, m, "b")
		m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}})
		// The old choice comes back, but the user has not acted yet: a second
		// refresh must still not select anything.
		if m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}, {Label: "beta", Value: "b"}}) {
			t.Fatal("a second refresh must not select a row")
		}
		if m.cursor != -1 {
			t.Fatalf("cursor = %d, want -1", m.cursor)
		}
		if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
			t.Fatalf("enter must still not pick, got %#v", cmd())
		}

		// Down recovers the first match and the error goes away.
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if m.cursor != 0 {
			t.Fatalf("down should select the first match, cursor = %d", m.cursor)
		}
		if got := m.ErrMsg(); got != "" {
			t.Fatalf("navigation should clear the error, got %q", got)
		}
		cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter after navigating should emit a command")
		}
		if picked, ok := cmd().(PickedMsg); !ok || picked.Value != "a" {
			t.Fatalf("want PickedMsg{a}, got %#v", cmd())
		}
	})

	t.Run("up selects the first match", func(t *testing.T) {
		m := New("Models", "", items)
		moveTo(t, m, "b")
		m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}})
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		if m.cursor != 0 {
			t.Fatalf("up should select the first match, cursor = %d", m.cursor)
		}
		if got := m.ErrMsg(); got != "" {
			t.Fatalf("up should clear the error, got %q", got)
		}
	})

	t.Run("typing and paste select the first match", func(t *testing.T) {
		typed := New("Models", "", items)
		moveTo(t, typed, "b")
		typed.ReplaceItems([]Item{{Label: "alpha", Value: "a"}})
		typed.Update(key('a'))
		if typed.cursor != 0 {
			t.Fatalf("typing should select the first match, cursor = %d", typed.cursor)
		}
		if got := typed.ErrMsg(); got != "" {
			t.Fatalf("typing should clear the error, got %q", got)
		}
		if cmd := typed.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
			t.Fatal("enter after typing should emit a command")
		}

		pasted := New("Models", "", items)
		moveTo(t, pasted, "b")
		pasted.ReplaceItems([]Item{{Label: "alpha", Value: "a"}})
		pasted.Update(tea.PasteMsg{Content: "a"})
		if pasted.cursor != 0 {
			t.Fatalf("paste should select the first match, cursor = %d", pasted.cursor)
		}
		if got := pasted.ErrMsg(); got != "" {
			t.Fatalf("paste should clear the error, got %q", got)
		}
	})

	t.Run("empty picker selects the first arrival", func(t *testing.T) {
		m := New("Models", "", nil)
		if len(m.matches) != 0 {
			t.Fatalf("precondition: matches = %v, want none", m.matches)
		}
		if !m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}, {Label: "beta", Value: "b"}}) {
			t.Fatal("the first arrival may select a row")
		}
		if m.cursor != 0 || cursorValue(m) != "a" {
			t.Fatalf("selected = %q at %d, want a at 0", cursorValue(m), m.cursor)
		}
		if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
			t.Fatal("enter on the first arrival should emit a command")
		}
	})

	t.Run("custom row is not swapped for a real row", func(t *testing.T) {
		m := New("Models", "", []Item{{Label: "known", Value: "known"}})
		m.SetAllowCustom(true)
		m.SetFilter("typed-model")
		if got := cursorValue(m); got != "<custom>" {
			t.Fatalf("precondition: selected row = %q, want the custom row", got)
		}
		// A real row carrying the typed value arrives, which suppresses the
		// synthetic custom row: the former selection is gone, not re-bound.
		if m.ReplaceItems([]Item{{Label: "typed-model", Value: "typed-model"}}) {
			t.Fatal("ReplaceItems must not swap the custom row for a real one")
		}
		if m.cursor != -1 {
			t.Fatalf("cursor = %d, want -1", m.cursor)
		}
		if got := m.ErrMsg(); got != errMissingSelection {
			t.Fatalf("errMsg = %q, want %q", got, errMissingSelection)
		}
		if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
			t.Fatalf("enter must not pick a substitute row, got %#v", cmd())
		}
	})

	t.Run("real row is not swapped for the custom row", func(t *testing.T) {
		m := New("Models", "", []Item{{Label: "typed-model", Value: "typed-model"}})
		m.SetAllowCustom(true)
		m.SetFilter("typed-model")
		for _, idx := range m.matches {
			if idx == -1 {
				t.Fatal("precondition: an exact match must suppress the custom row")
			}
		}
		m.cursor = 0
		// The real row disappears while the typed value now renders only as
		// the synthetic custom row: that is a different choice, not the old one.
		if m.ReplaceItems([]Item{{Label: "other", Value: "other"}}) {
			t.Fatal("ReplaceItems must not turn a real selection into the custom row")
		}
		if m.cursor != -1 {
			t.Fatalf("cursor = %d, want -1", m.cursor)
		}
	})

	t.Run("error stays inside the dock height", func(t *testing.T) {
		var many []Item
		for i := 0; i < 30; i++ {
			label := fmt.Sprintf("model-%02d", i)
			many = append(many, Item{Label: label, Value: label})
		}
		m := New("Models", "↵ pick · Esc cancel", many)
		moveTo(t, m, "model-29")
		m.ReplaceItems(many[:29])
		view := m.View(80, 24)
		if h := lipgloss.Height(view); h > 24 {
			t.Fatalf("view is %d rows tall, want at most 24", h)
		}
		if !strings.Contains(ansi.Strip(view), errMissingSelection) {
			t.Fatalf("the error must stay visible at 80x24:\n%s", ansi.Strip(view))
		}
	})

	t.Run("error survives the smallest dock", func(t *testing.T) {
		// A docked panel can be as short as dock.MaxRows' floor. With a footer
		// and an error the list must yield rows — and the footer is dropped
		// last — so the error never falls out of the frame where the user
		// could not read the reason Enter refused.
		m := New("Models", "↵ pick · Esc cancel", items)
		moveTo(t, m, "b")
		m.ReplaceItems([]Item{{Label: "alpha", Value: "a"}})
		for _, maxH := range []int{6, 5, 4, 3} {
			view := m.View(80, maxH)
			if h := lipgloss.Height(view); h > maxH {
				t.Errorf("View(80, %d) rendered %d rows", maxH, h)
			}
			if !strings.Contains(ansi.Strip(view), errMissingSelection) {
				t.Errorf("View(80, %d) must keep the error visible:\n%s", maxH, ansi.Strip(view))
			}
		}
	})
}

// TestReplaceItemsUnderActiveFilter pins what a refresh may do while the user
// is filtering. A row the filter hides can never be the selected row — the
// cursor is positional, so refilter already re-bound it — but the selected
// value leaving the catalog is still a disappearance, and it must not slide
// onto a row that happens to match.
func TestReplaceItemsUnderActiveFilter(t *testing.T) {
	items := []Item{
		{Label: "alpha", Value: "a"},
		{Label: "beta", Value: "b"},
		{Label: "gamma", Value: "c"},
	}
	m := New("Models", "", items)
	for _, r := range "gamm" {
		m.Update(key(r))
	}
	if got := cursorValue(m); got != "c" {
		t.Fatalf("precondition: selected = %q, want gamma", got)
	}
	if len(m.matches) != 1 {
		t.Fatalf("precondition: filter should leave one match, got %d", len(m.matches))
	}

	// The selected value leaves the catalog while the filter is still active.
	if m.ReplaceItems(items[:2]) {
		t.Fatal("a selection the refresh dropped must count as disappeared")
	}
	if m.cursor != -1 {
		t.Fatalf("cursor = %d, want -1", m.cursor)
	}
	if got := m.ErrMsg(); got != errMissingSelection {
		t.Fatalf("errMsg = %q, want %q", got, errMissingSelection)
	}
	if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter must not pick while nothing is selected, got %#v", cmd())
	}

	// A second refresh under the same filter still waits for the user.
	if m.ReplaceItems(items[:2]) {
		t.Fatal("a later refresh must still not select a row for the user")
	}
	if m.cursor != -1 {
		t.Fatalf("cursor = %d after a second refresh, want -1", m.cursor)
	}
}

// TestReplaceItemsKeepsFirstOfDuplicateValues pins the tie-break when a list
// carries the same opaque value twice: selection re-binds to the first match,
// so a refresh cannot shuffle which row stays highlighted.
func TestReplaceItemsKeepsFirstOfDuplicateValues(t *testing.T) {
	items := []Item{
		{Label: "same (a)", Value: "dup"},
		{Label: "other", Value: "other"},
		{Label: "same (b)", Value: "dup"},
	}
	m := New("Models", "", items)
	moveTo(t, m, "dup")
	if m.cursor != 0 {
		t.Fatalf("precondition: cursor = %d, want the first duplicate", m.cursor)
	}
	if !m.ReplaceItems(items) {
		t.Fatal("the value is still present, so the selection survives")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want the first row carrying the value", m.cursor)
	}
	if got := m.items[m.matches[m.cursor]].Label; got != "same (a)" {
		t.Fatalf("re-bound to %q, want the first duplicate", got)
	}
}
