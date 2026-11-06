package textfield_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/agents"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/skills"
)

// TestPasteReachesSelectedTextInputs samples the TUI surfaces that own a text
// field — the picker, memory, agents, connect, settings and skills filter
// paths, plus the settings field-edit input — and asserts a paste reaches each
// one. It is a sample, not a complete inventory and not a panel-routing
// contract: other text fields are covered inside their own panels instead (for
// the skills install source editor see
// TestEscapeCancelsSkillEditorBeforeLeavingFrame, and for the plugin install
// source see TestPluginPasteFollowsFocus).
func TestPasteReachesSelectedTextInputs(t *testing.T) {
	const pasted = "pasted-text-xyz"

	cases := map[string]func(t *testing.T) string{
		"picker filter": func(t *testing.T) string {
			m := picker.New("Pick", "", []picker.Item{{Label: "one"}})
			m.Update(tea.PasteMsg{Content: pasted})
			return m.FilterValue()
		},
		"memory filter": func(t *testing.T) string {
			p := memory.NewPanel([]acpwire.MemoryEntry{{ID: 1, Kind: "fact", Content: "Uses SQLite"}}, nil, nil)
			p.Update(tea.PasteMsg{Content: pasted})
			return p.FilterValue()
		},
		"agents roster filter": func(t *testing.T) string {
			p := agents.NewRosterPanel(config.Default(), nil, "", nil)
			p.Update(tea.PasteMsg{Content: pasted})
			return p.FilterValue()
		},
		"connect api key input": func(t *testing.T) string {
			cfg := config.Default()
			cfg.Privacy.RemoteProvidersAllowed = true
			// The wizard reads its catalog from the host; this pick builds no
			// provider client, so the host needs no resolver.
			state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
			host, _ := inproc.New(inproc.Deps{State: state})
			m := connect.New(connect.Opts{Cfg: cfg, Host: host})
			m, _ = m.Update(picker.PickedMsg{Value: "openrouter"})
			m, _ = m.Update(tea.PasteMsg{Content: pasted})
			return m.InputValue()
		},
		"settings browser filter": func(t *testing.T) string {
			b := settings.NewBrowser(config.Default(), "")
			b.Update(tea.PasteMsg{Content: pasted})
			return b.FilterValue()
		},
		"settings field edit input": func(t *testing.T) string {
			st := settings.NewState(config.Default())
			fl := settings.FrameList(settings.SwarmFrame(st))
			settings.FieldListSetSize(fl, 60, 20)
			// Row 0 of the swarm frame is the scalar "Max fix rounds".
			settings.FieldListUpdate(fl, tea.KeyPressMsg{Code: tea.KeyEnter})
			settings.FieldListUpdate(fl, tea.PasteMsg{Content: pasted})
			return fl.InputValue()
		},
		"skills panel filter": func(t *testing.T) string {
			p := skills.NewPanel(t.TempDir(), t.TempDir(), false, nil, nil)
			p.Update(tea.PasteMsg{Content: pasted})
			return p.FilterValue()
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if got := run(t); !strings.Contains(got, pasted) {
				t.Errorf("input value after paste = %q, want it to contain %q", got, pasted)
			}
		})
	}
}
