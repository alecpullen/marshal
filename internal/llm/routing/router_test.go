package routing

import (
	"errors"
	"strings"
	"testing"
)

func testRouter() *StaticRouter {
	return NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"fast": {
				Name:      "fast",
				Provider:  "ollama",
				Model:     "qwen2.5-coder:7b",
				LocalOnly: true,
			},
			"coder": {
				Name:      "coder",
				Provider:  "ollama",
				Model:     "qwen2.5-coder:14b",
				LocalOnly: true,
			},
			"remote": {
				Name:      "remote",
				Provider:  "openrouter",
				Model:     "anthropic/claude-sonnet-4",
				LocalOnly: false,
			},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleRepoScout:   {Preset: "fast"},
					RoleImplementer: {Preset: "coder"},
				},
			},
		},
		ContextBudgets: map[AgentRole]ContextBudget{
			RoleImplementer: {MaxRepoContextTokens: 48000},
		},
		LegacyProvider: "legacy-provider",
		LegacyModel:    "legacy-model",
	})
}

func TestRoleBindingUnmarshalBareString(t *testing.T) {
	// Test UnmarshalTOML directly with a string value (the path go-toml
	// takes when decoding inline tables and dotted tables).
	var b RoleBinding
	if err := b.UnmarshalTOML("reasoning"); err != nil {
		t.Fatalf("bare string unmarshal: %v", err)
	}
	if b.Preset != "reasoning" || b.CustomAgent != "" {
		t.Fatalf("got %+v, want Preset=reasoning", b)
	}
}

func TestRoleBindingUnmarshalTableCustomAgent(t *testing.T) {
	// Test UnmarshalTOML with a map (the path go-toml takes for
	// inline tables like `custom_agent = "my-reviewer"`).
	var b RoleBinding
	if err := b.UnmarshalTOML(map[string]any{"custom_agent": "my-reviewer"}); err != nil {
		t.Fatalf("table unmarshal: %v", err)
	}
	if b.CustomAgent != "my-reviewer" || b.Preset != "" {
		t.Fatalf("got %+v, want CustomAgent=my-reviewer", b)
	}
}

func TestRoleBindingUnmarshalTablePreset(t *testing.T) {
	// Test UnmarshalTOML with a map containing preset.
	var b RoleBinding
	if err := b.UnmarshalTOML(map[string]any{"preset": "reasoning"}); err != nil {
		t.Fatalf("table unmarshal: %v", err)
	}
	if b.Preset != "reasoning" || b.CustomAgent != "" {
		t.Fatalf("got %+v, want Preset=reasoning", b)
	}
}

func TestResolveQuestionUsesRepoScout(t *testing.T) {
	route, err := testRouter().Resolve("question")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleRepoScout {
		t.Fatalf("Role = %q, want %q", route.Role, RoleRepoScout)
	}
	if route.Preset.Name != "fast" || route.Preset.Model != "qwen2.5-coder:7b" {
		t.Fatalf("Preset = %#v, want fast qwen2.5-coder:7b", route.Preset)
	}
	if route.Legacy {
		t.Fatal("Legacy = true, want false")
	}
}

func TestResolveEditUsesImplementerAndBudget(t *testing.T) {
	route, err := testRouter().Resolve("edit")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want %q", route.Role, RoleImplementer)
	}
	if route.ContextBudget.MaxRepoContextTokens != 48000 {
		t.Fatalf("ContextBudget = %#v", route.ContextBudget)
	}
}

func TestResolveFallsBackToImplementerForMissingRole(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Presets: map[string]ModelPreset{
			"coder": {Name: "coder", Provider: "ollama", Model: "coder", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleImplementer: {Preset: "coder"},
				},
			},
		},
	})
	route, err := router.Resolve("question")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want implementer fallback", route.Role)
	}
}

func TestResolveQuestionMissingRepoScoutPresetDoesNotFallBackToImplementer(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Presets: map[string]ModelPreset{
			"coder": {Name: "coder", Provider: "ollama", Model: "coder", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleRepoScout:   {Preset: "missing"},
					RoleImplementer: {Preset: "coder"},
				},
			},
		},
	})
	_, err := router.Resolve("question")
	if !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("err = %v, want ErrPresetNotFound", err)
	}
}

