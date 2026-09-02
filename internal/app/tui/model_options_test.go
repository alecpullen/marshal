package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/llm/routing"
)

func TestOptionsIdleChangePersistsAndReloads(t *testing.T) {
	m, workDir, homeDir := newModelForConfigTest(t)
	reloads := 0
	m.configReloader = func(cfg config.Config) error {
		reloads++
		m.state.Config = cfg
		return nil
	}
	m.state.SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	m.state.Config.Models.Presets["coder"] = routing.ModelPreset{
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

	if reloads != 1 {
		t.Fatalf("configReloader called %d times, want 1", reloads)
	}
	if m.pendingModelOptions != nil {
		t.Fatal("pending state should be nil after idle commit")
	}
	if m.state.Config.Models.Presets["coder"].ContextWindow != 64000 {
		t.Fatalf("live preset context window = %d, want 64000", m.state.Config.Models.Presets["coder"].ContextWindow)
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
	m, workDir, homeDir := newModelForConfigTest(t)
	reloads := 0
	m.configReloader = func(config.Config) error { reloads++; return nil }
	m.busy = true
	m.state.SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	m.state.Config.Models.Presets["coder"] = routing.ModelPreset{
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
		t.Fatalf("configReloader called %d times while busy, want 0", reloads)
	}
	if m.state.Config.Models.Presets["coder"].ContextWindow != 32000 {
		t.Fatalf("live preset changed while busy: got %d, want 32000", m.state.Config.Models.Presets["coder"].ContextWindow)
	}
	if m.pendingModelOptions == nil {
		msgs := m.state.Messages()
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
	m, _, _ := newModelForConfigTest(t)
	reloads := 0
	m.configReloader = func(cfg config.Config) error {
		reloads++
		m.state.Config = cfg
		return nil
	}
	m.busy = true
	m.state.SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	m.state.Config.Models.Presets["coder"] = routing.ModelPreset{
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
	m2, _ = m.handleJobCount(jobCountMsg{count: 0})
	m = asModel(t, m2)

	if reloads != 1 {
		t.Fatalf("configReloader called %d times on flush, want 1", reloads)
	}
	if m.pendingModelOptions != nil {
		t.Fatal("pending state should be cleared after flush")
	}
	if m.state.Config.Models.Presets["coder"].ContextWindow != 64000 {
		t.Fatalf("live preset context window = %d, want 64000", m.state.Config.Models.Presets["coder"].ContextWindow)
	}
}

func TestOptionsReloadFailureRetainsPendingCandidate(t *testing.T) {
	m, _, _ := newModelForConfigTest(t)
	reloads := 0
	m.configReloader = func(config.Config) error {
		reloads++
		return errors.New("reload failed")
	}
	m.busy = true
	m.state.SetActiveRoute(session.RouteInfo{
		Role: routing.RoleImplementer, Profile: "p", Preset: "coder", Active: true,
	})
	m.state.Config.Models.Presets["coder"] = routing.ModelPreset{
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
	m2, _ = m.handleJobCount(jobCountMsg{count: 0})
	m = asModel(t, m2)

	if reloads != 1 {
		t.Fatalf("configReloader called %d times, want 1", reloads)
	}
	if m.pendingModelOptions == nil {
		t.Fatal("pending candidate should be retained after reload failure")
	}
	if !m.pendingModelOptions.retry {
		t.Fatal("retry flag should be set after reload failure")
	}
	if m.state.Config.Models.Presets["coder"].ContextWindow != 32000 {
		t.Fatalf("live preset should remain usable: got %d, want 32000", m.state.Config.Models.Presets["coder"].ContextWindow)
	}
}
