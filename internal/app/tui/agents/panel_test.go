package agents

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/settings"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
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

func TestCustomAgentDrillEditsPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Provider: "ollama", Model: "qwen"}}
	cfg.CustomAgents = map[string]routing.CustomAgent{"my-scout": {Name: "my-scout", Preset: "fast"}}
	p := NewRosterPanel(cfg, "", "", nil)
	// Build the custom-agent frame and verify the preset field reads "fast".
	frame := p.customAgentFrame("my-scout")
	fields := settings.FrameList(frame).Rows()
	var presetField *settings.Field
	for _, f := range fields {
		if settings.FieldID(f) == "roster.ca.my-scout.preset" {
			presetField = f
			break
		}
	}
	if presetField == nil {
		t.Fatal("preset field not found in custom agent frame")
	}
	if settings.FieldGetStr(presetField) != "fast" {
		t.Fatalf("preset = %q, want fast", settings.FieldGetStr(presetField))
	}
}

func TestToolDenylistValidation(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"x": {Name: "x", Preset: "p"}}
	p := NewRosterPanel(cfg, "", "", nil)
	// validateToolDenylist checks names against a live registry.
	// With no registry passed, it should still validate (empty registry).
	err := p.validateToolDenylist([]string{"nonexistent_tool"})
	if err == nil {
		t.Fatal("invalid tool name should fail validation")
	}
	// With a registry that has the tool, it should pass.
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}})
	p2 := NewRosterPanelWithRegistry(cfg, "", "", nil, reg)
	err2 := p2.validateToolDenylist([]string{"file.read"})
	if err2 != nil {
		t.Fatalf("valid tool name should pass validation: %v", err2)
	}
	err3 := p2.validateToolDenylist([]string{"file.read", "nonexistent"})
	if err3 == nil {
		t.Fatal("mix of valid and invalid should fail")
	}
}

func containsGlyph(s string, g string) bool {
	return strings.Contains(s, g)
}
