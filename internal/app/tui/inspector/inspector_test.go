package inspector

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func detail() Detail {
	var out []string
	for i := 0; i < 40; i++ {
		out = append(out, fmt.Sprintf("output line %d", i))
	}
	return Detail{
		Title:   "step 3 · tool 1/2",
		Subject: "shell.run go test ./...",
		Fields:  []Field{{"actor", "implementer"}, {"exit code", "1"}},
		Sections: []Section{
			{Name: "args", Body: `{"command": "go test"}`},
			{Name: "output", Body: strings.Join(out, "\n")},
		},
	}
}

func press(p *Panel, k tea.KeyPressMsg) tea.Msg {
	if cmd := p.Update(k); cmd != nil {
		return cmd()
	}
	return nil
}

func TestTabSwitchesSectionsAndShiftTabWraps(t *testing.T) {
	p := New(detail())
	if p.Section() != 0 {
		t.Fatalf("starts on %d", p.Section())
	}
	press(p, tea.KeyPressMsg{Code: tea.KeyTab})
	if p.Section() != 1 {
		t.Fatalf("tab -> %d", p.Section())
	}
	press(p, tea.KeyPressMsg{Code: tea.KeyTab})
	if p.Section() != 0 {
		t.Fatalf("tab wraps to %d", p.Section())
	}
	press(p, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if p.Section() != 1 {
		t.Fatalf("shift+tab wraps back to %d", p.Section())
	}
}

func TestScrollingMovesTheVisibleWindowAndStopsAtTheEnd(t *testing.T) {
	p := New(detail())
	press(p, tea.KeyPressMsg{Code: tea.KeyTab})
	first := ansi.Strip(p.View(100, 20))
	if !strings.Contains(first, "output line 0") || strings.Contains(first, "output line 39") {
		t.Fatalf("initial window wrong:\n%s", first)
	}
	for i := 0; i < 100; i++ {
		press(p, tea.KeyPressMsg{Code: tea.KeyDown})
		p.View(100, 20)
	}
	last := ansi.Strip(p.View(100, 20))
	if !strings.Contains(last, "output line 39") || strings.Contains(last, "output line 0\n") {
		t.Fatalf("scroll did not reach the end:\n%s", last)
	}
	press(p, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !strings.Contains(ansi.Strip(p.View(100, 20)), "output line 0") {
		t.Fatal("g returns to the top")
	}
}

func TestEachSectionScrollsIndependently(t *testing.T) {
	p := New(detail())
	press(p, tea.KeyPressMsg{Code: tea.KeyTab})
	p.View(100, 20)
	press(p, tea.KeyPressMsg{Code: tea.KeyPgDown})
	p.View(100, 20)
	press(p, tea.KeyPressMsg{Code: tea.KeyTab})
	if !strings.Contains(ansi.Strip(p.View(100, 20)), `"command"`) {
		t.Fatal("the args section keeps its own offset")
	}
}

func TestMessagesFromKeys(t *testing.T) {
	p := New(detail())
	cases := map[string]tea.Msg{
		"esc": ClosedMsg{},
		"n":   NavigateMsg{Delta: 1},
		"p":   NavigateMsg{Delta: -1},
		"o":   OpenMsg{},
	}
	for k, want := range cases {
		var msg tea.Msg
		if k == "esc" {
			msg = press(p, tea.KeyPressMsg{Code: tea.KeyEscape})
		} else {
			msg = press(p, tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		}
		if fmt.Sprintf("%#v", msg) != fmt.Sprintf("%#v", want) {
			t.Errorf("%s -> %#v, want %#v", k, msg, want)
		}
	}
	msg := press(p, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if c, ok := msg.(CopyMsg); !ok || !strings.Contains(c.Text, "command") {
		t.Errorf("y should copy the current section, got %#v", msg)
	}
}

func TestSetDetailKeepsTheSelectedSectionByName(t *testing.T) {
	p := New(detail())
	press(p, tea.KeyPressMsg{Code: tea.KeyTab}) // output
	next := detail()
	next.Title = "step 4"
	next.Sections = []Section{{Name: "narration", Body: "x"}, {Name: "output", Body: "y"}}
	p.SetDetail(next)
	if p.Section() != 1 {
		t.Fatalf("selected section = %d, want the output section kept", p.Section())
	}
}

func TestViewFitsItsFrame(t *testing.T) {
	p := New(detail())
	for _, w := range []int{80, 100, 140} {
		for _, h := range []int{12, 24, 40} {
			view := p.View(w, h)
			lines := strings.Split(view, "\n")
			if len(lines) > h {
				t.Errorf("w=%d h=%d: %d lines", w, h, len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > w {
					t.Errorf("w=%d h=%d: line %d wide: %q", w, h, ansi.StringWidth(l), l)
				}
			}
		}
	}
}