func TestResolveQuestionRemoteBlockedDoesNotFallBackToImplementer(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"remote": {Name: "remote", Provider: "openrouter", Model: "remote-model", LocalOnly: false},
			"coder":  {Name: "coder", Provider: "ollama", Model: "coder", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleRepoScout:   {Preset: "remote"},
					RoleImplementer: {Preset: "coder"},
				},
			},
		},
	})
	_, err := router.Resolve("question")
	if !errors.Is(err, ErrRemoteProviderBlocked) {
		t.Fatalf("err = %v, want ErrRemoteProviderBlocked", err)
	}
}

func TestResolveUsesLegacyWhenNoProfileRouteExists(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "missing",
		LegacyProvider: "ollama",
		LegacyModel:    "qwen2.5-coder:14b",
	})
	route, err := router.Resolve("question")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !route.Legacy {
		t.Fatal("Legacy = false, want true")
	}
	if route.Preset.Provider != "ollama" || route.Preset.Model != "qwen2.5-coder:14b" {
		t.Fatalf("legacy route preset = %#v", route.Preset)
	}
	if route.Preset.LocalOnly {
		t.Fatal("legacy preset LocalOnly = true, want false")
	}
}

func TestResolveMissingProfileWithoutLegacyReturnsError(t *testing.T) {
	_, err := NewStaticRouter(Config{DefaultProfile: "missing"}).Resolve("question")
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
}

func TestResolveMissingPresetReturnsError(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Profiles: map[string]AgentProfile{
			"local_balanced": {Name: "local_balanced", Roles: map[AgentRole]RoleBinding{RoleImplementer: {Preset: "missing"}}},
		},
	})
	_, err := router.Resolve("edit")
	if !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("err = %v, want ErrPresetNotFound", err)
	}
}

func TestResolveBlocksRemotePresetWhenRemoteDisabled(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "remote_profile",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"remote": {Name: "remote", Provider: "openrouter", Model: "model", LocalOnly: false},
		},
		Profiles: map[string]AgentProfile{
			"remote_profile": {Name: "remote_profile", Roles: map[AgentRole]RoleBinding{RoleImplementer: {Preset: "remote"}}},
		},
	})
	_, err := router.Resolve("edit")
	if !errors.Is(err, ErrRemoteProviderBlocked) {
		t.Fatalf("err = %v, want ErrRemoteProviderBlocked", err)
	}
}

func TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"tiny": {Name: "tiny", Provider: "ollama", Model: "qwen2.5:0.5b", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleKnowledge: {Preset: "tiny"},
				},
			},
		},
	})

	route, err := router.Resolve("knowledge")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleKnowledge {
		t.Fatalf("Role = %q, want %q", route.Role, RoleKnowledge)
	}
	if route.Preset.Name != "tiny" {
		t.Fatalf("Preset = %#v, want tiny", route.Preset)
	}
}

func TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured(t *testing.T) {
	route, err := testRouter().Resolve("knowledge")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want %q (fallback)", route.Role, RoleImplementer)
	}
	if route.Preset.Name != "coder" {
		t.Fatalf("Preset = %#v, want coder", route.Preset)
	}
}

func TestResolveRoleReturnsConfiguredPreset(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "default",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"local-small": {Provider: "ollama", Model: "small", LocalOnly: true},
			"local-big":   {Provider: "ollama", Model: "big", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"default": {
				Name: "default",
				Roles: map[AgentRole]RoleBinding{
					RolePlanner:     {Preset: "local-big"},
					RoleImplementer: {Preset: "local-small"},
				},
			},
		},
	})

	route, err := router.ResolveRole(RolePlanner)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if route.Role != RolePlanner || route.Preset.Model != "big" {
		t.Fatalf("route = %+v, want planner on model big", route)
	}
}

func TestResolveSDDRolesFallBackToImplementer(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Presets: map[string]ModelPreset{
			"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]RoleBinding{
					RoleImplementer: {Preset: "coder"},
				},
			},
		},
	})
	for _, role := range []AgentRole{RoleSDDImplementer, RoleSDDReviewer, RoleSDDBranchReviewer} {
		route, err := router.ResolveRole(role)
		if err != nil {
			t.Fatalf("ResolveRole(%s): %v", role, err)
		}
		if route.Preset.Name != "coder" {
			t.Errorf("ResolveRole(%s) preset = %q, want coder (fallback)", role, route.Preset.Name)
		}
	}
}

