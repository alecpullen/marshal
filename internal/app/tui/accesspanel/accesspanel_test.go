package accesspanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/theme"
)

func request() session.AccessRequest {
	return session.AccessRequest{RequestID: "r1", SessionID: "s1",
		Actor: "Marshal", StepID: 3, Reason: strings.Repeat("Downloading files. ", 20),
		Current: session.AccessState{Mode: "copilot"}, MissingSystem: true}
}

func press(p *Panel, key string) tea.Cmd {
	code := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape,
		"tab": tea.KeyTab, "down": tea.KeyDown, "pgdown": tea.KeyPgDown}[key]
	return p.Update(tea.KeyPressMsg{Code: code})
}

func TestAccessPanelDefaultsToDenyAndEscDenies(t *testing.T) {
	p := New(request())
	c := press(p, "enter")().(ChoiceMsg).Choice
	if c.Approved || c.RequestID != "r1" || c.Current.Mode != "copilot" {
		t.Fatalf("%+v", c)
	}
	press(p, "down")
	if c = press(p, "esc")().(ChoiceMsg).Choice; c.Approved {
		t.Fatal("esc approved")
	}
}

func TestAccessPanelChoicesMatchCapabilities(t *testing.T) {
	for _, editing := range []bool{false, true} {
		for _, system := range []bool{false, true} {
			r := request()
			r.MissingEditing, r.MissingSystem = editing, system
			p := New(r)
			want := 1
			if editing {
				want = 4
			} else if system {
				want = 2
			}
			if len(p.options) != want {
				t.Fatalf("%t/%t: %d", editing, system, len(p.options))
			}
			if want == 1 {
				continue
			}
			press(p, "tab")
			c := press(p, "enter")().(ChoiceMsg).Choice
			if !c.Approved || (c.Mode != "") != editing || c.Current != r.Current {
				t.Fatalf("%+v", c)
			}
		}
	}
}

func TestAccessPanelIgnoresPasteAndText(t *testing.T) {
	p := New(request())
	for _, msg := range []tea.Msg{tea.PasteMsg{Content: "enter auto"},
		tea.KeyPressMsg{Code: 'y', Text: "y"}, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
		if cmd := p.Update(msg); cmd != nil || p.selected != 0 {
			t.Fatal("input changed decision")
		}
	}
}

func TestAccessPanelFitsBudgetsAndMonochrome(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.Reload(previous) })
	for _, mono := range []bool{false, true} {
		theme.Reload(theme.LoadFor(mono, "xterm-256color"))
		for _, width := range []int{80, 120, 200} {
			for _, height := range []int{6, 9, 16, 24} {
				p := New(request())
				out := p.View(width, height)
				if lipgloss.Height(out) > height {
					t.Fatalf("height %d", height)
				}
				for _, line := range strings.Split(out, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width %d", width)
					}
				}
				plain := ansi.Strip(out)
				for _, text := range []string{"Allow system access?", "Current: copilot",
					"mode unchanged", "this session only", "no sudo", "Deny", "Enter decide", "Esc deny"} {
					if !strings.Contains(plain, text) {
						t.Fatalf("missing %q: %s", text, plain)
					}
				}
				if mono && strings.Contains(out, "\x1b[") {
					t.Fatal("monochrome emitted escape")
				}
			}
		}
	}
}

func TestAccessPanelDetailScrollKeepsDecisionAndIdentity(t *testing.T) {
	p := New(request())
	before := ansi.Strip(p.View(80, 9))
	press(p, "pgdown")
	after := ansi.Strip(p.View(80, 9))
	if before == after || p.selected != 0 || p.Request() != request() {
		t.Fatal("detail scroll changed selection or did not scroll")
	}
	p.View(120, 16)
	if p.selected != 0 || p.Request().RequestID != "r1" {
		t.Fatal("resize reset identity")
	}
}
