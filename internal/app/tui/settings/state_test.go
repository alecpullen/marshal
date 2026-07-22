package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestCloneConfigDeepCopiesAgentProfileRoles(t *testing.T) {
	src := config.Config{
		AgentProfiles: map[string]routing.AgentProfile{
			"fast": {
				Name: "fast",
				Roles: map[routing.AgentRole]string{
					routing.RoleImplementer: "small",
				},
			},
		},
	}

	out := cloneConfig(src)

	// Mutate the clone's inner Roles map directly.
	out.AgentProfiles["fast"].Roles[routing.RoleImplementer] = "large"

	// The source must be unchanged.
	if src.AgentProfiles["fast"].Roles[routing.RoleImplementer] != "small" {
		t.Fatalf("clone shares Roles map with source: got %q, want %q",
			src.AgentProfiles["fast"].Roles[routing.RoleImplementer], "small")
	}
}
