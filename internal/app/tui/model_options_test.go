package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/llm/routing"
)

func TestOptionsIdleChangePersistsAndReloads(t *testing.T) {
	reloads := 0
	m, workDir, homeDir := newModelForConfigTest(t, func(d *service.Deps) {
		d.ReloadConfig = func(cfg config.Config) error {
			reloads++
			d.State.Config = cfg
			return nil
		}
	})
	stateOf(m).SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	stateOf(m).Config.Models.Presets["coder"] = routing.ModelPreset{
		Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
		ContextWindow: 32000, MaxOutputTokens: 2048,
	}

	m2, settle := m.Update(modeloptions.ChangedMsg{
		Config: config.Config{
			Models: config.ModelsConfig{
				Presets: map[string]routing.ModelPreset{
					"coder": {
						Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
						ContextWindow: 64000, MaxOutputTokens: 4096,
					},
				},
			},
		},
		PresetName: "coder",
		FieldID:    "context_window",
		OldValue:   "32000",
		NewValue:   "64000",
	})
	m = asModel(t, m2)

	// The reload runs from the call queue, so the preset lands when its result
	// is handled.
	m = settleCalls(t, m, settle)
	if reloads != 1 {
		t.Fatalf("the runtime reload ran %d times, want 1", reloads)
	}
	if m.pendingModelOptions != nil {
		t.Fatal("pending state should be nil after idle commit")
	}
	if stateOf(m).Config.Models.Presets["coder"].ContextWindow != 64000 {
		t.Fatalf("live preset context window = %d, want 64000", stateOf(m).Config.Models.Presets["coder"].ContextWindow)
	}
	// Presets are user-global: the new value lands in the user config and
	// never in the project file.
	data, err := os.ReadFile(config.UserConfigPath(homeDir))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(data), "64000") {
		t.Fatalf("saved user config missing new value:\n%s", string(data))
	}
	if data, err := os.ReadFile(config.ProjectConfigPath(workDir)); err == nil && strings.Contains(string(data), "64000") {
		t.Fatalf("preset value leaked into project config:\n%s", string(data))
	}
}

func TestOptionsBusyChangeSavesWithoutReloading(t *testing.T) {
	reloads := 0
	m, workDir, homeDir := newModelForConfigTest(t, func(d *service.Deps) {
		d.ReloadConfig = func(config.Config) error { reloads++; return nil }
	})
	m.busy = true
	stateOf(m).SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	stateOf(m).Config.Models.Presets["coder"] = routing.ModelPreset{
		Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
		ContextWindow: 32000, MaxOutputTokens: 2048,
	}

	m2, _ := m.Update(modeloptions.ChangedMsg{
		Config: config.Config{
			Models: config.ModelsConfig{
				Presets: map[string]routing.ModelPreset{
					"coder": {
						Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
						ContextWindow: 64000, MaxOutputTokens: 4096,
					},
				},
			},
		},
		PresetName: "coder",
		FieldID:    "context_window",
		OldValue:   "32000",
		NewValue:   "64000",
	})
	m = asModel(t, m2)

	if reloads != 0 {
		t.Fatalf("the runtime reload ran %d times while busy, want 0", reloads)
	}
	if stateOf(m).Config.Models.Presets["coder"].ContextWindow != 32000 {
		t.Fatalf("live preset changed while busy: got %d, want 32000", stateOf(m).Config.Models.Presets["coder"].ContextWindow)
	}
	if m.pendingModelOptions == nil {
		msgs := stateOf(m).Messages()
		last := ""
		if len(msgs) > 0 {
			last = msgs[len(msgs)-1].Content
		}
		t.Fatalf("expected pending candidate after busy commit; last system message: %q", last)
	}
	data, err := os.ReadFile(config.UserConfigPath(homeDir))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(data), "64000") {
		t.Fatalf("saved user config missing candidate value:\n%s", string(data))
	}
	if data, err := os.ReadFile(config.ProjectConfigPath(workDir)); err == nil && strings.Contains(string(data), "64000") {
		t.Fatalf("preset value leaked into project config:\n%s", string(data))
	}
}

