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

// TestLargePasteInsertsAtCursor pins that a condensed paste expands at the
// selected insertion point: the chip lands exactly where the cursor was, the
// cursor stays immediately after it, and the submitted prompt carries the full
// content in the draft's order.
func TestLargePasteInsertsAtCursor(t *testing.T) {
	content := strings.Repeat("pasted line\n", 9) + "pasted tail" // 10 lines

	cases := []struct {
		name      string
		draft     string
		row, col  int
		wantValue func(chip string) string
		order     []string
	}{
		{
			name:      "start",
			draft:     "alpha omega",
			row:       0,
			col:       0,
			wantValue: func(chip string) string { return chip + "alpha omega" },
			order:     []string{"pasted tail", "alpha omega"},
		},
		{
			name:      "middle",
			draft:     "alpha omega",
			row:       0,
			col:       5,
			wantValue: func(chip string) string { return "alpha" + chip + " omega" },
			order:     []string{"alpha", "pasted tail", "omega"},
		},
		{
			name:      "multiline",
			draft:     "first line\nmid line\nlast line",
			row:       1,
			col:       3,
			wantValue: func(chip string) string { return "first line\nmid" + chip + " line\nlast line" },
			order:     []string{"first line", "pasted tail", "last line"},
		},
		{
			name:      "unicode",
			draft:     "héllo wörld ✓",
			row:       0,
			col:       6,
			wantValue: func(chip string) string { return "héllo " + chip + "wörld ✓" },
			order:     []string{"héllo", "pasted tail", "wörld ✓"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newViewTestModel(t, 80, 24)
			m.input.SetValue(tc.draft)
			m.input.MoveToEnd()
			for m.input.Line() > tc.row {
				m.input.CursorUp()
			}
			m.input.SetCursorColumn(tc.col)

			updated, _ := m.Update(tea.PasteMsg{Content: content})
			m = updated.(Model)

			if len(m.pastes) != 1 {
				t.Fatalf("pastes = %d, want 1", len(m.pastes))
			}
			chip := m.pastes[0].chipText()
			want := tc.wantValue(chip)
			if got := m.input.Value(); got != want {
				t.Fatalf("value after paste = %q, want %q", got, want)
			}

			// The cursor sits immediately after the chip: the next rune lands
			// there, not at the end of the draft.
			if got, wantCol := m.input.Column(), tc.col+len([]rune(chip)); got != wantCol {
				t.Fatalf("cursor column = %d, want %d", got, wantCol)
			}
			m = sendKey(m, tea.KeyPressMsg{Code: 'X', Text: "X"})
			if got, wantTyped := m.input.Value(), strings.Replace(want, chip, chip+"X", 1); got != wantTyped {
				t.Fatalf("keystroke after paste = %q, want %q", got, wantTyped)
			}
			// Undo the probe so the submission carries the draft itself.
			m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
			if got := m.input.Value(); got != want {
				t.Fatalf("value after removing the probe = %q, want %q", got, want)
			}

			m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			msgs := m.state.Messages()
			if len(msgs) == 0 {
				t.Fatal("expected a user message after submit")
			}
			last := msgs[len(msgs)-1]
			if last.Role != session.RoleUser {
				t.Fatalf("last message role = %v, want user", last.Role)
			}
			if strings.Contains(last.Content, "[pasted #") {
				t.Fatalf("submitted prompt still contains a chip: %q", last.Content)
			}
			prev := -1
			for _, want := range tc.order {
				idx := strings.Index(last.Content, want)
				if idx < 0 {
					t.Fatalf("submitted prompt missing %q: %q", want, last.Content)
				}
				if idx < prev {
					t.Fatalf("submitted prompt puts %q out of order: %q", want, last.Content)
				}
				prev = idx
			}
		})
	}
}

// TestLargePasteAtInputLimitIsAllOrNothing pins that a paste either lands as
// one whole chip with its attachment or changes nothing: no partial chip, no
// attachment without a chip, and a visible notice naming the limit that
// refused it.
func TestLargePasteAtInputLimitIsAllOrNothing(t *testing.T) {
	content := strings.Repeat("line\n", 9) + "tail" // 10 lines
	chip := pasteAttachment{N: 1, Lines: 10}.chipText()

	// assertRefused checks the all-or-nothing contract for a rejected paste and
	// returns the model.
	assertRefused := func(t *testing.T, m Model, before string, col int, wantFlash string) Model {
		t.Helper()
		if len(m.pastes) != 0 {
			t.Fatalf("pastes = %d, want 0: a refused paste must not leave an attachment", len(m.pastes))
		}
		if got := m.input.Value(); got != before {
			t.Fatalf("value after a refused paste = %q, want the untouched draft %q", got, before)
		}
		if strings.Contains(m.input.Value(), "[pasted #") {
			t.Fatalf("refused paste left a partial chip in the input: %q", m.input.Value())
		}
		if got := m.input.Column(); got != col {
			t.Fatalf("cursor column after a refused paste = %d, want %d", got, col)
		}
		if !m.flashActive() {
			t.Fatal("refused paste showed no notice")
		}
		if !strings.Contains(m.flash, wantFlash) {
			t.Fatalf("notice = %q, want it to name %q", m.flash, wantFlash)
		}
		return m
	}

	t.Run("exactly enough", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.input.SetValue("draft")
		m.input.MoveToEnd()
		limit := m.input.Length() + len([]rune(chip))
		m.input.CharLimit = limit

		updated, _ := m.Update(tea.PasteMsg{Content: content})
		m = updated.(Model)

		if len(m.pastes) != 1 {
			t.Fatalf("pastes = %d, want 1: a paste that exactly fits must land", len(m.pastes))
		}
		want := "draft" + chip
		if got := m.input.Value(); got != want {
			t.Fatalf("value after paste = %q, want %q", got, want)
		}
		if got := m.input.Length(); got != limit {
			t.Fatalf("input length = %d, want the limit %d", got, limit)
		}
		if m.flashActive() {
			t.Fatalf("successful paste flashed %q", m.flash)
		}
	})

	t.Run("one rune too little", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.input.SetValue("draft")
		m.input.MoveToEnd()
		m.input.CharLimit = m.input.Length() + len([]rune(chip)) - 1
		before, col := m.input.Value(), m.input.Column()

		updated, _ := m.Update(tea.PasteMsg{Content: content})
		m = assertRefused(t, updated.(Model), before, col, "character")
	})

	t.Run("content height", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		// The draft fills the textarea's whole content-height budget and is wide
		// enough that the chip would wrap onto a second visual row, so the
		// textarea's height guard drops the insert.
		m.input.SetWidth(20)
		m.input.SetValue("draft text here")
		m.input.MoveToEnd()
		m.input.MaxContentHeight = 1
		before, col := m.input.Value(), m.input.Column()

		updated, _ := m.Update(tea.PasteMsg{Content: content})
		m = assertRefused(t, updated.(Model), before, col, "line limit")
	})
}

