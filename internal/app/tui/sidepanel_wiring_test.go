package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// newTestModelForRail builds a Model at the given size with the rail
// enabled or disabled. Reuse whatever constructor the existing view tests
// use (see view_test.go) rather than inventing a new one.
func newTestModelForRail(t *testing.T, w, h int, railEnabled bool) *Model {
	t.Helper()
	m := newTestModel(t) // match view_test.go's helper
	m.state.Config.TUI.SidePanel.Enabled = railEnabled
	m.resize(w, h)
	return &m
}

func TestRailAbsentBelowThreshold(t *testing.T) {
	m := newTestModelForRail(t, 119, 40, true)
	if m.railWidth != 0 {
		t.Errorf("railWidth = %d, want 0 below threshold", m.railWidth)
	}
	if m.leftWidth != m.width {
		t.Errorf("leftWidth = %d, want %d", m.leftWidth, m.width)
	}
}

// TestNarrowViewIsUnchanged is the regression gate: with no rail, the
// rendered frame must be identical to what a rail-unaware build produces.
func TestNarrowViewIsUnchanged(t *testing.T) {
	// Use the same model instance: enable the rail, resize at 100 (below
	// threshold), then disable and resize again. The view must be identical
	// because the rail is absent in both cases.
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(100, 40)
	withRail := m.viewString()

	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(100, 40)
	withoutRail := m.viewString()

	if withRail != withoutRail {
		t.Error("enabling the rail changed the sub-threshold view")
	}
	for i, line := range strings.Split(withoutRail, "\n") {
		if w := ansi.StringWidth(line); w > 100 {
			t.Errorf("row %d width = %d, want <= 100", i, w)
		}
	}
}

func TestRailPresentAboveThreshold(t *testing.T) {
	m := newTestModelForRail(t, 160, 40, true)
	if m.railWidth != 40 {
		t.Errorf("railWidth = %d, want 40", m.railWidth)
	}
	if m.leftWidth != 120 {
		t.Errorf("leftWidth = %d, want 120", m.leftWidth)
	}
}

func TestRailedViewRowsFillFrameWidth(t *testing.T) {
	m := newTestModelForRail(t, 160, 40, true)
	for i, line := range strings.Split(m.viewString(), "\n") {
		if w := ansi.StringWidth(line); w > 160 {
			t.Errorf("row %d width = %d, want <= 160: %q", i, w, stripANSI(line))
		}
	}
}

func TestRailedViewContainsDivider(t *testing.T) {
	m := newTestModelForRail(t, 160, 40, true)
	if !strings.Contains(stripANSI(m.viewString()), "│") {
		t.Error("no divider in the railed view")
	}
}

func TestStatusLineKeepsFullFrameWidth(t *testing.T) {
	m := newTestModelForRail(t, 160, 40, true)
	lines := strings.Split(m.viewString(), "\n")
	status := lines[len(lines)-1]
	if w := ansi.StringWidth(status); w != 160 {
		t.Errorf("status line width = %d, want 160 (full frame)", w)
	}
}

func TestCtrlBTogglesRail(t *testing.T) {
	m := newTestModelForRail(t, 160, 40, true)
	if !m.railEnabled() {
		t.Fatal("rail should start enabled")
	}
	m.railHidden = true
	m.resize(160, 40)
	if m.railEnabled() {
		t.Error("railEnabled = true after hiding")
	}
	if m.leftWidth != 160 {
		t.Errorf("leftWidth = %d, want 160 when hidden", m.leftWidth)
	}
}
