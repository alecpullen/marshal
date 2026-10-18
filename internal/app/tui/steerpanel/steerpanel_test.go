package steerpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

func TestSteerPanelSubmitsLiteralBoundText(t *testing.T) {
	p := New("session", "turn")
	p.Update(tea.PasteMsg{Content: "/literal instruction"})
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(SubmitMsg)
	if msg.ComposerID != p.ID() || msg.SessionID != "session" || msg.TurnID != "turn" ||
		msg.Text != "/literal instruction" {
		t.Fatalf("%+v", msg)
	}
	long := strings.Repeat("x", 5000)
	other := New("session", "turn")
	other.Update(tea.PasteMsg{Content: long})
	if other.Text() != long {
		t.Fatal("silently truncated pasted draft")
	}
}

func TestSteerPanelEscAndEmptySubmissionAreOnlyMessages(t *testing.T) {
	p := New("session", "turn")
	close := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})().(CloseMsg)
	if close.ComposerID != p.ID() {
		t.Fatal("wrong composer")
	}
	submit := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(SubmitMsg)
	if submit.Text != "" {
		t.Fatal("invented text")
	}
	if p.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) != nil {
		t.Fatal("captured global ctrl+c")
	}
}

func TestSteerPanelShiftEnterAndPasteStayInComposer(t *testing.T) {
	p := New("session", "turn")
	p.Update(tea.PasteMsg{Content: "first"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	p.Update(tea.PasteMsg{Content: "second"})
	if !strings.Contains(p.Text(), "\n") || !strings.Contains(p.Text(), "second") {
		t.Fatal(p.Text())
	}
	if New("session", "turn").Text() != "" {
		t.Fatal("copied another surface")
	}
}

func TestSteerPanelLayoutAndMonochrome(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.Reload(previous) })
	for _, mono := range []bool{false, true} {
		theme.Reload(theme.LoadFor(mono, "xterm-256color"))
		for _, width := range []int{80, 120, 200} {
			for _, height := range []int{6, 9, 16, 24} {
				p := New("session", "turn")
				out := p.View(width, height)
				if lipgloss.Height(out) > height {
					t.Fatalf("height %d", height)
				}
				for _, line := range strings.Split(out, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width %d", width)
					}
				}
				for _, label := range []string{"Send now", "parent conversation", "background tasks continue", "Enter send now", "Esc back"} {
					if !strings.Contains(ansi.Strip(out), label) {
						t.Fatalf("missing %q: %s", label, out)
					}
				}
			}
		}
	}
}

func TestSteerPanelResizeKeepsDraftAndIdentity(t *testing.T) {
	p := New("session", "turn")
	id := p.ID()
	p.Update(tea.PasteMsg{Content: "keep this draft"})
	p.View(80, 9)
	p.View(120, 16)
	if p.ID() != id || p.Text() != "keep this draft" {
		t.Fatal("resize changed draft")
	}
}
