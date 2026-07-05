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
				var b strings.Builder
				fmt.Fprintf(&b, "Context:\n  Messages: %d (%d chars)\n", len(msgs), totalChars)
				if pack.IsEmpty() {
					b.WriteString("  No context pack built yet.")
					return b.String()
				}
				fmt.Fprintf(&b, "  Pack: %s/%s tokens, %d sections\n",
					compactTokens(pack.TokenUsage.EstimatedTokens),
					compactTokens(pack.TokenUsage.MaxTokens),
					len(pack.Sections),
				)
				for i, section := range pack.Sections {
					title := section.Title
					if title == "" {
						title = section.Source
					}
					fmt.Fprintf(&b, "    %d  %s  %s\n", i+1, title, compactTokens(section.EstimatedTokens))
				}
				return strings.TrimRight(b.String(), "\n")
			},
		},
		{
			Name:        "log",
			Description: "Show recent tool calls (audit log)",
			Handler: func(state *session.State, args []string) string {
				events := state.AuditLog()
				if len(events) == 0 {
					return "No tool calls yet."
				}
				start := 0
				if len(events) > 15 {
					start = len(events) - 15
				}
				var b strings.Builder
				b.WriteString("Recent tool calls:\n\n")
				for _, e := range events[start:] {
					b.WriteString(fmt.Sprintf("  %s  %-14s  %s\n",
						e.Timestamp.Format("15:04:05"), e.ToolName, e.ResultSummary))
				}
				return strings.TrimRight(b.String(), "\n")
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
			Name:        "swarm",
			Description: "Run a goal through the swarm (planner → scouts → implementer → reviewer)",
			Args:        "<goal>",
			Handler:     func(state *session.State, args []string) string { return "" },
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

// compactTokens renders a token count the way the TUI does: "842", "18k".
func compactTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
