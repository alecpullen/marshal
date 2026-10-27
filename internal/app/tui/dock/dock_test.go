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

// closeAwarePanel records how often the host told it that it is gone.
type closeAwarePanel struct {
	stubPanel
	closes int
}

func (p *closeAwarePanel) OnClose() { p.closes++ }

// uncomparablePanel holds a slice, so its dynamic type cannot be compared with
// ==. The host must never do that: it would panic as soon as such a panel is
// displaced.
type uncomparablePanel struct{ notes []string }

func (p uncomparablePanel) Update(tea.Msg) tea.Cmd { return nil }
func (p uncomparablePanel) View(int, int) string   { return "" }
func (p uncomparablePanel) Sizing() Sizing         { return Docked }

// TestHostCloseAwareDisplacementAndClose pins the host's notification rules:
// a permanently displaced panel is told exactly once, a repeated close is
// inert, a panel without the interface is hosted unchanged, and panels are
// never compared by identity.
func TestHostCloseAwareDisplacementAndClose(t *testing.T) {
	// Replacing the visible panel closes the one it displaces, once, and does
	// not tell the newcomer anything.
	first, second := &closeAwarePanel{}, &closeAwarePanel{}
	var h Host
	h.Open(first)
	if first.closes != 0 {
		t.Fatalf("opening a panel into an empty host notified it %d times", first.closes)
	}
	h.Open(second)
	if first.closes != 1 {
		t.Fatalf("displaced panel notified %d times, want exactly 1", first.closes)
	}
	if second.closes != 0 {
		t.Fatalf("the newly opened panel was notified %d times", second.closes)
	}

	// Closing notifies the active panel once and empties the slot; a repeated
	// close has nothing left to notify.
	h.CloseNow()
	if second.closes != 1 {
		t.Fatalf("closed panel notified %d times, want exactly 1", second.closes)
	}
	if h.IsOpen() {
		t.Fatal("CloseNow left a panel open")
	}
	h.CloseNow()
	if second.closes != 1 {
		t.Fatalf("a repeated CloseNow notified the panel again: %d notifications", second.closes)
	}

	// A panel without the interface is hosted exactly the same; there is simply
	// nobody to tell.
	plain := &stubPanel{}
	h.Open(plain)
	if h.Panel() != Panel(plain) {
		t.Fatalf("host holds %T, want the plain panel", h.Panel())
	}
	h.CloseNow()
	if h.IsOpen() {
		t.Fatal("CloseNow left the plain panel open")
	}

	// SwapRetained exchanges the slot without notifying either panel, and hands
	// the displaced one back for restoration or release.
	retained, replacement := &closeAwarePanel{}, &closeAwarePanel{}
	h.Open(retained)
	if got := h.SwapRetained(replacement); got != Panel(retained) {
		t.Fatalf("SwapRetained returned %T, want the displaced panel", got)
	}
	if retained.closes != 0 || replacement.closes != 0 {
		t.Fatalf("a swap notified: displaced %d, incoming %d", retained.closes, replacement.closes)
	}
	if h.Panel() != Panel(replacement) {
		t.Fatalf("host holds %T after the swap, want the incoming panel", h.Panel())
	}

	// ReleaseRetained notifies the released panel and leaves the slot alone: the
	// caller owns it, and a later close may notify the same panel again.
	h.ReleaseRetained(retained)
	if retained.closes != 1 {
		t.Fatalf("released panel notified %d times, want exactly 1", retained.closes)
	}
	if h.Panel() != Panel(replacement) {
		t.Fatalf("ReleaseRetained touched the slot: %T", h.Panel())
	}
	h.ReleaseRetained(nil)
	if retained.closes != 1 {
		t.Fatalf("releasing nil notified the retained panel again: %d", retained.closes)
	}

	// Non-comparable panels are handled the same way: no identity comparison
	// anywhere, so nothing panics.
	h.Open(uncomparablePanel{notes: []string{"a"}})
	h.Open(uncomparablePanel{notes: []string{"b"}})
	h.CloseNow()
	h.SwapRetained(uncomparablePanel{notes: []string{"c"}})
	h.ReleaseRetained(uncomparablePanel{notes: []string{"d"}})
	h.CloseNow()
	if h.IsOpen() {
		t.Fatal("the host kept a non-comparable panel open")
	}
}
