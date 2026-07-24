package agents

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestRosterRendersPresetBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RolePlanner: {Preset: "reason"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "●") {
		t.Fatalf("preset binding should show ● glyph:\n%s", view)
	}
}

func TestRosterRendersCustomAgentBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleReviewer: {CustomAgent: "strict"},
		}},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "reason"},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "◆") {
		t.Fatalf("custom-agent binding should show ◆ glyph:\n%s", view)
	}
}

func TestRosterRendersFallbackGlyph(t *testing.T) {
	// When a role is unset and falls back to the implementer preset,
	// it resolves to a preset (● glyph). The ↩ glyph is for legacy
	// provider/model fallback.
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"impl": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "impl"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	// The planner role is unset and falls back to implementer → preset.
	if !containsGlyph(view, "●") {
		t.Fatalf("resolved role should show ● glyph:\n%s", view)
	}
}

func TestRosterRendersUnresolvedGlyph(t *testing.T) {
	// When no profile exists and no legacy config, roles show ⚠.
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{}
	cfg.Profile.Default = "nonexistent"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "⚠") {
		t.Fatalf("unresolved role should show ⚠ glyph:\n%s", view)
	}
}

func TestRosterFilterPreFill(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, "", "planner", nil)
	if p.FilterValue() != "planner" {
		t.Fatalf("filter = %q, want planner", p.FilterValue())
	}
}

func containsGlyph(s string, g string) bool {
	return strings.Contains(s, g)
}