func TestOptionsPendingChangeFlushesWhenIdle(t *testing.T) {
	reloads := 0
	m, _, _ := newModelForConfigTest(t, func(d *service.Deps) {
		d.ReloadConfig = func(cfg config.Config) error {
			reloads++
			d.State.Config = cfg
			return nil
		}
	})
	m.busy = true
	stateOf(m).SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	stateOf(m).Config.Models.Presets["coder"] = routing.ModelPreset{
		Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
		ContextWindow: 32000, MaxOutputTokens: 2048,
	}

	m2, _ := m.Update(modeloptions.ChangedMsg{
		Config: config.Config{
			Models: config.ModelsConfig{
				Presets: map[string]routing.ModelPreset{
					"coder": {
						Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
						ContextWindow: 64000, MaxOutputTokens: 4096,
					},
				},
			},
		},
		PresetName: "coder",
		FieldID:    "context_window",
		OldValue:   "32000",
		NewValue:   "64000",
	})
	m = asModel(t, m2)

	m.busy = false
	// The turn-end change is what activates the candidate: a changedMsg lands in
	// handleChanged, which queues the reload. The pump command it also returns
	// blocks until the next signal, so it is dropped rather than run; the reload
	// it queued is settled below.
	m, _ = m.handleChanged(changedMsg{ch: m.sess.Changed()})
	m = settleCalls(t, m)
	if reloads != 1 {
		t.Fatalf("the runtime reload ran %d times on flush, want 1", reloads)
	}
	if m.pendingModelOptions != nil {
		t.Fatal("pending state should be cleared after flush")
	}
	if stateOf(m).Config.Models.Presets["coder"].ContextWindow != 64000 {
		t.Fatalf("live preset context window = %d, want 64000", stateOf(m).Config.Models.Presets["coder"].ContextWindow)
	}
}

func TestOptionsReloadFailureRetainsPendingCandidate(t *testing.T) {
	reloads := 0
	m, _, _ := newModelForConfigTest(t, func(d *service.Deps) {
		d.ReloadConfig = func(config.Config) error {
			reloads++
			return errors.New("reload failed")
		}
	})
	m.busy = true
	stateOf(m).SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	stateOf(m).Config.Models.Presets["coder"] = routing.ModelPreset{
		Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
		ContextWindow: 32000, MaxOutputTokens: 2048,
	}

	m2, _ := m.Update(modeloptions.ChangedMsg{
		Config: config.Config{
			Models: config.ModelsConfig{
				Presets: map[string]routing.ModelPreset{
					"coder": {
						Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b",
						ContextWindow: 64000, MaxOutputTokens: 4096,
					},
				},
			},
		},
		PresetName: "coder",
		FieldID:    "context_window",
		OldValue:   "32000",
		NewValue:   "64000",
	})
	m = asModel(t, m2)

	m.busy = false
	// The turn-end change is what activates the candidate; handleChanged queues
	// the reload, and its blocking pump command is dropped.
	m, _ = m.handleChanged(changedMsg{ch: m.sess.Changed()})
	m = settleCalls(t, m)
	if reloads != 1 {
		t.Fatalf("the runtime reload ran %d times, want 1", reloads)
	}
	if m.pendingModelOptions == nil {
		t.Fatal("pending candidate should be retained after reload failure")
	}
	if !m.pendingModelOptions.retry {
		t.Fatal("retry flag should be set after reload failure")
	}
	if stateOf(m).Config.Models.Presets["coder"].ContextWindow != 32000 {
		t.Fatalf("live preset should remain usable: got %d, want 32000", stateOf(m).Config.Models.Presets["coder"].ContextWindow)
	}
}
