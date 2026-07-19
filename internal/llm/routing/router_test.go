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
				Roles: map[AgentRole]string{
					RoleRepoScout:   "fast",
					RoleImplementer: "coder",
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

func TestResolveQuestionUsesRepoScout(t *testing.T) {
	route, err := testRouter().Resolve(TaskProfile{Class: "question"})
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
	route, err := testRouter().Resolve(TaskProfile{Class: "edit"})
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
				Roles: map[AgentRole]string{
					RoleImplementer: "coder",
				},
			},
		},
	})
	route, err := router.Resolve(TaskProfile{Class: "question"})
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
				Roles: map[AgentRole]string{
					RoleRepoScout:   "missing",
					RoleImplementer: "coder",
				},
			},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "question"})
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
				Roles: map[AgentRole]string{
					RoleRepoScout:   "remote",
					RoleImplementer: "coder",
				},
			},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "question"})
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
	route, err := router.Resolve(TaskProfile{Class: "question"})
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
	_, err := NewStaticRouter(Config{DefaultProfile: "missing"}).Resolve(TaskProfile{Class: "question"})
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
}

func TestResolveMissingPresetReturnsError(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Profiles: map[string]AgentProfile{
			"local_balanced": {Name: "local_balanced", Roles: map[AgentRole]string{RoleImplementer: "missing"}},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "edit"})
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
			"remote_profile": {Name: "remote_profile", Roles: map[AgentRole]string{RoleImplementer: "remote"}},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "edit"})
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
				Roles: map[AgentRole]string{
					RoleKnowledge: "tiny",
				},
			},
		},
	})

	route, err := router.Resolve(TaskProfile{Class: "knowledge"})
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
	route, err := testRouter().Resolve(TaskProfile{Class: "knowledge"})
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
				Roles: map[AgentRole]string{
					RolePlanner:     "local-big",
					RoleImplementer: "local-small",
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
				Roles: map[AgentRole]string{
					RoleImplementer: "coder",
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
				Roles: map[AgentRole]string{RoleImplementer: "local-small"},
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
	route, err := router.Resolve(TaskProfile{Class: "edit"})
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
				Roles: map[AgentRole]string{}, // no roles at all
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
