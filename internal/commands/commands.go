package commands

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func RegisterAll(cmdReg *Registry, toolReg *registry.Registry) error {
	commands := []Command{
		{
			Name:        "exit",
			Description: "Exit Marshal",
			Handler:     func(state *session.State, args []string) string { return "Goodbye!" },
		},
		{
			Name:        "quit",
			Description: "Exit Marshal (alias for /exit)",
			Handler:     func(state *session.State, args []string) string { return "Goodbye!" },
		},
		{
			Name:        "new",
			Description: "Start a new conversation",
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "clear",
			Description: "Start a new conversation (alias for /new)",
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "help",
			Description: "Show available commands",
			Handler: func(state *session.State, args []string) string {
				var b strings.Builder
				b.WriteString("Available commands:\n\n")
				for _, cmd := range cmdReg.List() {
					argStr := ""
					if cmd.Args != "" {
						argStr = " " + cmd.Args
					}
					b.WriteString(fmt.Sprintf("  /%s%s — %s\n", cmd.Name, argStr, cmd.Description))
				}
				return b.String()
			},
		},
		{
			Name:        "tools",
			Description: "List available tools",
			Handler: func(state *session.State, args []string) string {
				var b strings.Builder
				b.WriteString("Available tools:\n\n")
				for _, tool := range toolReg.List() {
					b.WriteString(fmt.Sprintf("  %s (%s) — %s\n", tool.Name, tool.Risk, tool.Description))
				}
				return b.String()
			},
		},
		{
			Name:        "route",
			Description: "Show current model route",
			Handler: func(state *session.State, args []string) string {
				route := state.ActiveRoute()
				if !route.Active {
					return "No active route."
				}
				local := ""
				if route.LocalOnly {
					local = " (local only)"
				}
				return fmt.Sprintf(
					"Current route:\n  Profile: %s\n  Role: %s\n  Provider: %s\n  Model: %s\n  Preset: %s%s",
					route.Profile, route.Role, route.Provider, route.Model, route.Preset, local,
				)
			},
		},
		{
			Name:        "context",
			Description: "Show context window usage",
			Handler: func(state *session.State, args []string) string {
				msgs := state.Messages()
				var totalChars int
				for _, m := range msgs {
					totalChars += len(m.Content)
				}
				pack := state.ContextPack()
				return fmt.Sprintf(
					"Context stats:\n  Messages: %d\n  Total chars: %d\n  Context pack sections: %d",
					len(msgs), totalChars, len(pack.Sections),
				)
			},
		},
		{
			Name:        "stop",
			Description: "Cancel the current agent turn",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "ask",
			Description: "Switch to Ask mode (read-only, no planning)",
			Handler: func(state *session.State, args []string) string {
				return "Switched to Ask mode. Agent will answer questions without planning or editing."
			},
		},
		{
			Name:        "edit",
			Description: "Switch to Edit mode (planning + full tools)",
			Handler: func(state *session.State, args []string) string {
				return "Switched to Edit mode. Agent will plan and execute changes."
			},
		},
		{
			Name:        "auto",
			Description: "Switch to Auto mode (classify each turn)",
			Handler: func(state *session.State, args []string) string {
				return "Switched to Auto mode. Agent will classify each turn automatically."
			},
		},
		{
			Name:        "model",
			Description: "Switch to a model preset by name",
			Args:        "<preset-name>",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "config",
			Description: "Show current configuration summary",
			Handler: func(state *session.State, args []string) string {
				cfg := state.Config
				route := state.ActiveRoute()
				return fmt.Sprintf(
					"Configuration:\n  Project: %s\n  Working dir: %s\n  Profile: %s\n  Remote allowed: %v\n  Auto-approve: %v",
					cfg.Project.Name, state.WorkingDir, route.Profile, cfg.Privacy.RemoteProvidersAllowed, cfg.Tools.Shell.AutoApprove,
				)
			},
		},
		{
			Name:        "settings",
			Description: "Open settings panel",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "memory",
			Description: "Open memory browser",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "rollback",
			Description: "Rollback last patch",
			Handler: func(state *session.State, args []string) string {
				if !state.HasBackup() {
					return "No backup available to rollback."
				}
				if err := state.RollbackBackup(); err != nil {
					return fmt.Sprintf("Rollback failed: %v", err)
				}
				return "Rolled back last patch. All modified files reverted."
			},
		},
	}

	for _, cmd := range commands {
		if err := cmdReg.Register(cmd); err != nil {
			return err
		}
	}
	return nil
}
