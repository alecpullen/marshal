package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestCloneConfigDeepCopiesAgentProfileRoles(t *testing.T) {
	src := config.Config{
		AgentProfiles: map[string]routing.AgentProfile{
			"fast": {
				Name: "fast",
				Roles: map[routing.AgentRole]routing.RoleBinding{
					routing.RoleImplementer: {Preset: "small"},
				},
			},
		},
	}

	out := cloneConfig(src)

	// Mutate the clone's inner Roles map directly.
	out.AgentProfiles["fast"].Roles[routing.RoleImplementer] = routing.RoleBinding{Preset: "large"}

	// The source must be unchanged.
	if src.AgentProfiles["fast"].Roles[routing.RoleImplementer].Preset != "small" {
		t.Fatalf("clone shares Roles map with source: got %q, want %q",
			src.AgentProfiles["fast"].Roles[routing.RoleImplementer], "small")
	}
}

func TestCloneConfigDeepCopiesCustomAgents(t *testing.T) {
	src := config.Config{
		CustomAgents: map[string]routing.CustomAgent{
			"reviewer": {
				Name:         "reviewer",
				Preset:       "fast",
				ToolDenylist: []string{"file.write_patch", "shell.run"},
			},
		},
	}

	out := cloneConfig(src)

	// Mutate the clone's inner slice and fields.
	ca := out.CustomAgents["reviewer"]
	ca.ToolDenylist = append(ca.ToolDenylist, "web.fetch")
	ca.Preset = "slow"
	out.CustomAgents["reviewer"] = ca

	// The source must be unchanged.
	srcCA := src.CustomAgents["reviewer"]
	if srcCA.Preset != "fast" {
		t.Fatalf("clone shares Preset with source: got %q, want %q", srcCA.Preset, "fast")
	}
	if len(srcCA.ToolDenylist) != 2 {
		t.Fatalf("clone shares ToolDenylist slice with source: got %v", srcCA.ToolDenylist)
	}
}

func TestQueueCmdBatchesInsteadOfOverwriting(t *testing.T) {
	s := newState(config.Default())
	fired := 0
	s.queueCmd(func() tea.Msg { fired++; return nil })
	s.queueCmd(func() tea.Msg { fired++; return nil })
	cmd := s.takePendingCmd()
	if cmd == nil {
		t.Fatal("takePendingCmd returned nil after two queued commands")
	}
	cmd() // a tea.Batch's own func returns a BatchMsg holding both cmds
	if s.takePendingCmd() != nil {
		t.Error("takePendingCmd must clear the queue")
	}
}

func TestMarkProbedIsOnceOnly(t *testing.T) {
	s := newState(config.Default())
	if !s.markProbed("ollama") {
		t.Fatal("first markProbed must return true")
	}
	if s.markProbed("ollama") {
		t.Error("second markProbed for the same provider must return false")
	}
	if !s.markProbed("groq") {
		t.Error("a different provider must still return true")
	}
}

func TestStateDataDirDefaultsEmpty(t *testing.T) {
	if got := newState(config.Default()).dataDir; got != "" {
		t.Errorf("dataDir = %q, want empty by default", got)
	}
}
