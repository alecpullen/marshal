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

func TestScalarInlineEditAppliesAndValidates(t *testing.T) {
	n := 5
	fl := newFieldList(func() []*field {
		return []*field{intField2("t.n", "Count", func() int { return n }, 1, func(v int) { n = v })}
	})
	fl.SetSize(60, 20)

	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit
	if !fl.Editing() {
		t.Fatal("enter should open inline edit")
	}
	fl.input.SetValue("") // clear the pre-filled "5" so typing "12" gives exactly "12"
	for _, r := range "12" {
		fl.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("edit should apply 12, got %d", n)
	}
	if fl.Editing() {
		t.Fatal("apply should close the edit")
	}

	// invalid input blocks apply and shows the error
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.input.SetValue("abc")
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("invalid input must not apply, got %d", n)
	}
	if !strings.Contains(fl.View(), "must be a number") {
		t.Fatalf("error should render, got:\n%s", fl.View())
	}
	// esc cancels without applying
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if fl.Editing() {
		t.Fatal("esc should cancel the edit")
	}
}

func TestScalarReadOnlyRowIgnoresEnter(t *testing.T) {
	fl := newFieldList(func() []*field {
		return []*field{{id: "t.ro", title: "Preset", kind: kindScalar, getStr: func() string { return "qwen" }}}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if fl.Editing() {
		t.Fatal("read-only row must not open an edit")
	}
}

func TestMaskedRowKeepsOnEmptyAndClearsOnD(t *testing.T) {
	secret := "sk-abcd1234"
	fl := newFieldList(func() []*field {
		return []*field{secretRow("t.key", "API key", func() string { return secret }, func(v string) { secret = v })}
	})
	fl.SetSize(60, 20)
	if !strings.Contains(fl.View(), "••••1234") {
		t.Fatalf("masked value should render last four, got:\n%s", fl.View())
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply empty = keep
	if secret != "sk-abcd1234" {
		t.Fatalf("empty apply must keep the secret, got %q", secret)
	}
	fl.Update(kp("d"))
	if secret != "" {
		t.Fatalf("d must clear the secret, got %q", secret)
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

func TestEnumCycleWithArrows(t *testing.T) {
	v := "deny"
	fl := newFieldList(func() []*field {
		return []*field{enumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if v != "confirm" {
		t.Fatalf("right should cycle deny→confirm, got %q", v)
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if v != "deny" {
		t.Fatalf("left should cycle back to deny, got %q", v)
	}
}

func TestEnumPickerSelectsOption(t *testing.T) {
	v := "deny"
	fl := newFieldList(func() []*field {
		return []*field{enumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open picker
	if !fl.Editing() {
		t.Fatal("enter should open the picker")
	}
	view := fl.View()
	if !strings.Contains(view, "confirm") || !strings.Contains(view, "allow") {
		t.Fatalf("picker should list all options, got:\n%s", view)
	}
	fl.Update(kp("j"))
	fl.Update(kp("j"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v != "allow" {
		t.Fatalf("picker should apply allow, got %q", v)
	}
	if fl.Editing() {
		t.Fatal("picker should close after apply")
	}
}
