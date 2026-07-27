package layout

import "testing"

func TestPanelWidthFillsDockWidth(t *testing.T) {
	if got := PanelWidth(120); got != 118 {
		t.Fatalf("PanelWidth(120) = %d, want 118 (fills, 2-cell margin)", got)
	}
	if got := PanelWidth(20); got != 30 {
		t.Fatalf("PanelWidth(20) = %d, want 30 (floor)", got)
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
