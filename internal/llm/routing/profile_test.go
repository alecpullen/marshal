package routing

import "testing"

func TestSingleModelProfileBindsEveryRole(t *testing.T) {
	p := SingleModelProfile("single", "openai/gpt-4o")

	if p.Name != "single" {
		t.Errorf("Name = %q, want %q", p.Name, "single")
	}
	if len(p.Roles) != len(AllRoles) {
		t.Fatalf("bound %d roles, want %d", len(p.Roles), len(AllRoles))
	}
	for _, role := range AllRoles {
		b, ok := p.Roles[role]
		if !ok {
			t.Errorf("role %q unbound", role)
			continue
		}
		if b.Preset != "openai/gpt-4o" {
			t.Errorf("role %q bound to %q, want %q", role, b.Preset, "openai/gpt-4o")
		}
		if b.CustomAgent != "" {
			t.Errorf("role %q has CustomAgent %q, want empty", role, b.CustomAgent)
		}
	}
}

func TestSingleModelProfileExcludesEmbedding(t *testing.T) {
	p := SingleModelProfile("single", "openai/gpt-4o")
	if _, ok := p.Roles[RoleEmbedding]; ok {
		t.Error("embedding must not be bound to a chat preset — a chat model cannot produce vectors")
	}
}

func TestSingleModelProfileResolvesThroughTheRouter(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "single",
		RemoteAllowed:  true,
		Presets: map[string]ModelPreset{
			"openai/gpt-4o": {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o"},
		},
		Profiles: map[string]AgentProfile{"single": SingleModelProfile("single", "openai/gpt-4o")},
	})
	for _, role := range AllRoles {
		route, err := r.ResolveRole(role)
		if err != nil {
			t.Errorf("ResolveRole(%q) error = %v", role, err)
			continue
		}
		if route.Preset.Model != "gpt-4o" {
			t.Errorf("role %q resolved to %q, want gpt-4o", role, route.Preset.Model)
		}
	}
}

func TestPresetRoleBindingsInvertsProfiles(t *testing.T) {
	cfg := Config{
		Profiles: map[string]AgentProfile{
			"single": {Name: "single", Roles: map[AgentRole]RoleBinding{
				RoleImplementer: {Preset: "ollama/qwen2.5-coder:14b"},
				RoleReviewer:    {Preset: "ollama/qwen2.5-coder:14b"},
				RoleRepoScout:   {Preset: "ollama/qwen2.5-coder:7b"},
			}},
		},
	}
	got, names := PresetRoleBindingsSorted(cfg)
	if len(names) != 2 || names[0] != "ollama/qwen2.5-coder:14b" || names[1] != "ollama/qwen2.5-coder:7b" {
		t.Fatalf("preset keys = %v, want sorted [14b, 7b]", names)
	}
	reviewerBinds := got["ollama/qwen2.5-coder:14b"]
	if len(reviewerBinds) != 1 || reviewerBinds[0].Profile != "single" {
		t.Fatalf("bindings for 14b = %+v", reviewerBinds)
	}
	if got["ollama/qwen2.5-coder:7b"][0].Roles[0] != RoleRepoScout {
		t.Fatalf("bindings for 7b = %+v", got["ollama/qwen2.5-coder:7b"])
	}
}

func TestPresetRoleBindingsAttributionNotInheritance(t *testing.T) {
	cfg := Config{
		Profiles: map[string]AgentProfile{
			"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
				RoleImplementer: {Preset: "base"},
			}},
		},
	}
	got := PresetRoleBindings(cfg)
	roles := got["base"][0].Roles
	if len(roles) != 1 || roles[0] != RoleImplementer {
		t.Fatalf("inherited roles leaked into attribution: %+v", roles)
	}
}
