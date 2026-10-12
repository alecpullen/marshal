package settings

import (
	"slices"

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
	case "profiles":
		cfg.AgentProfiles = map[string]routing.AgentProfile{}
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
	case "sdd":
		cfg.SDD = def.SDD
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
	case "project":
		cfg.Project = def.Project
		cfg.Commands = def.Commands
	}
}

// envAllowlistRestoreField restores tools.shell.sandbox.env_allowlist to the
// built-in curated list.
//
// It exists because the list-drill cannot express "use the defaults": deleting
// every row is the only way to empty the list, and [] means "deny all env"
// (see sandbox.Restricted.buildEnv), which strips HOME and with it the
// GOPATH/GOMODCACHE the Go toolchain derives — so `go build` fails. The
// section-level "Reset sandbox" action is too blunt here, since it also
// discards the backend, the resource limits, and the passthrough opt-in, so
// this stays scoped to the one key.
//
// Two presses to confirm, mirroring resetField's idiom.
func envAllowlistRestoreField(s *state, items *[]string) *field {
	id := "sandbox.env_allowlist.restore"
	return &field{
		ID:    id,
		Title: "Restore default env vars",
		Kind:  kindAction,
		Desc:  "restore the curated default list (PATH, HOME, USER, … and the Go toolchain vars)",
		ActLabel: func() string {
			if as, ok := s.actionState[id]; ok && as.label != "" {
				return as.label
			}
			return "restore defaults"
		},
		Disarm: func() { delete(s.actionState, id) },
		Act: func() tea.Cmd {
			if as, ok := s.actionState[id]; ok && as.label == "again to confirm" {
				*items = slices.Clone(config.Default().Tools.Shell.Sandbox.EnvAllowlist)
				s.applyActionResult(id, "\u2713 restored")
				return nil
			}
			s.actionState[id] = actionState{label: "again to confirm"}
			return nil
		},
	}
}

func resetField(s *state, sectionID, title string) *field {
	id := sectionID + ".reset"
	return &field{
		ID:    id,
		Title: "Reset " + title + " to defaults",
		Kind:  kindAction,
		Desc:  "restore this section to built-in defaults (applies immediately)",
		ActLabel: func() string {
			if as, ok := s.actionState[id]; ok && as.label != "" {
				return as.label
			}
			return "reset to defaults"
		},
		Disarm: func() { delete(s.actionState, id) },
		Act: func() tea.Cmd {
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
	base := f.List.Fields
	f.List.Fields = func() []*field {
		return append(base(), resetField(s, sectionID, title))
	}
	return f
}