func TestResolveRoleFallsBackToImplementerForUnconfiguredRole(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "default",
		Presets: map[string]ModelPreset{
			"local-small": {Provider: "ollama", Model: "small", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"default": {
				Name:  "default",
				Roles: map[AgentRole]RoleBinding{RoleImplementer: {Preset: "local-small"}},
			},
		},
	})

	route, err := router.ResolveRole(RoleRepoScout)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if route.Preset.Model != "small" {
		t.Fatalf("route.Preset.Model = %q, want implementer fallback \"small\"", route.Preset.Model)
	}
}

func TestLegacyRouteHasSaneDefaults(t *testing.T) {
	// A router with only LegacyProvider/LegacyModel set should return a
	// legacy route with non-zero ContextBudget values.
	router := NewStaticRouter(Config{
		DefaultProfile: "missing",
		LegacyProvider: "ollama",
		LegacyModel:    "qwen2.5-coder:7b",
	})
	route, err := router.Resolve("edit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !route.Legacy {
		t.Fatal("expected legacy route")
	}
	if route.ContextBudget.MaxRepoContextTokens == 0 {
		t.Fatal("legacy route ContextBudget.MaxRepoContextTokens is 0, want > 0")
	}
}

func TestCastResolvesEveryRequestedRole(t *testing.T) {
	router := testRouter()

	t.Run("SwarmCastRoles", func(t *testing.T) {
		entries := router.Cast(SwarmCastRoles)
		if len(entries) != len(SwarmCastRoles) {
			t.Fatalf("got %d entries, want %d", len(entries), len(SwarmCastRoles))
		}
		for i, entry := range entries {
			if entry.Role != SwarmCastRoles[i] {
				t.Errorf("entry[%d].Role = %q, want %q", i, entry.Role, SwarmCastRoles[i])
			}
			// Every swarm role should resolve without error in testRouter
			// (RolePlanner and RoleReviewer fall back to implementer).
			if entry.Err != nil {
				t.Errorf("entry[%d] (%s): unexpected error: %v", i, entry.Role, entry.Err)
			}
			if entry.Route.Role == "" {
				t.Errorf("entry[%d] (%s): empty Route", i, entry.Role)
			}
		}
	})

	t.Run("SDDCastRoles", func(t *testing.T) {
		entries := router.Cast(SDDCastRoles)
		if len(entries) != len(SDDCastRoles) {
			t.Fatalf("got %d entries, want %d", len(entries), len(SDDCastRoles))
		}
		for i, entry := range entries {
			if entry.Role != SDDCastRoles[i] {
				t.Errorf("entry[%d].Role = %q, want %q", i, entry.Role, SDDCastRoles[i])
			}
			// SDD roles fall back to implementer in testRouter.
			if entry.Err != nil {
				t.Errorf("entry[%d] (%s): unexpected error: %v", i, entry.Role, entry.Err)
			}
			if entry.Route.Role == "" {
				t.Errorf("entry[%d] (%s): empty Route", i, entry.Role)
			}
		}
	})

	t.Run("never_short_circuits_on_errors", func(t *testing.T) {
		// A router with no profiles at all — every role should error,
		// but Cast should still return one entry per role.
		empty := NewStaticRouter(Config{DefaultProfile: "missing"})
		roles := []AgentRole{RolePlanner, RoleImplementer, RoleSDDImplementer}
		entries := empty.Cast(roles)
		if len(entries) != len(roles) {
			t.Fatalf("got %d entries, want %d", len(entries), len(roles))
		}
		for i, entry := range entries {
			if entry.Role != roles[i] {
				t.Errorf("entry[%d].Role = %q, want %q", i, entry.Role, roles[i])
			}
			if entry.Err == nil {
				t.Errorf("entry[%d] (%s): expected error, got nil", i, entry.Role)
			}
		}
	})
}

func TestLegacyRouteBlockedWhenRemoteNotAllowed(t *testing.T) {
	r := NewStaticRouter(Config{
		RemoteAllowed:  false,
		LegacyProvider: "https://api.openai.com/v1",
		LegacyModel:    "gpt-4o",
	})
	route, ok := r.legacyRoute(RoleImplementer)
	if ok {
		t.Fatalf("expected legacyRoute to be blocked; got %+v", route)
	}
}

