package dock

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakePanel struct{ lastW, lastH int }

func (f *fakePanel) Update(tea.Msg) tea.Cmd { return nil }

func (f *fakePanel) View(width, maxHeight int) string {
	f.lastW, f.lastH = width, maxHeight
	return "line1\nline2\nline3"
}

func TestMaxRows(t *testing.T) {
	cases := []struct{ frame, want int }{
		{24, 9},
		{40, 16},
		{10, 6},
	}
	for _, testCase := range cases {
		if got := MaxRows(testCase.frame); got != testCase.want {
			t.Errorf("MaxRows(%d) = %d, want %d", testCase.frame, got, testCase.want)
		}
	}
}

func TestHostLifecycle(t *testing.T) {
	var host Host
	if host.IsOpen() || host.Rows() != 0 || host.View(80, 24) != "" {
		t.Fatal("empty host must be closed, zero rows, empty view")
	}

	panel := &fakePanel{}
	host.Open(panel)
	if !host.IsOpen() {
		t.Fatal("host should be open after Open")
	}

	view := host.View(80, 24)
	if !strings.Contains(view, "line2") {
		t.Fatalf("view should render panel content, got %q", view)
	}
	if panel.lastH != MaxRows(24) {
		t.Errorf("panel got maxHeight %d, want %d", panel.lastH, MaxRows(24))
	}
	if host.Rows() != 3 {
		t.Errorf("Rows() = %d after render, want 3", host.Rows())
	}

	host.CloseNow()
	if host.IsOpen() || host.Rows() != 0 {
		t.Fatal("CloseNow must reset panel and rows")
	}
}