// TestMultiplePastesAndDraftPreservation pins that successive pastes keep their
// attachment order and insert at the cursor across a settled question and an
// abandoned approval edit, and that the draft still submits with every
// attachment expanded in place.
func TestMultiplePastesAndDraftPreservation(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	first := strings.Repeat("first line\n", 9) + "first tail"
	second := strings.Repeat("second line\n", 9) + "second tail"

	// The first chip goes at the start of the draft.
	m.input.SetValue("head tail")
	m.input.MoveToBegin()
	updated, _ := m.Update(tea.PasteMsg{Content: first})
	m = updated.(Model)
	if len(m.pastes) != 1 || m.pastes[0].Content != first {
		t.Fatalf("pastes = %+v, want the first paste only", m.pastes)
	}

	// A question arrives while the draft carries the first chip. Answering it
	// must leave both the draft and the attachment alone.
	q := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Archive or delete?"}},
		ResponseChan: make(chan []session.Answer, 1),
	}
	m.state.SetPendingQuestion(q)
	m = answerPendingQuestion(m)
	if len(m.pastes) != 1 || m.pastes[0].Content != first {
		t.Fatalf("pastes after the question = %+v, want the first paste only", m.pastes)
	}
	if !strings.Contains(m.input.Value(), m.pastes[0].chipText()) {
		t.Fatal("answering the question dropped the first chip")
	}

	// The second paste lands at the cursor, immediately after the first chip.
	updated, _ = m.Update(tea.PasteMsg{Content: second})
	m = updated.(Model)
	if len(m.pastes) != 2 {
		t.Fatalf("pastes = %d, want 2", len(m.pastes))
	}
	if m.pastes[0].N != 1 || m.pastes[1].N != 2 {
		t.Fatalf("attachment numbers = %d, %d, want 1, 2", m.pastes[0].N, m.pastes[1].N)
	}
	value := m.input.Value()
	firstChip, secondChip := m.pastes[0].chipText(), m.pastes[1].chipText()
	if !strings.Contains(value, firstChip) || !strings.Contains(value, secondChip) {
		t.Fatalf("value %q is missing one of the chips", value)
	}
	if strings.Index(value, firstChip) > strings.Index(value, secondChip) {
		t.Fatalf("chips are out of order in %q", value)
	}

	// Opening and abandoning the isolated approval edit drops neither chip nor
	// either attachment.
	tc := &session.PendingToolCall{
		ID:           "call_paste",
		Name:         "shell.run",
		Command:      "go test",
		Risk:         "shell",
		Reason:       "runs the test suite",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	m.state.SetPendingApproval(tc)
	m = chooseApprovalEdit(t, m)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // abandon the edit
	if got := m.input.Value(); got != value {
		t.Fatalf("value after the approval edit = %q, want %q", got, value)
	}
	if len(m.pastes) != 2 || m.pastes[0].Content != first || m.pastes[1].Content != second {
		t.Fatalf("pastes after the approval edit = %+v, want both pastes in order", m.pastes)
	}

	// Deny the pending approval so the composer owns Enter again. Right moves
	// the selection from Approve to Deny; huh finishes over a command round trip.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	for i := 0; i < 4 && m.state.PendingApproval() != nil; i++ {
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if m.state.PendingApproval() != nil {
		t.Fatal("approval still pending after deny")
	}
	if got := m.input.Value(); got != value {
		t.Fatalf("value after denying the approval = %q, want %q", got, value)
	}

	// The draft submits with both attachments expanded where their chips were.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var submitted string
	for _, msg := range m.state.Messages() {
		if msg.Role == session.RoleUser {
			submitted = msg.Content
		}
	}
	if submitted == "" {
		t.Fatal("expected a user message after submitting the draft")
	}
	if strings.Contains(submitted, "[pasted #") {
		t.Fatalf("submitted prompt still contains a chip: %q", submitted)
	}
	prev := -1
	for _, want := range []string{"first tail", "second tail", "head tail"} {
		idx := strings.Index(submitted, want)
		if idx < 0 {
			t.Fatalf("submitted prompt missing %q", want)
		}
		if idx < prev {
			t.Fatalf("submitted prompt puts %q out of order", want)
		}
		prev = idx
	}
	if len(m.pastes) != 0 {
		t.Fatalf("pastes = %d after submit, want 0", len(m.pastes))
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
