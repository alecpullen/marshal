package dock

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
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
	// A FullFrame panel is offered every row the status line leaves. The
	// closing row is charged only when Close actually appends one, which
	// TestFullFrameViewIsClosedInItsBudget and
	// TestFullFrameClosedPanelFillsItsBudget pin from both sides.
	if want := 40 - statusLineRows; full.gotMaxH != want {
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

// fullFrameStub fills whatever height budget it is given with an open
// chrome frame, the way a full-frame settings panel draws itself.
type fullFrameStub struct{ gotMaxH int }

func (s *fullFrameStub) Update(msg tea.Msg) tea.Cmd { return nil }
func (s *fullFrameStub) Sizing() Sizing             { return FullFrame }
func (s *fullFrameStub) View(width, maxHeight int) string {
	s.gotMaxH = maxHeight
	body := strings.TrimSuffix(strings.Repeat("row\n", max(maxHeight-1, 1)), "\n")
	return chrome.PanelWithHints("Full", "esc close", body, layout.PanelWidth(width), maxHeight, true, theme.Current())
}

// TestFullFrameViewIsClosedInItsBudget pins decision 8: the dock hands a
// FullFrame panel one row less than the status line leaves, then closes the
// open frame itself, so the panel fills frameHeight-statusLineRows rows and
// ends on a bottom border.
func TestFullFrameViewIsClosedInItsBudget(t *testing.T) {
	const frameHeight = 40
	var h Host
	h.Open(&fullFrameStub{})

	view := h.View(100, frameHeight)
	if got, want := h.Rows(), frameHeight-statusLineRows; got != want {
		t.Fatalf("FullFrame view is %d rows, want frameHeight-statusLineRows = %d:\n%s", got, want, ansi.Strip(view))
	}
	if got, want := len(strings.Split(ansi.Strip(view), "\n")), h.Rows(); got != want {
		t.Fatalf("measured %d rows but rendered %d", want, got)
	}
	lines := strings.Split(ansi.Strip(view), "\n")
	if !strings.HasPrefix(lines[0], "╭─ ") {
		t.Fatalf("first row is not an open frame top: %q", lines[0])
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("last row = %q, want the dock's closing bottom border", last)
	}
}

// closedFullFrameStub draws a closed chrome frame, the way the settings and
// agents panels do. It already ends on its own bottom border, so the dock must
// not reserve a row for closing it.
type closedFullFrameStub struct{ gotMaxH int }

func (s *closedFullFrameStub) Update(msg tea.Msg) tea.Cmd { return nil }
func (s *closedFullFrameStub) Sizing() Sizing             { return FullFrame }
func (s *closedFullFrameStub) View(width, maxHeight int) string {
	s.gotMaxH = maxHeight
	body := strings.TrimSuffix(strings.Repeat("row\n", max(maxHeight-2, 1)), "\n")
	return chrome.Frame{Title: "Full", Closed: true, Focused: true}.
		Render(body, layout.PanelWidth(width)+2, maxHeight, theme.Current())
}

// TestFullFrameClosedPanelFillsItsBudget pins the other half of the contract:
// a panel that draws its own bottom border keeps the whole budget. Reserving a
// row for a close that never happens left the panel one row short and
// padRows painted a blank row between its ╰╯ and the status line.
func TestFullFrameClosedPanelFillsItsBudget(t *testing.T) {
	const frameHeight = 40
	var h Host
	h.Open(&closedFullFrameStub{})

	view := h.View(100, frameHeight)
	want := frameHeight - statusLineRows
	if got := h.Rows(); got != want {
		t.Fatalf("closed FullFrame view is %d rows, want %d: a closed panel must not lose a row to the dock's close reserve:\n%s",
			got, want, ansi.Strip(view))
	}
	if got, want := h.panel.(*closedFullFrameStub).gotMaxH, frameHeight-statusLineRows; got != want {
		t.Fatalf("panel was given maxHeight %d, want %d", got, want)
	}
	lines := strings.Split(ansi.Strip(view), "\n")
	if first := lines[0]; !strings.HasPrefix(first, "╭─ ") {
		t.Fatalf("first row = %q, want the panel's own top border", first)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("last row = %q, want the panel's own bottom border", last)
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
