package textfield_test

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/agents"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/db"
)

// TestPasteReachesEveryTextInput enumerates every TUI component that owns a
// text field, sends it a tea.PasteMsg, and asserts the pasted text landed in
// the field's value. The table lives in one shared file on purpose: when an
// eighth input-owning component is added, add a row here and it fails until
// paste reaches its input.
func TestPasteReachesEveryTextInput(t *testing.T) {
	const pasted = "pasted-text-xyz"

	cases := map[string]func(t *testing.T) string{
		"picker filter": func(t *testing.T) string {
			m := picker.New("Pick", "", []picker.Item{{Label: "one"}})
			m.Update(tea.PasteMsg{Content: pasted})
			return m.FilterValue()
		},
		"memory filter": func(t *testing.T) string {
			database, projectID := newPasteTestDB(t)
			p := memory.NewPanel(database, projectID)
			p.Update(tea.PasteMsg{Content: pasted})
			return p.FilterValue()
		},
		"agents roster filter": func(t *testing.T) string {
			p := agents.NewRosterPanel(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "", nil)
			p.Update(tea.PasteMsg{Content: pasted})
			return p.FilterValue()
		},
		"connect api key input": func(t *testing.T) string {
			cfg := config.Default()
			cfg.Privacy.RemoteProvidersAllowed = true
			m := connect.New(connect.Opts{Cfg: cfg})
			m, _ = m.Update(picker.PickedMsg{Value: "openrouter"})
			m, _ = m.Update(tea.PasteMsg{Content: pasted})
			return m.InputValue()
		},
		"settings browser filter": func(t *testing.T) string {
			b := settings.NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
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
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if got := run(t); !strings.Contains(got, pasted) {
				t.Errorf("input value after paste = %q, want it to contain %q", got, pasted)
			}
		})
	}
}

func newPasteTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}
