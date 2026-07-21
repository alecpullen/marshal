package settings

import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"

	tea "charm.land/bubbletea/v2"
)

func resetSection(cfg *config.Config, sectionID string) {
	def := config.Default()
	switch sectionID {
	case "agent":
		cfg.Agent = def.Agent
		cfg.Profile = def.Profile
	case "providers":
		cfg.Providers = nil
	case "presets":
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	case "privacy":
		cfg.Privacy = def.Privacy
	case "shell":
		cfg.Tools.Shell = def.Tools.Shell
	case "sandbox":
		cfg.Tools.Shell.Sandbox = def.Tools.Shell.Sandbox
	case "indexing":
		cfg.Indexing = def.Indexing
	case "web":
		cfg.Web = def.Web
	case "swarm":
		cfg.Swarm = def.Swarm
	case "mcp":
		cfg.MCP = def.MCP
	case "snapshots":
		cfg.Snapshots = def.Snapshots
	case "hooks":
		cfg.Hooks = def.Hooks
	case "permissions":
		cfg.Permissions = def.Permissions
	case "diagnostics":
		cfg.Diagnostics = def.Diagnostics
	case "commands":
		cfg.Commands = def.Commands
	}
}

func resetField(s *state, sectionID, title string) *field {
	id := sectionID + ".reset"
	return &field{
		id:    id,
		title: "Reset " + title + " to defaults",
		kind:  kindAction,
		desc:  "restore this section to built-in defaults (applies immediately)",
		actLabel: func() string {
			if as, ok := s.actionState[id]; ok && as.label != "" {
				return as.label
			}
			return "reset to defaults"
		},
		disarm: func() { delete(s.actionState, id) },
		act: func() tea.Cmd {
			if as, ok := s.actionState[id]; ok && as.label == "again to confirm" {
				resetSection(&s.cfg, sectionID)
				s.applyActionResult(id, "\u2713 reset")
				return nil
			}
			s.actionState[id] = actionState{label: "again to confirm"}
			return nil
		},
	}
}

func withResetRow(s *state, sectionID, title string, f *frame) *frame {
	base := f.list.fields
	f.list.fields = func() []*field {
		return append(base(), resetField(s, sectionID, title))
	}
	return f
}
