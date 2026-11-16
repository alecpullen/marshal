package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// pasteDraftModel is a screen whose composer holds text with one condensed
// paste in it, built through the real paste path.
func pasteDraftModel(t *testing.T, content, tail string) (Model, string) {
	t.Helper()
	m := newViewTestModel(t, 100, 30)
	updated, _ := m.Update(tea.PasteMsg{Content: content})
	m = updated.(Model)
	m.input.InsertString(tail)
	m.refreshViewport()
	if len(m.pastes) != 1 {
		t.Fatalf("pastes = %d, want 1", len(m.pastes))
	}
	return m, m.pastes[0].chipText()
}

func TestADraftRoundTrips(t *testing.T) {
	content := strings.Repeat("line\n", 9) + "tail" // 10 lines, so it condenses
	m, chip := pasteDraftModel(t, content, " please summarise")
	if !strings.Contains(m.input.Value(), chip) {
		t.Fatalf("composer %q missing chip %q", m.input.Value(), chip)
	}

	d := m.Draft()
	if d.Empty() {
		t.Fatal("a composer with text and a paste is not an empty draft")
	}

	// The screen opens the connect panel on start, as a first run does; the
	// draft must still be in the composer underneath it.
	m2 := testModel(t, stateOf(m), WithDraft(d), WithOpenConnectOnStart())
	m2.resize(100, 30)
	m2.refreshViewport()
	if !m2.dock.IsOpen() {
		t.Fatal("connect panel should be open on start")
	}
	if got, want := m2.input.Value(), m.input.Value(); got != want {
		t.Fatalf("composer under the start panel = %q, want %q", got, want)
	}
	m2.dock.CloseNow()
	if got, want := m2.input.Value(), m.input.Value(); got != want {
		t.Fatalf("composer after the start panel closed = %q, want %q", got, want)
	}
	if len(m2.pastes) != len(m.pastes) {
		t.Fatalf("pastes after the round trip = %d, want %d", len(m2.pastes), len(m.pastes))
	}

	// The composer renders the same, cursor included: SetValue leaves it
	// after the last rune, where typing the text left it.
	if got, want := m2.input.View(), m.input.View(); got != want {
		t.Fatalf("composer renders differently after the round trip:\n got %q\nwant %q", got, want)
	}
	if m2.input.Line() != m.input.Line() || m2.input.Column() != m.input.Column() {
		t.Fatalf("cursor = %d:%d, want %d:%d",
			m2.input.Line(), m2.input.Column(), m.input.Line(), m.input.Column())
	}

	// Sending each screen's draft expands the paste identically. Enter is the
	// submit path: it trims the textarea value, expands the chips from
	// m.pastes, and records the prompt it submitted.
	first, ok := pressKey(m, tea.KeyEnter)
	if !ok {
		t.Fatal("Enter was not handled on the first screen")
	}
	second, ok := pressKey(m2, tea.KeyEnter)
	if !ok {
		t.Fatal("Enter was not handled on the round-tripped screen")
	}
	if len(first.history) == 0 || len(second.history) == 0 {
		t.Fatalf("submitting recorded no prompt: %v / %v", first.history, second.history)
	}
	sent, roundTripped := first.history[0], second.history[0]
	if sent != roundTripped {
		t.Fatalf("round-tripped prompt = %q, want %q", roundTripped, sent)
	}
	if !strings.Contains(sent, strings.TrimRight(content, "\n")) {
		t.Fatalf("prompt %q does not carry the pasted content", sent)
	}
	if strings.Contains(sent, chip) {
		t.Fatalf("prompt %q still carries the chip %q", sent, chip)
	}
}

func TestAnEmptyComposerIsAnEmptyDraft(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if d := m.Draft(); !d.Empty() {
		t.Fatalf("a blank composer's draft is not empty: text=%q pastes=%d", d.text, len(d.pastes))
	}
	// An empty draft opens a screen with an empty composer, not a stale one.
	m2 := testModel(t, stateOf(m), WithDraft(m.Draft()))
	if !m2.Draft().Empty() {
		t.Fatalf("screen opened with an empty draft is not empty: %q", m2.input.Value())
	}
	if m2.input.Value() != "" {
		t.Fatalf("composer = %q, want empty", m2.input.Value())
	}
}

func TestADraftIsACopy(t *testing.T) {
	content := strings.Repeat("line\n", 9) + "tail"
	m, chip := pasteDraftModel(t, content, " keep me")

	d := m.Draft()
	if d.text != chip+" keep me" {
		t.Fatalf("draft text = %q, want %q", d.text, chip+" keep me")
	}

	// Editing the model after Draft leaves the draft alone: neither the text
	// nor the attachments the composer keeps.
	m.input.SetValue("something else entirely")
	m.input.InsertString(" and more")
	m.pastes = append(m.pastes, pasteAttachment{N: 2, Lines: 1, Chars: 3, Content: "new"})
	if d.text != chip+" keep me" {
		t.Fatalf("draft text changed with the model: %q", d.text)
	}
	if len(d.pastes) != 1 || d.pastes[0].Content != content {
		t.Fatalf("draft pastes followed the model's append: %d", len(d.pastes))
	}

	// And a draft given to a screen does not follow that screen's edits
	// either: the composer and the draft hold separate attachment slices.
	m2 := testModel(t, stateOf(m), WithDraft(d))
	m2.pastes = append(m2.pastes, pasteAttachment{N: 2, Lines: 1, Chars: 3, Content: "new"})
	m2.input.SetValue("edited over there")
	if d.text != chip+" keep me" || len(d.pastes) != 1 {
		t.Fatalf("draft followed the screen it was given to: text=%q pastes=%d", d.text, len(d.pastes))
	}
}
