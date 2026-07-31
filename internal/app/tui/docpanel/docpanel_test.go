package docpanel

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/commands"
)

func testState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestPanelRendersRowsAndHeaders(t *testing.T) {
	p := New(commands.Doc{Title: "Things", Rows: []commands.Row{
		{Header: "Group"},
		{Text: "one", Detail: "first"},
	}}, testState(t))
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
	p := New(commands.Doc{Title: "T", Rows: []commands.Row{{Text: "a", Detail: "b"}}}, testState(t))
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc at root should emit a message")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("esc at root produced %T, want ClosedMsg", cmd())
	}
}

func TestPanelDrillInAndBack(t *testing.T) {
	p := New(commands.Doc{Title: "T", Rows: []commands.Row{
		{Text: "parent", Children: []commands.Row{{Text: "child", Detail: "c"}}},
	}}, testState(t))
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
	p := New(commands.Doc{Title: "Help", Footer: footer, Rows: []commands.Row{
		{Header: "Group"},
		{Text: "one", Detail: "first"},
	}}, testState(t))
	out := p.View(100, 24)
	for _, want := range []string{"line one", "line two", "line three", "line four", "line five"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q:\n%s", want, out)
		}
	}
}

func TestPanelActionEmitsActionMsg(t *testing.T) {
	ran := false
	p := New(commands.Doc{Title: "T", Rows: []commands.Row{
		{Text: "do it", ActionLabel: "↵ run", Action: func(s *session.State) commands.Result {
			ran = true
			return commands.Text("done")
		}},
	}}, testState(t))
	p.View(100, 20)
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an action row should emit a command")
	}
	msg, ok := cmd().(ActionMsg)
	if !ok {
		t.Fatalf("action produced %T, want ActionMsg", cmd())
	}
	if !ran || msg.Result.Text != "done" {
		t.Fatalf("action did not run or wrong result: ran=%v text=%q", ran, msg.Result.Text)
	}
}
