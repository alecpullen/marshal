package dock

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

type stubPanel struct {
	sizing   Sizing
	gotWidth int
	gotMaxH  int
}

func (s *stubPanel) Update(msg tea.Msg) tea.Cmd { return nil }
func (s *stubPanel) View(width, maxHeight int) string {
	s.gotWidth, s.gotMaxH = width, maxHeight
	return "stub"
}
func (s *stubPanel) Sizing() Sizing { return s.sizing }

func TestHostViewBudgetsBySizingHint(t *testing.T) {
	docked := &stubPanel{sizing: Docked}
	h := &Host{}
	h.Open(docked)
	h.View(100, 40)
	if want := MaxRows(40); docked.gotMaxH != want {
		t.Fatalf("Docked panel got maxHeight %d, want MaxRows(40) = %d", docked.gotMaxH, want)
	}

	full := &stubPanel{sizing: FullFrame}
	h.Open(full)
	h.View(100, 40)
	if want := 40 - 1; full.gotMaxH != want {
		t.Fatalf("FullFrame panel got maxHeight %d, want frame minus status line = %d", full.gotMaxH, want)
	}
	if !h.FullFrameOpen() {
		t.Fatal("FullFrameOpen() = false with a FullFrame panel open")
	}
	h.Open(docked)
	if h.FullFrameOpen() {
		t.Fatal("FullFrameOpen() = true with a Docked panel open")
	}
}
