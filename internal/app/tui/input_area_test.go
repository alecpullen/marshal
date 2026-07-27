package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func TestInputChromeRowsExcludesTextarea(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	if got := m.inputChromeRows(); got != 0 {
		t.Fatalf("inputChromeRows() = %d, want 0 with no chrome visible", got)
	}
	if got, want := m.inputAreaRows(), max(m.input.Height(), 1); got != want {
		t.Fatalf("inputAreaRows() = %d, want %d (chrome 0 + input height)", got, want)
	}
}

func TestDynamicMaxHeightBudget(t *testing.T) {
	m := newViewTestModel(t, 80, 40)
	// 40 - transcriptFrameRows(0) - statusLineRows(1) - minTranscriptRows(3);
	// no todo panel, live strip, dock, or popup.
	if got, want := m.input.MaxHeight, 36; got != want {
		t.Fatalf("input.MaxHeight = %d, want %d", got, want)
	}
}

func TestInputGrowsBeyondEightRows(t *testing.T) {
	m := newViewTestModel(t, 80, 40)
	m.input, _ = m.input.Update(tea.PasteMsg{Content: strings.Repeat("a line of text\n", 11)}) // 12 lines
	m.updateViewportHeight()
	if got := m.input.Height(); got != 12 {
		t.Fatalf("input.Height() = %d, want 12 (old cap was 8; budget here is 36)", got)
	}
}

func TestMaxHeightFloorIsOneRow(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.height = 5 // below every budget; no chrome visible
	if got := m.maxInputHeight(); got != 1 {
		t.Fatalf("maxInputHeight() = %d, want 1 (the input always keeps a row)", got)
	}
}

// Bubbles v2 scrolls the textarea with the cursor line kept visible; this
// pins the behavior we rely on when content exceeds MaxHeight.
func TestTextareaKeepsCursorVisiblePastMaxHeight(t *testing.T) {
	input := textarea.New()
	input.MaxHeight = 3
	input.MinHeight = 1
	input.DynamicHeight = true
	input.SetWidth(20)
	input.Focus()
	var cmd tea.Cmd
	input, cmd = input.Update(tea.PasteMsg{Content: "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10"})
	_ = cmd
	if got := input.View(); !strings.Contains(got, "l10") {
		t.Fatalf("cursor line should stay visible past MaxHeight, view = %q", got)
	}
}
