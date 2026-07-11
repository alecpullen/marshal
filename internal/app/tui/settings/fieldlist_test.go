package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// kp is a single-rune key helper. Special keys (Space, etc.) are constructed
// inline to avoid conflict with the "charm.land/bubbles/v2/key" import.
func kp(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func testToggleField(title string, val *bool) *field {
	return &field{
		id: "test." + title, title: title, kind: kindToggle,
		getBool: func() bool { return *val },
		setBool: func(b bool) { *val = b },
	}
}

func TestFieldListNavigationAndToggle(t *testing.T) {
	a, b := false, false
	fl := newFieldList(func() []*field {
		return []*field{testToggleField("Alpha", &a), testToggleField("Beta", &b)}
	})
	fl.SetSize(60, 20)

	if fl.CursorRow().title != "Alpha" {
		t.Fatalf("cursor should start on first row, got %q", fl.CursorRow().title)
	}
	fl.Update(kp("j"))
	if fl.CursorRow().title != "Beta" {
		t.Fatalf("j should move to Beta, got %q", fl.CursorRow().title)
	}
	fl.Update(kp("k"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !a {
		t.Fatal("space should toggle Alpha to true")
	}
	view := fl.View()
	if !strings.Contains(view, "Alpha") || !strings.Contains(view, "on ●") {
		t.Fatalf("view should show toggled value, got:\n%s", view)
	}
	if !strings.Contains(view, "off ○") {
		t.Fatalf("view should show Beta off, got:\n%s", view)
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("view should mark the cursor row, got:\n%s", view)
	}
}

func TestFieldListGandG(t *testing.T) {
	a, b, c := false, false, false
	fl := newFieldList(func() []*field {
		return []*field{testToggleField("A", &a), testToggleField("B", &b), testToggleField("C", &c)}
	})
	fl.SetSize(60, 20)
	fl.Update(kp("G"))
	if fl.CursorRow().title != "C" {
		t.Fatalf("G should jump to last row, got %q", fl.CursorRow().title)
	}
	fl.Update(kp("g"))
	if fl.CursorRow().title != "A" {
		t.Fatalf("g should jump to first row, got %q", fl.CursorRow().title)
	}
}

func TestFieldListDescriptionShownForCursorRow(t *testing.T) {
	a := false
	f := testToggleField("Alpha", &a)
	f.desc = "controls the alpha behavior"
	b := false
	g := testToggleField("Beta", &b)
	g.desc = "controls the beta behavior"
	fl := newFieldList(func() []*field { return []*field{f, g} })
	fl.SetSize(60, 20)
	view := fl.View()
	if !strings.Contains(view, "controls the alpha behavior") {
		t.Fatalf("cursor row description should render, got:\n%s", view)
	}
	if strings.Contains(view, "controls the beta behavior") {
		t.Fatalf("non-cursor description should NOT render, got:\n%s", view)
	}
}

func TestFieldListScrollsToKeepCursorVisible(t *testing.T) {
	vals := make([]bool, 30)
	fl := newFieldList(func() []*field {
		out := make([]*field, 30)
		for i := range out {
			out[i] = testToggleField(strings.Repeat("x", 3)+string(rune('a'+i%26)), &vals[i])
		}
		return out
	})
	fl.SetSize(60, 8)
	fl.Update(kp("G"))
	view := fl.View()
	if len(strings.Split(view, "\n")) > 8 {
		t.Fatalf("view must not exceed height 8, got %d lines", len(strings.Split(view, "\n")))
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("cursor row must remain visible after G, got:\n%s", view)
	}
	if !strings.Contains(view, "↑") {
		t.Fatalf("expected ↑ more indicator when scrolled down, got:\n%s", view)
	}
}
