package routing

import "testing"

func TestSingleModelProfileBindsEveryRole(t *testing.T) {
	p := SingleModelProfile("single", "fast")

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
		if b.Preset != "fast" {
			t.Errorf("role %q bound to %q, want %q", role, b.Preset, "fast")
		}
		if b.CustomAgent != "" {
			t.Errorf("role %q has CustomAgent %q, want empty", role, b.CustomAgent)
		}
	}
}

func TestSingleModelProfileExcludesEmbedding(t *testing.T) {
	p := SingleModelProfile("single", "fast")
	if _, ok := p.Roles[RoleEmbedding]; ok {
		t.Error("embedding must not be bound to a chat preset — a chat model cannot produce vectors")
	}
}

func TestSingleModelProfileResolvesThroughTheRouter(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "single",
		RemoteAllowed:  true,
		Presets: map[string]ModelPreset{
			"fast": {Name: "fast", Provider: "openai", Model: "gpt-4o"},
		},
		Profiles: map[string]AgentProfile{"single": SingleModelProfile("single", "fast")},
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
