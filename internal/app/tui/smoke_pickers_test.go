package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/llm/routing"
)

// makeSmokeModel builds a fresh Model with two user messages, two
// presets, and a command registry — enough to exercise every picker.
func makeSmokeModel(t *testing.T) Model {
	t.Helper()
	cfg := config.Default()
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	cfg.Models.Presets["qwen-local"] = routing.ModelPreset{
		Name: "qwen-local", Provider: "ollama", Model: "qwen2.5", LocalOnly: true,
	}
	cfg.Models.Presets["sonnet"] = routing.ModelPreset{
		Name: "sonnet", Provider: "anthropic", Model: "sonnet-5",
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first turn", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "second turn about parsing", session.ContentTypePlain)
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(120, 40)
	return m
}

// TestSmokePickersProgrammatic exercises the picker flows that Task 8
// Step 6 would cover manually: open each picker, render View(), confirm
// the output is well-formed, and confirm Esc / pick closes cleanly.
// Each subtest gets a fresh model because dispatchCommand has a pointer
// receiver and mutates state.
func TestSmokePickersProgrammatic(t *testing.T) {
	type tcase struct {
		name string
		cmd  string
		// expectPicker: do we expect a picker to be open after dispatch?
		expectPicker bool
	}
	cases := []tcase{
		{"/model bare opens picker", "/model", true},
		{"/model with prefilter opens picker", "/model qw", true},
		{"/model exact arg switches directly", "/model sonnet", false},
		{"/rewind opens picker", "/rewind", true},
		{"/branches with 1 leaf falls through", "/branches", false},
		{"/mode opens picker", "/mode", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := makeSmokeModel(t)
			updated, _ := m.dispatchCommand(tc.cmd)
			mm := *(updated.(*Model))
			if tc.expectPicker {
				if mm.pickerModel == nil {
					t.Fatalf("%s: expected picker to open", tc.name)
				}
				view := mm.View().Content
				if !strings.Contains(view, "╭") || !strings.Contains(view, "╯") {
					t.Fatalf("%s: View should render a chrome.Panel (missing border chars):\n%s", tc.name, view)
				}
				// Esc should close cleanly
				upd, cmd := mm.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				mm2 := upd.(Model)
				if cmd == nil {
					t.Fatalf("%s: Esc should emit a cancel command", tc.name)
				}
				upd, _ = mm2.Update(cmd())
				mm3 := upd.(Model)
				if mm3.pickerModel != nil {
					t.Fatalf("%s: CancelledMsg should close the picker", tc.name)
				}
			} else {
				if mm.pickerModel != nil {
					t.Fatalf("%s: picker must not open", tc.name)
				}
			}
		})
	}

	// /model with no presets should print a /settings hint, no picker.
	t.Run("/model with no presets points at /settings", func(t *testing.T) {
		emptyCfg := config.Default()
		emptyCfg.Models.Presets = map[string]routing.ModelPreset{}
		emptyState := session.New(emptyCfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		emptyReg := commands.New()
		if err := commands.RegisterAll(emptyReg, nil); err != nil {
			t.Fatalf("RegisterAll: %v", err)
		}
		mm := New(emptyState, WithCommandRegistry(emptyReg))
		mm.resize(120, 40)
		updated, _ := mm.dispatchCommand("/model")
		mm2 := *(updated.(*Model))
		if mm2.pickerModel != nil {
			t.Fatal("no presets: picker must not open")
		}
		msgs := emptyState.Messages()
		if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "/settings") {
			t.Fatal("should add a system message pointing at /settings")
		}
	})

	// /branches with 2+ leaves should open a picker.
	t.Run("/branches with 2+ leaves opens picker", func(t *testing.T) {
		m := makeSmokeModel(t)
		// Create a second branch by rewinding to before the last user turn.
		m.state.Rewind(1)
		m.state.AddMessage(session.RoleUser, "alternate third turn", session.ContentTypePlain)
		updated, _ := m.dispatchCommand("/branches")
		mm := *(updated.(*Model))
		if mm.pickerModel == nil {
			t.Fatal("2 branches: picker should open")
		}
	})
}
