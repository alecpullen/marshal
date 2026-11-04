package docpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/dock"
)

func TestPanelRendersRowsAndHeaders(t *testing.T) {
	p := New(acpwire.WireDoc{Title: "Things", Rows: []acpwire.WireRow{
		{Header: "Group"},
		{Text: "one", Detail: "first"},
	}}, nil)
	out := p.View(100, 20)
	for _, want := range []string{"Things", "Group", "one", "first"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if p.Sizing() != dock.Docked {
		t.Fatal("default sizing should be Docked")
	}
}

func TestPanelEscClosesAtRoot(t *testing.T) {
	p := New(acpwire.WireDoc{Title: "T", Rows: []acpwire.WireRow{{Text: "a", Detail: "b"}}}, nil)
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc at root should emit a message")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("esc at root produced %T, want ClosedMsg", cmd())
	}
}

func TestPanelDrillInAndBack(t *testing.T) {
	p := New(acpwire.WireDoc{Title: "T", Rows: []acpwire.WireRow{
		{Text: "parent", Children: []acpwire.WireRow{{Text: "child", Detail: "c"}}},
	}}, nil)
	p.View(100, 20)                               // sizes the list
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill
	out := p.View(100, 20)
	if !strings.Contains(out, "child") {
		t.Fatalf("expected drilled child rows:\n%s", out)
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("esc while drilled should pop, not close")
	}
	out = p.View(100, 20)
	if !strings.Contains(out, "parent") {
		t.Fatalf("expected back at root after pop:\n%s", out)
	}
}

func TestPanelFooterNotClipped(t *testing.T) {
	footer := "line one\nline two\nline three\nline four\nline five"
	p := New(acpwire.WireDoc{Title: "Help", Footer: footer, Rows: []acpwire.WireRow{
		{Header: "Group"},
		{Text: "one", Detail: "first"},
	}}, nil)
	out := p.View(100, 24)
	for _, want := range []string{"line one", "line two", "line three", "line four", "line five"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q:\n%s", want, out)
		}
	}
}

// A row with an action ID runs that ID through the act the panel was given.
func TestPanelActionRunsItsID(t *testing.T) {
	var ran string
	act := func(actionID string) tea.Cmd {
		return func() tea.Msg {
			ran = actionID
			return "ran " + actionID
		}
	}
	p := New(acpwire.WireDoc{Title: "T", Rows: []acpwire.WireRow{
		{Text: "do it", ActionLabel: "↵ run", ActionID: "x"},
	}}, act)
	p.View(100, 20)
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an action row should emit a command")
	}
	if msg := cmd(); msg != "ran x" {
		t.Fatalf("action produced %v, want the act's message", msg)
	}
	if ran != "x" {
		t.Fatalf("act saw %q, want %q", ran, "x")
	}
}

// Without an act, a row that carries an action ID is plain read-only text: the
// ACP side sends no IDs yet, and the browse help has no actions at all.
func TestARowWithoutAnActRendersAsText(t *testing.T) {
	p := New(acpwire.WireDoc{Title: "T", Rows: []acpwire.WireRow{
		{Text: "do it", Detail: "later", ActionLabel: "↵ run", ActionID: "x"},
	}}, nil)
	out := p.View(100, 20)
	for _, want := range []string{"do it", "later"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter on a row with no act returned a command: %T", cmd)
	}
}
