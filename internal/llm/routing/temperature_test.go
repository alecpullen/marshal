package routing

import "testing"

func TestRoleDefaultTemperature(t *testing.T) {
	cases := []struct {
		role AgentRole
		want *float64
	}{
		{RoleImplementer, floatPtr(0.2)},
		{RoleSDDImplementer, floatPtr(0.2)},
		{RoleReviewer, floatPtr(0.1)},
		{RoleSecurityReviewer, floatPtr(0.1)},
		{RoleSDDReviewer, floatPtr(0.1)},
		{RoleSDDBranchReviewer, floatPtr(0.1)},
		{RoleTester, floatPtr(0.1)},
		{RolePlanner, nil},
		{RoleRepoScout, nil},
		{RoleRouter, nil},
		{RoleKnowledge, nil},
	}
	for _, c := range cases {
		got := RoleDefaultTemperature(c.role)
		if c.want == nil {
			if got != nil {
				t.Errorf("RoleDefaultTemperature(%s) = %v, want nil", c.role, *got)
			}
			continue
		}
		if got == nil || *got != *c.want {
			t.Errorf("RoleDefaultTemperature(%s) = %v, want %v", c.role, got, *c.want)
		}
	}
}

func TestResolveRoleAppliesDefaultTemperature(t *testing.T) {
	cfg := Config{
		DefaultProfile:   "p",
		ProviderBaseURLs: map[string]string{"ollama": "http://localhost:11434"},
		RemoteAllowed:    false,
		Presets: map[string]ModelPreset{
			"ollama/qwen": {Provider: "ollama", Model: "qwen", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
				RoleImplementer: {Preset: "ollama/qwen"},
				RolePlanner:     {Preset: "ollama/qwen"},
			}},
		},
	}
	r := NewStaticRouter(cfg)

	impl, err := r.ResolveRole(RoleImplementer)
	if err != nil {
		t.Fatalf("ResolveRole(implementer): %v", err)
	}
	if impl.Preset.Temperature == nil || *impl.Preset.Temperature != 0.2 {
		t.Fatalf("implementer temperature = %v, want 0.2", impl.Preset.Temperature)
	}

	planner, err := r.ResolveRole(RolePlanner)
	if err != nil {
		t.Fatalf("ResolveRole(planner): %v", err)
	}
	if planner.Preset.Temperature != nil {
		t.Fatalf("planner temperature = %v, want nil", *planner.Preset.Temperature)
	}
}

func TestResolveRolePreservesPresetTemperature(t *testing.T) {
	custom := 0.55
	cfg := Config{
		DefaultProfile:   "p",
		ProviderBaseURLs: map[string]string{"ollama": "http://localhost:11434"},
		Presets: map[string]ModelPreset{
			"ollama/qwen": {Provider: "ollama", Model: "qwen", LocalOnly: true, Temperature: &custom},
		},
		Profiles: map[string]AgentProfile{
			"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
				RoleImplementer: {Preset: "ollama/qwen"},
			}},
		},
	}
	r := NewStaticRouter(cfg)
	route, err := r.ResolveRole(RoleImplementer)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if route.Preset.Temperature == nil || *route.Preset.Temperature != 0.55 {
		t.Fatalf("temperature = %v, want preset's 0.55 (default must not override)", route.Preset.Temperature)
	}
}
