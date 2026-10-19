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
	got := cmd().(DoneMsg)
	if !got.Stop || len(got.IDs) != 1 || got.IDs[0] != "job:1" {
		t.Fatalf("confirmation = %+v", got)
	}
}

// TestRefreshPreservesTheUsersSelection pins the exit-reachability fix: a
// refresh must not re-seat a destructive choice on the safe one. Re-seating it
// makes the exit unreachable whenever the membership keeps growing — the user
// presses stop, the panel quietly selects keep, and the next Enter keeps the
// work.
func TestRefreshPreservesTheUsersSelection(t *testing.T) {
	p := New(false, []Item{{ID: "job:1", Label: "download"}})
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Replace([]Item{{ID: "job:1", Label: "download"}, {ID: "job:2", Label: "test"}})
	got := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(DoneMsg)
	if !got.Stop {
		t.Fatal("a refresh re-seated the user's stop choice on keep")
	}
	if len(got.IDs) != 2 || got.IDs[0] != "job:1" || got.IDs[1] != "job:2" {
		t.Fatalf("confirmation IDs = %v, want the refreshed membership", got.IDs)
	}
}

// TestFreshPanelStartsOnTheSafeChoice pins the other half: preserving the
// cursor across a refresh must not make a NEW warning default to stopping.
func TestFreshPanelStartsOnTheSafeChoice(t *testing.T) {
	p := New(false, []Item{{ID: "job:1", Label: "download"}})
	if got := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(DoneMsg); got.Stop {
		t.Fatal("a fresh warning did not default to keep")
	}
}

// TestNoticeIsRenderedAndCleared pins the explicit re-ask: a refusal's notice
// appears above the choices and a later refresh clears it.
func TestNoticeIsRenderedAndCleared(t *testing.T) {
	p := New(false, []Item{{ID: "job:1", Label: "download"}})
	p.SetNotice("1 new item appeared after you chose — review and confirm again")
	view := p.View(80, 20)
	if !strings.Contains(view, "new item appeared after you chose") {
		t.Fatalf("notice missing from the view:\n%s", view)
	}
	p.SetNotice("")
	if strings.Contains(p.View(80, 20), "new item appeared") {
		t.Fatal("cleared notice still rendered")
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
