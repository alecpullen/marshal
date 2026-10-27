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

// TestAccessPanelOffersOnlyRequestedActSystem pins the simplified capability
// surface: Deny plus exactly the requested Act and/or System grant, with no
// autonomy variants.
func TestAccessPanelOffersOnlyRequestedActSystem(t *testing.T) {
	for _, tc := range []struct {
		name          string
		editing       bool
		system        bool
		wantLabels    []string
		wantGrantMode string
	}{
		{"none", false, false, []string{"Deny"}, ""},
		{"editing", true, false, []string{"Deny", "Act"}, "act"},
		{"system", false, true, []string{"Deny", "Allow system access for this conversation"}, ""},
		{"both", true, true, []string{"Deny", "Act + system access"}, "act"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			r.MissingEditing, r.MissingSystem = tc.editing, tc.system
			p := New(r)

			if len(p.options) != len(tc.wantLabels) {
				t.Fatalf("options = %+v, want %d", p.options, len(tc.wantLabels))
			}
			for i, want := range tc.wantLabels {
				if p.options[i].label != want {
					t.Fatalf("option[%d] = %q, want %q", i, p.options[i].label, want)
				}
			}
			for _, opt := range p.options {
				lower := strings.ToLower(opt.label)
				for _, retired := range []string{"edit", "copilot", "auto"} {
					if strings.Contains(lower, retired) {
						t.Fatalf("retired autonomy option %q remains: %+v", retired, p.options)
					}
				}
			}
			if p.selected != 0 {
				t.Fatal("a refreshed presentation must default to Deny")
			}
			if tc.wantGrantMode == "" {
				return
			}
			p.View(80, 24)
			press(p, "tab")
			c := press(p, "enter")().(ChoiceMsg).Choice
			if !c.Approved || c.Mode != tc.wantGrantMode || c.Current != r.Current {
				t.Fatalf("choice = %+v, want approved mode %q with the presented snapshot", c, tc.wantGrantMode)
			}
		})
	}
}

// TestAccessPanelDisplaysFullEffectiveState pins that the review shows the
// canonical mode, every modifier, the revision, conversation lifetime, and
// sudo's separate meaning.
func TestAccessPanelDisplaysFullEffectiveState(t *testing.T) {
	r := request()
	r.MissingEditing, r.MissingSystem = true, true
	r.Current = session.AccessState{Mode: "auto", Auto: true, System: true, Revision: 7}
	p := New(r)

	plain := ansi.Strip(p.View(120, 24))
	for _, want := range []string{
		"Current: Act · Auto · System · rev 7",
		"Act + system access",
		"for this conversation",
		"does not grant sudo",
		"Deny",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("panel missing %q:\n%s", want, plain)
		}
	}
}

func TestAccessPanelChoicesMatchCapabilities(t *testing.T) {
	for _, editing := range []bool{false, true} {
		for _, system := range []bool{false, true} {
			r := request()
			r.MissingEditing, r.MissingSystem = editing, system
			p := New(r)
			want := 1
			if editing || system {
				want = 2
			}
			if len(p.options) != want {
				t.Fatalf("%t/%t: %d", editing, system, len(p.options))
			}
			if want == 1 {
				continue
			}
			p.View(80, 24)
			press(p, "tab")
			c := press(p, "enter")().(ChoiceMsg).Choice
			if !c.Approved || (c.Mode != "") != editing || c.Current != r.Current {
				t.Fatalf("%+v", c)
			}
		}
	}
}

// TestAccessPanelHiddenOrUndersizedCannotApprove pins that a panel which never
// rendered its choices cannot turn Enter into a grant.
func TestAccessPanelHiddenOrUndersizedCannotApprove(t *testing.T) {
	for _, size := range []struct{ width, height int }{{0, 20}, {80, 0}, {10, 3}} {
		r := request()
		r.MissingEditing, r.MissingSystem = true, false
		p := New(r)
		if out := p.View(size.width, size.height); out != "" {
			t.Fatalf("size %dx%d rendered an actionable panel", size.width, size.height)
		}
		press(p, "tab")
		if cmd := press(p, "enter"); cmd != nil {
			t.Fatalf("size %dx%d approved from a hidden panel", size.width, size.height)
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
				for _, text := range []string{"Current: Act · rev 0",
					"mode unchanged", "for this conversation", "does not grant sudo", "Deny", "↵ decide", "Esc deny"} {
					if !strings.Contains(plain, text) {
						t.Fatalf("missing %q: %s", text, plain)
					}
				}
				// The title shares the top border with the key hints, so a
				// narrow border truncates it: its head always survives, and a
				// border with room pins it in full.
				if !strings.HasPrefix(plain, "╭─ ⚠ Allow system") {
					t.Fatalf("missing the titled border: %s", plain)
				}
				if width >= 120 && !strings.Contains(plain, "Allow system access?") {
					t.Fatalf("missing the full title: %s", plain)
				}
				if mono && strings.Contains(out, "\x1b[") {
					t.Fatal("monochrome emitted escape")
				}
			}
		}
	}
}

// TestAccessPanelHeaderCarriesTheKeys pins that the key row lives in the
// frame's top border, under a warning glyph, rather than inside the panel.
func TestAccessPanelHeaderCarriesTheKeys(t *testing.T) {
	r := request()
	r.MissingEditing, r.MissingSystem = true, false
	rows := strings.Split(ansi.Strip(New(r).View(120, 24)), "\n")
	if len(rows) == 0 {
		t.Fatal("panel rendered nothing")
	}
	if !strings.HasPrefix(rows[0], "╭─ ⚠ Allow editing?") || !strings.Contains(rows[0], "↵ decide") {
		t.Fatalf("top border = %q, want the title and keys", rows[0])
	}
	for _, row := range rows[1:] {
		if strings.Contains(row, "Esc deny") {
			t.Fatalf("key row still drawn inside the frame: %q", row)
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
