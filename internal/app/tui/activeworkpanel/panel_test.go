package activeworkpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDefaultAndEscapeKeepWork(t *testing.T) {
	for _, code := range []rune{tea.KeyEnter, tea.KeyEscape} {
		p := New(false, []Item{{ID: "job:1", Label: "download"}})
		cmd := p.Update(tea.KeyPressMsg{Code: code})
		if cmd == nil || cmd().(DoneMsg).Stop {
			t.Fatalf("key %v did not keep work", code)
		}
	}
}

func TestConfirmationCapturesDisplayedIDs(t *testing.T) {
	items := []Item{{ID: "job:1", Label: "download"}}
	p := New(false, items)
	items[0].ID = "mutated"
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.Replace([]Item{{ID: "job:2", Label: "test"}})
	got := cmd().(DoneMsg)
	if !got.Stop || len(got.IDs) != 1 || got.IDs[0] != "job:1" {
		t.Fatalf("confirmation = %+v", got)
	}
	next := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(DoneMsg)
	if next.Stop {
		t.Fatal("refresh did not restore keep as default")
	}
}

func TestSmallAndReplacementViews(t *testing.T) {
	p := New(true, []Item{{ID: "job:1", Label: "download"}})
	if p.View(0, 0) != "" {
		t.Fatal("zero-size view must be empty")
	}
	for _, size := range [][2]int{{80, 9}, {120, 16}, {200, 24}} {
		view := p.View(size[0], size[1])
		if !strings.Contains(view, "Keep current conversation") || !strings.Contains(view, "download") {
			t.Fatalf("missing warning content at %v", size)
		}
	}
}
