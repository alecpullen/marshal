package dock

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/layout"
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

// TestMaxRows documents the 40%-of-frame height cap with a floor of 6
// rows. The table covers the linear regime, the floor, and a zero frame.
func TestMaxRows(t *testing.T) {
	tests := []struct {
		frameHeight int
		want        int
	}{
		{0, 6},   // floor applies immediately at zero
		{10, 6},  // below floor → floor
		{15, 6},  // floor boundary
		{20, 8},  // 20*2/5 = 8, well above the floor
		{24, 9},  // 24*2/5 = 9.6 → 9
		{40, 16}, // standard terminal height
		{100, 40},
	}
	for _, tc := range tests {
		if got := MaxRows(tc.frameHeight); got != tc.want {
			t.Errorf("MaxRows(%d) = %d, want %d", tc.frameHeight, got, tc.want)
		}
	}
}

// TestDockStatusLineRowsMatchesLayout pins dock.statusLineRows to
// layout.StatusLineRows. The two values are aliases today; if anyone
// reintroduces a literal in dock.go (regression), this catches the drift
// against the layout-side source of truth.
func TestDockStatusLineRowsMatchesLayout(t *testing.T) {
	if int(statusLineRows) != layout.StatusLineRows {
		t.Fatalf("dock.statusLineRows = %d, layout.StatusLineRows = %d — keep them in sync",
			statusLineRows, layout.StatusLineRows)
	}
}

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

type ownerPanel struct {
	got []tea.Msg
}

type ownedMsg struct{}

func (p *ownerPanel) Update(msg tea.Msg) tea.Cmd { p.got = append(p.got, msg); return nil }
func (p *ownerPanel) View(w, h int) string       { return "" }
func (p *ownerPanel) Sizing() Sizing             { return Docked }
func (p *ownerPanel) OwnsMsg(msg tea.Msg) bool   { _, ok := msg.(ownedMsg); return ok }

func TestHostOwnsMsg(t *testing.T) {
	var h Host
	if h.OwnsMsg(ownedMsg{}) {
		t.Error("empty host claimed a message")
	}

	h.Open(&ownerPanel{})
	if !h.OwnsMsg(ownedMsg{}) {
		t.Error("host did not claim the panel's own message")
	}
	if h.OwnsMsg(struct{}{}) {
		t.Error("host claimed a message the panel does not own")
	}
}

// A panel that does not implement MessageOwner must not claim anything.
func TestHostOwnsMsgNonOwner(t *testing.T) {
	var h Host
	h.Open(&stubPanel{})
	if h.OwnsMsg(ownedMsg{}) {
		t.Error("non-owner panel claimed a message")
	}
}
