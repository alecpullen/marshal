package routing

import "testing"

func TestCustomAgentSamplingOverrides(t *testing.T) {
	presetTemp := 0.2
	agentTemp := 0.7
	cfg := Config{
		DefaultProfile:   "p",
		ProviderBaseURLs: map[string]string{"ollama": "http://localhost:11434"},
		RemoteAllowed:    false,
		Presets: map[string]ModelPreset{
			"ollama/qwen": {Provider: "ollama", Model: "qwen", LocalOnly: true, Temperature: &presetTemp, Thinking: "high"},
		},
		Profiles: map[string]AgentProfile{
			"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
				RoleImplementer: {Preset: "ollama/qwen"},
			}},
		},
		CustomAgents: map[string]CustomAgent{
			"overridden": {Preset: "ollama/qwen", Temperature: &agentTemp, Thinking: "low"},
			"inherits":   {Preset: "ollama/qwen"},
		},
	}
	r := NewStaticRouter(cfg)

	// ResolveCustomAgent: explicit overrides win.
	route, err := r.ResolveCustomAgent("overridden", RoleSubtask)
	if err != nil {
		t.Fatalf("ResolveCustomAgent(overridden): %v", err)
	}
	if route.Preset.Temperature == nil || *route.Preset.Temperature != 0.7 {
		t.Fatalf("overridden temperature = %v, want 0.7", route.Preset.Temperature)
	}
	if route.Preset.Thinking != "low" {
		t.Fatalf("overridden thinking = %q, want low", route.Preset.Thinking)
	}

	// ResolveCustomAgent: nil/"" leaves the preset's values untouched.
	route, err = r.ResolveCustomAgent("inherits", RoleSubtask)
	if err != nil {
		t.Fatalf("ResolveCustomAgent(inherits): %v", err)
	}
	if route.Preset.Temperature == nil || *route.Preset.Temperature != 0.2 {
		t.Fatalf("inherits temperature = %v, want preset 0.2", route.Preset.Temperature)
	}
	if route.Preset.Thinking != "high" {
		t.Fatalf("inherits thinking = %q, want preset high", route.Preset.Thinking)
	}

	// resolveAgentBinding (unexported, same package): same precedence.
	route, err = r.resolveAgentBinding("overridden", RoleImplementer, "p")
	if err != nil {
		t.Fatalf("resolveAgentBinding(overridden): %v", err)
	}
	if route.Preset.Temperature == nil || *route.Preset.Temperature != 0.7 {
		t.Fatalf("binding overridden temperature = %v, want 0.7", route.Preset.Temperature)
	}
	if route.Preset.Thinking != "low" {
		t.Fatalf("binding overridden thinking = %q, want low", route.Preset.Thinking)
	}

	route, err = r.resolveAgentBinding("inherits", RoleImplementer, "p")
	if err != nil {
		t.Fatalf("resolveAgentBinding(inherits): %v", err)
	}
	if route.Preset.Temperature == nil || *route.Preset.Temperature != 0.2 {
		t.Fatalf("binding inherits temperature = %v, want preset 0.2", route.Preset.Temperature)
	}
	if route.Preset.Thinking != "high" {
		t.Fatalf("binding inherits thinking = %q, want preset high", route.Preset.Thinking)
	}
}
