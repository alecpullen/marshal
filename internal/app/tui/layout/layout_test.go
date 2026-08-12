package layout

import "testing"

func TestPanelWidthFillsDockWidth(t *testing.T) {
	if got := PanelWidth(120); got != 118 {
		t.Fatalf("PanelWidth(120) = %d, want 118 (fills, 2-cell margin)", got)
	}
}

// TestPanelWidthNeverExceedsDock pins F15: the floor must not hand back a
// width larger than the space available, which would overflow the dock. This
// is currently unreachable because the TUI gates below 80 columns, but the
// old assertion encoded the overflow as intended behaviour and would mislead
// anyone who lowers that gate.
func TestPanelWidthNeverExceedsDock(t *testing.T) {
	for _, dock := range []int{0, 1, 5, 20, 31, 32, 40, 200} {
		if got := PanelWidth(dock); dock > 0 && got > dock {
			t.Errorf("PanelWidth(%d) = %d, which is wider than the dock", dock, got)
		}
	}
}

// TestPanelWidthIsAlwaysUsable pins the other half: a panel width must stay
// positive so callers can subtract borders without going negative.
func TestPanelWidthIsAlwaysUsable(t *testing.T) {
	for _, dock := range []int{-5, 0, 1, 3, 20} {
		if got := PanelWidth(dock); got < 1 {
			t.Errorf("PanelWidth(%d) = %d, want >= 1", dock, got)
		}
	}
}

func TestTwoColumnBreakpoint(t *testing.T) {
	if TwoColumn(WideBreakpoint - 1) {
		t.Fatal("interior below the breakpoint must stay single-column")
	}
	if !TwoColumn(WideBreakpoint) {
		t.Fatal("interior at the breakpoint must split into two columns")
	}
}

func TestSplitPanes(t *testing.T) {
	list, detail := SplitPanes(100)
	if list != 50 || detail != 48 {
		t.Fatalf("SplitPanes(100) = (%d, %d), want (50, 48) — half, minus 2-cell gap", list, detail)
	}
	if list+detail+2 != 100 {
		t.Fatalf("panes plus gap must add back to the interior width")
	}
}
