package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/export"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// snapshotContext returns the snapshot service and database, or a
// user-facing error message when either is unavailable.
func snapshotContext(state *session.State) (session.Snapshotter, *db.DB, string) {
	sp := state.Snapshotter()
	if sp == nil {
		return nil, nil, "Snapshot service is not available."
	}
	database := state.DB()
	if database == nil {
		return nil, nil, "No database available to look up snapshots."
	}
	return sp, database, ""
}

func RegisterAll(cmdReg *Registry, toolReg *registry.Registry) error {
	const (
		groupChat     = "Chat"
		groupModels   = "Models & providers"
		groupWorkflow = "Workflows"
		groupChanges  = "Changes"
		groupSettings = "Settings & info"
	)
	commands := []Command{
		{
			Name:        "exit",
			Description: "Exit Marshal",
			Group:       groupChat,
			TUIOnly:     true,
		},
		{
			Name:        "quit",
			Description: "Exit Marshal (alias for /exit)",
			Group:       groupChat,
			TUIOnly:     true,
		},
		{
			Name:        "new",
			Description: "Start a new conversation",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "clear",
			Description: "Start a new conversation (alias for /new)",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "help",
			Description: "Show available commands",
			Args:        "[command]",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) string {
				if len(args) > 0 {
					c, ok := cmdReg.Lookup(args[0])
					if !ok {
						return fmt.Sprintf("Unknown command: /%s", args[0])
					}
					out := "/" + c.Name
					if c.Args != "" {
						out += " " + c.Args
					}
					return out + "\n  " + c.Description
				}
				var b strings.Builder
				b.WriteString("Keys\n")
				b.WriteString("  ⏎ send · esc cancel/deny · tab/shift+tab mode · alt+m /model\n")
				b.WriteString("  ctrl+o settings · ctrl+p models · ctrl+k memory · ctrl+g thinking · ctrl+t tasks · ctrl+r rollback\n")
				b.WriteString("  pgup/pgdn scroll · ctrl+u/ctrl+d half-page · end bottom\n")
				groupOrder := []string{groupChat, groupModels, groupWorkflow, groupChanges, groupSettings, "Other"}
				byGroup := map[string][]Command{}
				for _, c := range cmdReg.List() { // List already sorts and hides Hidden
					g := c.Group
					if g == "" {
						g = "Other"
					}
					byGroup[g] = append(byGroup[g], c)
				}
				for _, g := range groupOrder {
					cmds := byGroup[g]
					if len(cmds) == 0 {
						continue
					}
					b.WriteString(g + "\n")
					for _, c := range cmds {
						line := "  /" + c.Name
						if c.Args != "" {
							line += " " + c.Args
						}
						b.WriteString(fmt.Sprintf("%-28s %s\n", line, c.Description))
					}
				}
				return strings.TrimRight(b.String(), "\n")
			},
		},
		{
			Name:        "tools",
			Description: "List available tools",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) string {
				if toolReg == nil {
					return "Tools unavailable (agent failed to initialise). Fix the provider config and restart, or use /settings."
				}
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
			Group:       groupModels,
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
			Group:       groupSettings,
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
					strutil.CompactTokens(pack.TokenUsage.EstimatedTokens),
					strutil.CompactTokens(pack.TokenUsage.MaxTokens),
					len(pack.Sections),
				)
				for i, section := range pack.Sections {
					title := section.Title
					if title == "" {
						title = section.Source
					}
					fmt.Fprintf(&b, "    %d  %s  %s\n", i+1, title, strutil.CompactTokens(section.EstimatedTokens))
				}
				return strings.TrimRight(b.String(), "\n")
			},
		},
		{
			Name:        "log",
			Description: "Show recent tool calls (audit log)",
			Group:       groupSettings,
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
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "plan",
			Description: "Switch to Plan mode (read-only, forced numbered plan)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "default",
			Description: "Switch to Default mode (read-only, request elevation to edit)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "edit",
			Description: "Switch to Edit mode (plan + edit, confirm each change)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "copilot",
			Description: "Switch to Copilot mode (auto-approve, may ask questions)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "auto",
			Description: "Switch to Auto mode (fully autonomous, no questions)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "mode",
			Description: "Pick the interaction mode (Plan / Default / Edit / Copilot / Auto)",
			Args:        "[plan|default|edit|copilot|auto]",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
		{
			Name:        "swarm",
			Description: "Run a goal through the swarm (planner → scouts → implementer → reviewer)",
			Args:        "<goal>",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
		{
			Name:        "sdd",
			Description: "Run a plan through subagent-driven development (implementer → reviewer → branch review)",
			Args:        "[plan-file]",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
		{
			Name:        "agents",
			Description: "Configure the agent roster: roles, models, custom agents, swarm & SDD budgets",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
		{
			Name:        "connect",
			Description: "Add or reconnect a provider",
			Group:       groupModels,
			TUIOnly:     true,
		},
		{
			Name:        "models",
			Description: "Pick a model from connected providers",
			Group:       groupModels,
			TUIOnly:     true,
		},
		{
			Name:        "model",
			Description: "Switch to a model preset by name",
			Args:        "<preset-name>",
			Group:       groupModels,
			TUIOnly:     true,
		},
		{
			Name:        "config",
			Description: "Show current configuration summary",
			Group:       groupSettings,
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
			Group:       groupSettings,
			TUIOnly:     true,
		},
		{
			Name:        "set",
			Description: "Change a setting inline (\"/set\" alone browses)",
			Args:        "<key> [value]",
			Group:       groupSettings,
			TUIOnly:     true,
		},
		{
			Name:        "memory",
			Description: "Open memory browser",
			Group:       groupSettings,
			TUIOnly:     true,
		},
		{
			Name:        "rollback",
			Description: "Rollback last patch",
			Group:       groupChanges,
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
		{
			Name:        "undo",
			Description: "Restore the working tree to the snapshot before the current turn",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) string {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return errMsg
				}
				hash, err := database.SnapshotBefore(state.SessionID(), state.TurnIndex())
				if err != nil {
					return fmt.Sprintf("Failed to find snapshot to undo: %v", err)
				}
				if hash == "" {
					return "No snapshot to undo to."
				}
				if err := sp.Restore(context.Background(), hash); err != nil {
					return fmt.Sprintf("Failed to restore snapshot: %v", err)
				}
				return fmt.Sprintf("Restored working tree to snapshot %s.", hash)
			},
		},
		{
			Name:        "redo",
			Description: "Redo the last undo by restoring the latest snapshot (experimental)",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) string {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return errMsg
				}
				_, hash, _, err := database.LatestSnapshot(state.SessionID())
				if err != nil {
					return fmt.Sprintf("Failed to find snapshot to redo: %v", err)
				}
				if hash == "" {
					return "No snapshot to redo."
				}
				if err := sp.Restore(context.Background(), hash); err != nil {
					return fmt.Sprintf("Failed to restore snapshot: %v", err)
				}
				return fmt.Sprintf("Redone to latest snapshot %s (experimental).", hash)
			},
		},
		{
			Name:        "diff",
			Description: "Show the current turn's cumulative changes from the last snapshot",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) string {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return errMsg
				}
				hash, err := database.SnapshotBefore(state.SessionID(), state.TurnIndex())
				if err != nil || hash == "" {
					return "No snapshot to diff against yet — make some changes first."
				}
				diff, err := sp.Diff(context.Background(), hash)
				if err != nil {
					return fmt.Sprintf("Diff failed: %v", err)
				}
				if diff == "" {
					return "No changes since the last snapshot."
				}
				return diff
			},
		},
		{
			Name:        "trust",
			Description: "Show or re-open the project trust decision",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) string {
				trusted := state.Trusted()
				if trusted {
					return "Project is trusted. Use --trust (permanent) or restart to re-prompt."
				}
				return "Project is not trusted. Use --trust (permanent) or restart to re-prompt."
			},
		},
		{
			Name:        "rename",
			Description: "Rename the current session (overrides auto-title)",
			Args:        "<title>",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				title := strings.Join(args, " ")
				if title == "" {
					return "Usage: /rename <title>"
				}
				if len(title) > 200 {
					title = title[:200]
				}
				state.SetTitleManual(title)
				if db := state.DB(); db != nil {
					if err := db.UpdateSessionTitle(state.SessionID(), title); err != nil {
						return fmt.Sprintf("Renamed locally, but failed to persist: %v", err)
					}
				}
				return "Session renamed."
			},
		},
		{
			Name:        "rewind",
			Description: "Rewind the conversation to before a prior user turn (starts a new branch)",
			Args:        "[turn-number]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				msgs := state.Messages()
				var turns []session.Message
				for _, m := range msgs {
					if m.Role == session.RoleUser {
						turns = append(turns, m)
					}
				}
				if len(turns) == 0 {
					return "No user turns to rewind to."
				}
				n := 0
				if len(args) > 0 {
					if k, err := strconv.Atoi(args[0]); err == nil && k >= 1 && k <= len(turns) {
						n = k - 1
					} else {
						return fmt.Sprintf("Invalid turn number. Pick 1..%d.", len(turns))
					}
				} else {
					n = len(turns) - 1
				}
				target := turns[n]
				newLeaf := state.Rewind(target.ID)

				if sp, database, _ := snapshotContext(state); sp != nil {
					if hash, err := database.SnapshotBefore(state.SessionID(), state.TurnIndex()); err == nil && hash != "" {
						_ = sp.Restore(context.Background(), hash)
					}
				}
				return fmt.Sprintf("Rewound to before: %q. Your next message starts a new branch (leaf %d).", strutil.Truncate(target.Content, 60, true), newLeaf)
			},
		},
		{
			Name:        "branches",
			Description: "List branches and switch to one (e.g. /branches 2)",
			Args:        "[branch-number]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				leaves := state.Branches()
				if len(leaves) == 0 {
					return "No branches."
				}
				if len(args) == 0 {
					var b strings.Builder
					cur := state.LeafID()
					for i, id := range leaves {
						marker := "  "
						if id == cur {
							marker = "* "
						}
						fmt.Fprintf(&b, "%s%d. leaf %d\n", marker, i+1, id)
					}
					return strings.TrimRight(b.String(), "\n")
				}
				k, err := strconv.Atoi(args[0])
				if err != nil || k < 1 || k > len(leaves) {
					return fmt.Sprintf("Invalid branch. Pick 1..%d.", len(leaves))
				}
				state.SwitchBranch(leaves[k-1])
				return fmt.Sprintf("Switched to branch %d (leaf %d).", k, leaves[k-1])
			},
		},
		{
			Name:        "history",
			Description: "List generations, dump a transcript, or search archived turns",
			Args:        "[dump <seq>|search <query>]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				database := state.DB()
				if database == nil {
					return "No database available."
				}
				cmd := &historyCommand{
					database:  database,
					sessionID: state.SessionID(),
				}
				out, err := cmd.Run(context.Background(), args)
				if err != nil {
					return err.Error()
				}
				return out
			},
		},
		{
			Name:        "export",
			Description: "Export this session to a self-contained HTML file",
			Args:        "[relative-path]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) string {
				rel := strings.TrimSpace(strings.Join(args, " "))
				if rel == "" {
					rel = "marshal-session-" + state.SessionID() + ".html"
				}
				// F-SEC-38: clamp the export to the working dir.
				if filepath.IsAbs(rel) {
					return "Export failed: path must be relative to the working directory"
				}
				cleaned := filepath.Clean(rel)
				if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
					return "Export failed: path escapes the working directory"
				}
				path := filepath.Join(state.WorkingDir, cleaned)
				redactOn := state.Config.Privacy.RedactSecrets
				if err := export.Write(state, path, redactOn); err != nil {
					return "Export failed: " + err.Error()
				}
				return "Exported to " + path
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
