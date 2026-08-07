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
	"marshal/internal/history"
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
			Handler: func(state *session.State, args []string) Result {
				count := state.ClearMessages()
				return Text(fmt.Sprintf("Started new conversation. Cleared %d messages.", count))
			},
		},
		{
			Name:        "clear",
			Description: "Start a new conversation (alias for /new)",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				count := state.ClearMessages()
				return Text(fmt.Sprintf("Started new conversation. Cleared %d messages.", count))
			},
		},
		{
			Name:        "save",
			Description: "Confirm the current session is saved (auto-saved throughout)",
			Group:       groupChat,
			TUIOnly:     true,
		},
		{
			Name:        "sessions",
			Description: "List previous sessions and resume one",
			Group:       groupChat,
			TUIOnly:     true,
		},
		{
			Name:        "resume",
			Description: "Resume a previous session by ID",
			Args:        "<session-id>",
			Group:       groupChat,
			TUIOnly:     true,
		},
		{
			Name:        "help",
			Description: "Show available commands",
			Args:        "[command]",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				if len(args) > 0 {
					c, ok := cmdReg.Lookup(args[0])
					if !ok {
						return Text(fmt.Sprintf("Unknown command: /%s", args[0]))
					}
					out := "/" + c.Name
					if c.Args != "" {
						out += " " + c.Args
					}
					return Text(out + "\n  " + c.Description)
				}
				groupOrder := []string{groupChat, groupModels, groupWorkflow, groupChanges, groupSettings, "Other"}
				byGroup := map[string][]Command{}
				for _, c := range cmdReg.List() { // List already sorts and hides Hidden
					g := c.Group
					if g == "" {
						g = "Other"
					}
					byGroup[g] = append(byGroup[g], c)
				}
				var rows []Row
				for _, g := range groupOrder {
					cmds := byGroup[g]
					if len(cmds) == 0 {
						continue
					}
					rows = append(rows, Row{Header: g})
					for _, c := range cmds {
						line := "/" + c.Name
						if c.Args != "" {
							line += " " + c.Args
						}
						rows = append(rows, Row{Text: line, Detail: c.Description})
					}
				}
				res := Panel("Help", false, rows)
				res.Doc.Footer = "⏎ send · esc cancel/deny · tab/shift+tab mode · alt+m /models\n" +
					"ctrl+o settings · ctrl+p models · ctrl+k memory · ctrl+g thinking · ctrl+t tasks · ctrl+r rollback\n" +
					"pgup/pgdn scroll · ctrl+u/ctrl+d half-page · end bottom · ctrl+b side rail\n" +
					"select text: hold alt/option while dragging (the mouse wheel is captured for scrolling).\n" +
					"ctrl+b hides the side rail first so a selection does not span it."
				return res
			},
		},
		{
			Name:        "tools",
			Description: "List available tools",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				if toolReg == nil {
					return Text("Tools unavailable (agent failed to initialise). Fix the provider config and restart, or use /settings.")
				}
				var rows []Row
				for _, tool := range toolReg.List() {
					rows = append(rows, Row{
						Text:   tool.Name,
						Detail: fmt.Sprintf("(%s) — %s", tool.Risk, tool.Description),
					})
				}
				return Panel("Tools", false, rows)
			},
		},
		{
			Name:        "route",
			Description: "Show current model route",
			Group:       groupModels,
			Handler: func(state *session.State, args []string) Result {
				route := state.ActiveRoute()
				if !route.Active {
					return Text("No active route.")
				}
				local := ""
				if route.LocalOnly {
					local = " (local only)"
				}
				return Text(fmt.Sprintf(
					"Current route:\n  Profile: %s\n  Role: %s\n  Provider: %s\n  Model: %s\n  Preset: %s%s",
					route.Profile, route.Role, route.Provider, route.Model, route.Preset, local,
				))
			},
		},
		{
			Name:        "context",
			Description: "Show context window usage",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				msgs := state.Messages()
				var totalChars int
				for _, m := range msgs {
					totalChars += len(m.Content)
				}
				rows := []Row{
					{Text: "Messages", Detail: fmt.Sprintf("%d (%d chars)", len(msgs), totalChars)},
				}
				pack := state.ContextPack()
				if pack.IsEmpty() {
					rows = append(rows, Row{Text: "Pack", Detail: "not built yet"})
					return Panel("Context", true, rows)
				}
				rows = append(rows, Row{
					Text: "Pack",
					Detail: fmt.Sprintf("%s/%s tokens, %d sections",
						strutil.CompactTokens(pack.TokenUsage.EstimatedTokens),
						strutil.CompactTokens(pack.TokenUsage.MaxTokens),
						len(pack.Sections)),
				})
				rows = append(rows, Row{Header: "Sections"})
				for i, section := range pack.Sections {
					title := section.Title
					if title == "" {
						title = section.Source
					}
					rows = append(rows, Row{
						Text:   fmt.Sprintf("%d  %s", i+1, title),
						Detail: strutil.CompactTokens(section.EstimatedTokens),
					})
				}
				return Panel("Context", true, rows)
			},
		},
		{
			Name:        "log",
			Description: "Show recent tool calls (audit log)",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				events := state.AuditLog()
				if len(events) == 0 {
					return Text("No tool calls yet.")
				}
				start := 0
				if len(events) > 15 {
					start = len(events) - 15
				}
				var rows []Row
				for _, e := range events[start:] {
					rows = append(rows, Row{
						Text:   fmt.Sprintf("%s  %s", e.Timestamp.Format("15:04:05"), e.ToolName),
						Detail: e.ResultSummary,
					})
				}
				return Panel("Recent tool calls", false, rows)
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
			Description: "Run a plan file task-by-task (subagent-driven development): implement, review, and commit each task on a branch",
			Args:        "[plan-file]",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
		{
			Name:        "run",
			Description: "Show the last plan run: branch diff, ledger, and artifacts",
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
			Description: "Pick a model from connected providers, or switch directly to a preset or model",
			Args:        "[preset-or-model]",
			Group:       groupModels,
			TUIOnly:     true,
		},
		{
			Name:        "config",
			Description: "Show current configuration summary",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				cfg := state.Config
				route := state.ActiveRoute()
				return Text(fmt.Sprintf(
					"Configuration:\n  Project: %s\n  Working dir: %s\n  Profile: %s\n  Remote allowed: %v\n  Auto-approve: %v",
					cfg.Project.Name, state.WorkingDir, route.Profile, cfg.Privacy.RemoteProvidersAllowed, cfg.Tools.Shell.AutoApprove,
				))
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
			Name:        "doctor",
			Description: "Report configuration problems",
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
			Name:        "skills",
			Description: "Manage installed skills",
			Group:       groupSettings,
			TUIOnly:     true,
		},
		{
			Name:        "plugins",
			Description: "Manage installed plugins",
			Group:       groupSettings,
			TUIOnly:     true,
		},
		{
			Name:        "rollback",
			Description: "Rollback last patch",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) Result {
				if !state.HasBackup() {
					return Text("No backup available to rollback.")
				}
				if err := state.RollbackBackup(); err != nil {
					return Text(fmt.Sprintf("Rollback failed: %v", err))
				}
				return Text("Rolled back last patch. All modified files reverted.")
			},
		},
		{
			Name:        "undo",
			Description: "Restore the working tree to the snapshot before the current turn",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) Result {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return Text(errMsg)
				}
				hash, err := database.SnapshotBefore(state.SessionID(), state.TurnIndex())
				if err != nil {
					return Text(fmt.Sprintf("Failed to find snapshot to undo: %v", err))
				}
				if hash == "" {
					return Text("No snapshot to undo to.")
				}
				if err := sp.Restore(context.Background(), hash); err != nil {
					return Text(fmt.Sprintf("Failed to restore snapshot: %v", err))
				}
				return Text(fmt.Sprintf("Restored working tree to snapshot %s.", hash))
			},
		},
		{
			Name:        "redo",
			Description: "Redo the last undo by restoring the latest snapshot (experimental)",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) Result {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return Text(errMsg)
				}
				_, hash, _, err := database.LatestSnapshot(state.SessionID())
				if err != nil {
					return Text(fmt.Sprintf("Failed to find snapshot to redo: %v", err))
				}
				if hash == "" {
					return Text("No snapshot to redo.")
				}
				if err := sp.Restore(context.Background(), hash); err != nil {
					return Text(fmt.Sprintf("Failed to restore snapshot: %v", err))
				}
				return Text(fmt.Sprintf("Redone to latest snapshot %s (experimental).", hash))
			},
		},
		{
			Name:        "diff",
			Description: "Show the current turn's cumulative changes from the last snapshot",
			Group:       groupChanges,
			Handler: func(state *session.State, args []string) Result {
				sp, database, errMsg := snapshotContext(state)
				if errMsg != "" {
					return Text(errMsg)
				}
				hash, err := database.SnapshotBefore(state.SessionID(), state.TurnIndex())
				if err != nil || hash == "" {
					return Text("No snapshot to diff against yet — make some changes first.")
				}
				diff, err := sp.Diff(context.Background(), hash)
				if err != nil {
					return Text(fmt.Sprintf("Diff failed: %v", err))
				}
				if diff == "" {
					return Text("No changes since the last snapshot.")
				}
				return Text(diff)
			},
		},
		{
			Name:        "trust",
			Description: "Show or re-open the project trust decision",
			Group:       groupSettings,
			Handler: func(state *session.State, args []string) Result {
				trusted := state.Trusted()
				if trusted {
					return Text("Project is trusted. Use --trust (permanent) or restart to re-prompt.")
				}
				return Text("Project is not trusted. Use --trust (permanent) or restart to re-prompt.")
			},
		},
		{
			Name:        "rename",
			Description: "Rename the current session (overrides auto-title)",
			Args:        "<title>",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				title := strings.Join(args, " ")
				if title == "" {
					return Text("Usage: /rename <title>")
				}
				if len(title) > 200 {
					title = title[:200]
				}
				state.SetTitleManual(title)
				if db := state.DB(); db != nil {
					if err := db.UpdateSessionTitle(state.SessionID(), title); err != nil {
						return Text(fmt.Sprintf("Renamed locally, but failed to persist: %v", err))
					}
				}
				return Text("Session renamed.")
			},
		},
		{
			Name:        "rewind",
			Description: "Rewind the conversation to before a prior user turn (starts a new branch)",
			Args:        "[turn-number]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				msgs := state.Messages()
				var turns []session.Message
				for _, m := range msgs {
					if m.Role == session.RoleUser {
						turns = append(turns, m)
					}
				}
				if len(turns) == 0 {
					return Text("No user turns to rewind to.")
				}
				n := 0
				if len(args) > 0 {
					if k, err := strconv.Atoi(args[0]); err == nil && k >= 1 && k <= len(turns) {
						n = k - 1
					} else {
						return Text(fmt.Sprintf("Invalid turn number. Pick 1..%d.", len(turns)))
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
				return Text(fmt.Sprintf("Rewound to before: %q. Your next message starts a new branch (leaf %d).", strutil.Truncate(target.Content, 60, true), newLeaf))
			},
		},
		{
			Name:        "branches",
			Description: "List branches and switch to one (e.g. /branches 2)",
			Args:        "[branch-number]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				leaves := state.Branches()
				if len(leaves) == 0 {
					return Text("No branches.")
				}
				if len(args) > 0 {
					k, err := strconv.Atoi(args[0])
					if err != nil || k < 1 || k > len(leaves) {
						return Text(fmt.Sprintf("Invalid branch. Pick 1..%d.", len(leaves)))
					}
					state.SwitchBranch(leaves[k-1])
					return Text(fmt.Sprintf("Switched to branch %d (leaf %d).", k, leaves[k-1]))
				}
				cur := state.LeafID()
				var rows []Row
				for i, id := range leaves {
					i, id := i, id
					marker := "  "
					if id == cur {
						marker = "* "
					}
					rows = append(rows, Row{
						Text:        fmt.Sprintf("%sbranch %d", marker, i+1),
						Detail:      fmt.Sprintf("leaf %d", id),
						Desc:        "switch the conversation to this branch",
						ActionLabel: "↵ switch",
						Action: func(s *session.State) Result {
							s.SwitchBranch(id)
							return Text(fmt.Sprintf("Switched to branch %d (leaf %d).", i+1, id))
						},
					})
				}
				return Panel("Branches", false, rows)
			},
		},
		{
			Name:        "history",
			Description: "List generations, dump a transcript, or search archived turns",
			Args:        "[dump <seq>|search <query>]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				database := state.DB()
				if database == nil {
					return Text("No database available.")
				}
				// dump/search keep their text behavior via historyCommand.
				if len(args) > 0 {
					cmd := &historyCommand{database: database, sessionID: state.SessionID()}
					out, err := cmd.Run(context.Background(), args)
					if err != nil {
						return Text(err.Error())
					}
					return Text(out)
				}
				summaries, err := history.ListGenerations(context.Background(), database, state.SessionID())
				if err != nil {
					return Text(fmt.Sprintf("list generations: %v", err))
				}
				if len(summaries) == 0 {
					return Text("No generations yet.")
				}
				var rows []Row
				for _, s := range summaries {
					s := s
					status := "ended"
					if s.Live {
						status = "live"
					}
					rows = append(rows, Row{
						Text:     fmt.Sprintf("%d  %s", s.Seq, s.StartedAt.UTC().Format("2006-01-02 15:04:05")),
						Detail:   fmt.Sprintf("%d turns · %s", s.TurnCount, status),
						Desc:     "digest=" + s.SeedDigest,
						Children: generationTurnRows(database, s),
					})
				}
				return Panel("Generations", true, rows)
			},
		},
		{
			Name:        "export",
			Description: "Export this session to a self-contained HTML file",
			Args:        "[relative-path]",
			Group:       groupChat,
			Handler: func(state *session.State, args []string) Result {
				rel := strings.TrimSpace(strings.Join(args, " "))
				if rel == "" {
					rel = "marshal-session-" + state.SessionID() + ".html"
				}
				// F-SEC-38: clamp the export to the working dir.
				if filepath.IsAbs(rel) {
					return Text("Export failed: path must be relative to the working directory")
				}
				cleaned := filepath.Clean(rel)
				if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
					return Text("Export failed: path escapes the working directory")
				}
				path := filepath.Join(state.WorkingDir, cleaned)
				redactOn := state.Config.Privacy.RedactSecrets
				if err := export.Write(state, path, redactOn); err != nil {
					return Text("Export failed: " + err.Error())
				}
				return Text("Exported to " + path)
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

// generationTurnRows loads a generation's archived turns as drill-in rows,
// capped so a huge generation cannot stall the panel.
func generationTurnRows(database *db.DB, s history.GenerationSummary) []Row {
	turns, err := database.TurnsForGeneration(s.ID)
	if err != nil {
		return []Row{{Text: "failed to load turns", Detail: err.Error()}}
	}
	const cap = 200
	var rows []Row
	for _, turn := range turns {
		snippet := turn.Content
		if len(snippet) > 80 {
			snippet = snippet[:80] + "…"
		}
		rows = append(rows, Row{
			Text:   fmt.Sprintf("turn %d (%s)", turn.TurnSeq, turn.Role),
			Detail: snippet,
		})
		if len(rows) >= cap {
			rows = append(rows, Row{Text: fmt.Sprintf("… %d more turns", len(turns)-cap)})
			break
		}
	}
	return rows
}
