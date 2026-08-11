package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

func TestShouldCondensePasteBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"seven lines short", strings.Repeat("line\n", 6) + "line", false},
		{"eight lines", strings.Repeat("line\n", 7) + "line", true},
		{"1501 chars single line", strings.Repeat("a", 1501), true},
		{"exactly 1500 chars", strings.Repeat("a", 1500), false},
		{"small", "hello", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCondensePaste(tc.content); got != tc.want {
				t.Fatalf("shouldCondensePaste(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestLargePasteCreatesAttachmentAndChip(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	content := strings.Repeat("line\n", 9) + "tail" // 10 lines

	updated, _ := m.Update(tea.PasteMsg{Content: content})
	mm := updated.(Model)

	if len(mm.pastes) != 1 {
		t.Fatalf("pastes = %d, want 1", len(mm.pastes))
	}
	att := mm.pastes[0]
	if att.N != 1 {
		t.Fatalf("attachment N = %d, want 1", att.N)
	}
	if att.Lines != 10 {
		t.Fatalf("attachment lines = %d, want 10", att.Lines)
	}
	if att.Content != content {
		t.Fatal("attachment content mismatch")
	}
	if !strings.Contains(mm.input.Value(), att.chipText()) {
		t.Fatalf("input value %q missing chip %q", mm.input.Value(), att.chipText())
	}
	// The textarea should not have grown to the paste's line count.
	if mm.input.Height() >= 10 {
		t.Fatalf("input height = %d, should stay small (chip only)", mm.input.Height())
	}
}

func TestTwoLargePastesGetSequentialNumbers(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	updated, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("a\n", 9)})
	m = updated.(Model)
	updated, _ = m.Update(tea.PasteMsg{Content: strings.Repeat("b\n", 9)})
	m = updated.(Model)

	if len(m.pastes) != 2 {
		t.Fatalf("pastes = %d, want 2", len(m.pastes))
	}
	if m.pastes[0].N != 1 || m.pastes[1].N != 2 {
		t.Fatalf("attachment numbers = %d, %d, want 1, 2", m.pastes[0].N, m.pastes[1].N)
	}
	if !strings.Contains(m.input.Value(), m.pastes[0].chipText()) {
		t.Fatal("first chip missing from input")
	}
	if !strings.Contains(m.input.Value(), m.pastes[1].chipText()) {
		t.Fatal("second chip missing from input")
	}
}

func TestSmallPasteFallsThroughToTextarea(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	updated, _ := m.Update(tea.PasteMsg{Content: "small paste"})
	m = updated.(Model)

	if len(m.pastes) != 0 {
		t.Fatalf("pastes = %d, want 0 for small paste", len(m.pastes))
	}
	if m.input.Value() != "small paste" {
		t.Fatalf("input value = %q, want small paste", m.input.Value())
	}
}

func TestEnterExpandsPasteIntoPrompt(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	content := strings.Repeat("line\n", 9) + "tail"
	updated, _ := m.Update(tea.PasteMsg{Content: content})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(Model)

	if len(mm.pastes) != 0 {
		t.Fatalf("pastes = %d after submit, want 0", len(mm.pastes))
	}
	msgs := mm.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a user message after submit")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleUser {
		t.Fatalf("last message role = %v, want user", last.Role)
	}
	if !strings.Contains(last.Content, "tail") {
		t.Fatalf("expanded prompt missing original content: %q", last.Content)
	}
	if strings.Contains(last.Content, "[pasted #") {
		t.Fatalf("expanded prompt still contains chip: %q", last.Content)
	}
}

func TestEnterDropsDeletedPasteAttachment(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	content := strings.Repeat("line\n", 9) + "tail"
	updated, _ := m.Update(tea.PasteMsg{Content: content})
	m = updated.(Model)

	// User deletes the chip from the textarea.
	m.input.SetValue("")
	m.input.MoveToEnd()

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(Model)

	if len(mm.pastes) != 0 {
		t.Fatalf("pastes = %d after deleting chip, want 0", len(mm.pastes))
	}
}

func TestSecondSubmitAfterPasteIsClean(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	updated, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("a\n", 9)})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	m.input.SetValue("second prompt")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(Model)

	msgs := mm.state.Messages()
	last := msgs[len(msgs)-1]
	if last.Content != "second prompt" {
		t.Fatalf("second prompt = %q, want 'second prompt'", last.Content)
	}
}