func TestLegacyRouteAllowedWhenLocal(t *testing.T) {
	r := NewStaticRouter(Config{
		RemoteAllowed:  false,
		LegacyProvider: "http://localhost:11434/v1",
		LegacyModel:    "qwen2.5-coder:7b",
	})
	route, ok := r.legacyRoute(RoleImplementer)
	if !ok {
		t.Fatalf("expected legacyRoute to allow localhost; got ok=false")
	}
	if route.Preset.Provider != "http://localhost:11434/v1" {
		t.Fatalf("wrong provider: %q", route.Preset.Provider)
	}
}

func TestResolveRoleReturnsFallbackErrorOnExhaustion(t *testing.T) {
	// Both repo_scout and implementer are unconfigured in the profile,
	// and no legacy provider exists. The returned error should reference
	// the implementer role (the fallback error) rather than the primary
	// role (repo_scout), because the fallback error is more informative
	// (it tells the caller which fallback role also wasn't found).
	router := NewStaticRouter(Config{
		DefaultProfile: "default",
		Profiles: map[string]AgentProfile{
			"default": {
				Name:  "default",
				Roles: map[AgentRole]RoleBinding{}, // no roles at all
			},
		},
	})

	_, err := router.ResolveRole(RoleRepoScout)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// With the old code, the returned error mentions "repo_scout" (the
	// primary role). With the fix, it should mention "implementer" (the
	// fallback role) because that is the more specific error.
	if !strings.Contains(err.Error(), "implementer") {
		t.Fatalf("returned error should reference implementer (fallback), got: %v", err)
	}
}

func TestResolveCustomAgentWithPreset(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"fast": {Provider: "ollama", Model: "qwen", LocalOnly: true}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleImplementer: {Preset: "fast"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-scout": {Name: "my-scout", Preset: "fast", SystemPrompt: "be fast"},
		},
	})
	route, err := r.ResolveCustomAgent("my-scout", RoleSubtask)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.Preset.Model != "qwen" {
		t.Fatalf("model = %s, want qwen", route.Preset.Model)
	}
	if route.CustomAgent == nil || route.CustomAgent.SystemPrompt != "be fast" {
		t.Fatalf("CustomAgent not attached: %+v", route.CustomAgent)
	}
}

func TestResolveCustomAgentFallsBackToRole(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"impl": {Provider: "ollama", Model: "qwen", LocalOnly: true}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleImplementer: {Preset: "impl"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-scout": {Name: "my-scout", Preset: ""}, // no preset, no role bound
		},
	})
	route, err := r.ResolveCustomAgent("my-scout", RoleSubtask)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.Preset.Model != "qwen" {
		t.Fatalf("fallback model = %s, want qwen (implementer)", route.Preset.Model)
	}
	if route.CustomAgent == nil || route.CustomAgent.Name != "my-scout" {
		t.Fatalf("CustomAgent not attached")
	}
}

func TestResolveCustomAgentMissing(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Profiles:       map[string]AgentProfile{"p": {Name: "p"}},
		CustomAgents:   map[string]CustomAgent{},
	})
	if _, err := r.ResolveCustomAgent("ghost", RoleSubtask); err == nil {
		t.Fatal("want error for missing agent")
	}
}

func TestResolveRoleCustomAgentBinding(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"reason": {Provider: "ollama", Model: "qwen-reason", LocalOnly: true}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleReviewer:    {CustomAgent: "my-reviewer"},
			RoleImplementer: {Preset: "reason"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-reviewer": {Name: "my-reviewer", Preset: "reason", SystemPrompt: "strict"},
		},
	})
	route, err := r.ResolveRole(RoleReviewer)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.CustomAgent == nil || route.CustomAgent.Name != "my-reviewer" {
		t.Fatalf("reviewer not bound to custom agent: %+v", route.CustomAgent)
	}
	if route.Preset.Model != "qwen-reason" {
		t.Fatalf("preset = %s, want qwen-reason", route.Preset.Model)
	}
}

func TestResolveRolePresetBindingUnchanged(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"reason": {Provider: "ollama", Model: "qwen-reason", LocalOnly: true}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleReviewer: {Preset: "reason"},
		}}},
	})
	route, err := r.ResolveRole(RoleReviewer)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.CustomAgent != nil {
		t.Fatalf("preset binding should not attach CustomAgent: %+v", route.CustomAgent)
	}
	if route.Preset.Model != "qwen-reason" {
		t.Fatalf("preset = %s, want qwen-reason", route.Preset.Model)
	}
}
