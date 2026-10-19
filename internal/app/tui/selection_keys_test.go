package tui

import (
	"strings"
	"testing"
)

func TestEscClearsTheSelectionBeforeBrowsing(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m = pressKeys(m, "esc")
	if m.sel != nil || m.browsing {
		t.Fatalf("first Esc: sel=%v browsing=%v, want the selection cleared and nothing else", m.sel, m.browsing)
	}
	m = pressKeys(m, "esc")
	if !m.browsing {
		t.Fatal("second Esc should enter browse mode")
	}
}

func TestDrillClearsTheSelection(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.drillIntoSubagent(m.state.Subagents()[0])
	if m.sel != nil {
		t.Fatal("drilling in must clear the selection")
	}
	selectRows(t, &m, selPos{0, 0}, selPos{0, 79})
	m.popDrill()
	if m.sel != nil {
		t.Fatal("drilling out must clear the selection")
	}
}

func TestCtrlSClearsTheSelectionAndNamesDragToCopy(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m = pressKeys(m, "ctrl+s")
	if m.sel != nil {
		t.Fatal("Ctrl+S must clear the selection")
	}
	m = pressKeys(m, "ctrl+s")
	msgs := m.state.Messages()
	if last := msgs[len(msgs)-1].Content; !strings.Contains(last, "drag selects and copies") {
		t.Fatalf("re-capture notice = %q, want it to say drag selects and copies", last)
	}
}
