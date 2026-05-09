package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/alecpullen/marshal/internal/orchestrator"
	"github.com/alecpullen/marshal/internal/pipeline"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// graphCmd creates the graph subcommand tree.
func graphCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Inspect and manage task graphs",
		Long: `View, create, and manipulate task execution graphs.
		
Task graphs represent the orchestration structure for multi-step AI workflows.
Each graph contains tasks with dependencies, forming a DAG that the scheduler executes.`,
	}

	cmd.AddCommand(
		graphShowCmd(gf),
		graphStatusCmd(gf),
		graphHistoryCmd(gf),
		graphValidateCmd(gf),
		graphExportCmd(gf),
		graphCreateCmd(gf),
		graphMutateCmd(gf),
	)

	return cmd
}

// graphShowCmd displays a graph as a diagram.
func graphShowCmd(gf *globalFlags) *cobra.Command {
	var format string
	var compact bool

	cmd := &cobra.Command{
		Use:   "show [graph-id]",
		Short: "Display task graph diagram",
		Long:  `Show the task graph as a Mermaid diagram, ASCII tree, or simple text.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := ""
			if len(args) > 0 {
				graphID = args[0]
			}

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			opts := orchestrator.DefaultMermaidOptions()
			opts.Compact = compact

			gen := orchestrator.NewMermaidGenerator(g, opts)

			switch strings.ToLower(format) {
			case "mermaid", "":
				fmt.Println(gen.Generate())
			case "ascii", "tree":
				fmt.Println(gen.GenerateASCII())
			case "simple", "text":
				fmt.Println(gen.GenerateSimple())
			case "markdown", "md":
				fmt.Println(gen.ExportMarkdown())
			default:
				return fmt.Errorf("unknown format: %s (valid: mermaid, ascii, simple, markdown)", format)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "mermaid", "Output format (mermaid|ascii|simple|markdown)")
	cmd.Flags().BoolVarP(&compact, "compact", "c", false, "Use compact node labels")

	return cmd
}

// graphStatusCmd shows graph execution status.
func graphStatusCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [graph-id]",
		Short: "Show graph execution status",
		Long:  `Display current execution progress, task states, and statistics.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := ""
			if len(args) > 0 {
				graphID = args[0]
			}

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			stats := g.Stats()
			tiers := g.TopologicalTiers()

			// Header
			fmt.Printf("Graph: %s\n", g.ID)
			fmt.Printf("Goal:  %s\n", g.RootGoal)
			fmt.Printf("Status: %s | Version: %d | Updated: %s\n\n",
				stats.Status, stats.GraphVersion, g.UpdatedAt.Format("15:04:05"))

			// Statistics
			fmt.Printf("Tasks: %d total\n", stats.TotalTasks)
			fmt.Printf("  ✅ Passed: %d\n", stats.CompletedTasks)
			fmt.Printf("  ❌ Failed: %d\n", stats.FailedTasks)
			fmt.Printf("  🔵 Running: %d\n", stats.RunningTasks)
			fmt.Printf("  ⚪ Pending: %d\n", stats.PendingTasks)

			// Execution tiers
			if len(tiers) > 0 {
				fmt.Printf("\nExecution Tiers (%d total):\n", len(tiers))
				for i, tier := range tiers {
					fmt.Printf("  Tier %d: %d task(s)\n", i+1, len(tier))
					for _, taskID := range tier {
						task, ok := g.GetTask(taskID)
						if !ok {
							continue
						}
						emoji := statusEmoji(task.Status)
						desc := task.Description
						if len(desc) > 40 {
							desc = desc[:37] + "..."
						}
						fmt.Printf("    %s %-12s %s\n", emoji, taskID, desc)
					}
				}
			}

			// Ready tasks
			ready := g.Ready()
			if len(ready) > 0 {
				fmt.Printf("\nReady to Execute (%d):\n", len(ready))
				for _, task := range ready {
					fmt.Printf("  - %s (%s)\n", task.ID, task.Role)
				}
			}

			// History summary
			if len(g.History) > 0 {
				fmt.Printf("\nMutations: %d total\n", len(g.History))
				recent := g.History[len(g.History)-5:]
				for _, m := range recent {
					target := m.TargetID
					if target == "" && m.NewSpec != nil {
						target = m.NewSpec.ID
					}
					fmt.Printf("  %s %s → %s\n", m.Timestamp.Format("15:04:05"), m.Type, target)
				}
			}

			return nil
		},
	}

	return cmd
}

// graphHistoryCmd shows mutation history.
func graphHistoryCmd(gf *globalFlags) *cobra.Command {
	var limit int
	var mutationType string

	cmd := &cobra.Command{
		Use:   "history [graph-id]",
		Short: "Show graph mutation history",
		Long:  `Display the complete history of changes to the graph.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := ""
			if len(args) > 0 {
				graphID = args[0]
			}

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			fmt.Printf("Mutation History for %s:\n\n", g.ID)

			if len(g.History) == 0 {
				fmt.Println("No mutations recorded (fresh graph).")
				return nil
			}

			start := 0
			if limit > 0 && len(g.History) > limit {
				start = len(g.History) - limit
			}

			fmt.Printf("%-4s %-12s %-20s %-30s %-20s\n", "#", "Time", "Type", "Target", "Reason")
			fmt.Println(strings.Repeat("-", 88))

			for i, m := range g.History[start:] {
				// Filter by type if specified
				if mutationType != "" && string(m.Type) != mutationType {
					continue
				}

				target := m.TargetID
				if target == "" && m.NewSpec != nil {
					target = m.NewSpec.ID
				}

				reason := m.Reason
				if len(reason) > 28 {
					reason = reason[:25] + "..."
				}

				fmt.Printf("%-4d %-12s %-20s %-30s %-20s\n",
					start+i+1,
					m.Timestamp.Format("15:04:05"),
					m.Type,
					target,
					reason,
				)
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum entries to show")
	cmd.Flags().StringVarP(&mutationType, "type", "t", "", "Filter by mutation type")

	return cmd
}

// graphValidateCmd validates graph integrity.
func graphValidateCmd(gf *globalFlags) *cobra.Command {
	var strict bool

	cmd := &cobra.Command{
		Use:   "validate [graph-id]",
		Short: "Validate graph integrity",
		Long:  `Check the graph for cycles, orphaned tasks, and other issues.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := ""
			if len(args) > 0 {
				graphID = args[0]
			}

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			validator := orchestrator.NewValidator(strict)
			result := validator.Validate(g)

			fmt.Println(result.String())

			if !result.Valid {
				os.Exit(1)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&strict, "strict", "s", false, "Treat warnings as errors")

	return cmd
}

// graphExportCmd exports graph to various formats.
func graphExportCmd(gf *globalFlags) *cobra.Command {
	var output string
	var format string

	cmd := &cobra.Command{
		Use:   "export [graph-id]",
		Short: "Export graph to file",
		Long:  `Export the graph as Mermaid diagram, Markdown report, or JSON.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := ""
			if len(args) > 0 {
				graphID = args[0]
			}

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			var content string
			gen := orchestrator.NewMermaidGenerator(g, orchestrator.DefaultMermaidOptions())

			switch strings.ToLower(format) {
			case "mermaid":
				content = gen.Generate()
			case "markdown", "md":
				content = gen.ExportMarkdown()
			case "simple":
				content = gen.GenerateSimple()
			case "ascii":
				content = gen.GenerateASCII()
			default:
				return fmt.Errorf("unknown format: %s", format)
			}

			if output == "" || output == "-" {
				fmt.Println(content)
			} else {
				if err := os.WriteFile(output, []byte(content), 0644); err != nil {
					return fmt.Errorf("writing file: %w", err)
				}
				fmt.Printf("Exported to %s\n", output)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "-", "Output file (- for stdout)")
	cmd.Flags().StringVarP(&format, "format", "f", "mermaid", "Export format (mermaid|markdown|simple|ascii)")

	return cmd
}

// graphCreateCmd creates a new graph from a plan.
func graphCreateCmd(gf *globalFlags) *cobra.Command {
	var sessionID string
	var goal string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new empty task graph",
		Long:  `Create a fresh task graph for testing or manual population.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if goal == "" {
				goal = "Manual task graph"
			}

			if sessionID == "" {
				sessionID = uuid.New().String()[:8]
			}

			graphID := orchestrator.GenerateGraphID()
			g := orchestrator.NewGraph(graphID, sessionID, goal)

			// Save to storage
			store, err := getGraphStore(sessionID)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Save(g); err != nil {
				return fmt.Errorf("saving graph: %w", err)
			}

			fmt.Printf("Created graph %s in session %s\n", graphID, sessionID)
			fmt.Printf("Goal: %s\n", goal)

			return nil
		},
	}

	cmd.Flags().StringVarP(&sessionID, "session", "s", "", "Session ID (auto-generated if empty)")
	cmd.Flags().StringVarP(&goal, "goal", "g", "", "Graph goal/purpose")

	return cmd
}

// graphMutateCmd manually mutates a graph.
func graphMutateCmd(gf *globalFlags) *cobra.Command {
	var mutationType string
	var taskID string
	var specJSON string

	cmd := &cobra.Command{
		Use:   "mutate [graph-id]",
		Short: "Manually mutate a graph",
		Long:  `Apply a mutation to a graph (add, remove, update, replace tasks).`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			graphID := args[0]

			g, err := loadGraph(graphID)
			if err != nil {
				return err
			}

			m := pipeline.NewGraphMutation(pipeline.MutationType(mutationType))
			m.TargetID = taskID

			if specJSON != "" {
				var spec pipeline.TaskSpec
				if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
					return fmt.Errorf("parsing spec JSON: %w", err)
				}
				m.NewSpec = &spec
			}

			m.Reason = "Manual CLI mutation"
			m.Trigger = "cli"

			if err := g.ApplyMutation(*m); err != nil {
				return fmt.Errorf("applying mutation: %w", err)
			}

			// Save updated graph
			sessionID := g.SessionID
			store, err := getGraphStore(sessionID)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Save(g); err != nil {
				return fmt.Errorf("saving graph: %w", err)
			}

			fmt.Printf("Applied %s mutation to graph %s\n", mutationType, graphID)
			fmt.Printf("Graph now at version %d\n", g.Version)

			return nil
		},
	}

	cmd.Flags().StringVarP(&mutationType, "type", "t", "add", "Mutation type (add|remove|update|replace)")
	cmd.Flags().StringVar(&taskID, "task", "", "Task ID to operate on")
	cmd.Flags().StringVar(&specJSON, "spec", "", "Task spec JSON (for add/update/replace)")

	return cmd
}

// loadGraph loads a graph from storage.
func loadGraph(graphID string) (*orchestrator.Graph, error) {
	// Get session directory from environment or default
	sessionsDir := os.Getenv("MARSHAL_SESSIONS_DIR")
	if sessionsDir == "" {
		home, _ := os.UserHomeDir()
		sessionsDir = home + "/.config/marshal/sessions"
	}

	// Find graph in sessions
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("reading sessions directory: %w", err)
	}

	// If graphID is empty, try to find the most recent graph
	if graphID == "" {
		var latestGraph *orchestrator.Graph
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			store, err := orchestrator.CreateSessionGraphStore(entry.Name(), sessionsDir)
			if err != nil {
				continue
			}

			ids, _ := store.List("", 10)
			store.Close()

			for _, id := range ids {
				g, err := loadGraphFromSession(id, entry.Name(), sessionsDir)
				if err != nil {
					continue
				}
				if latestGraph == nil || g.UpdatedAt.After(latestGraph.UpdatedAt) {
					latestGraph = g
				}
			}
		}

		if latestGraph == nil {
			return nil, fmt.Errorf("no graphs found")
		}
		return latestGraph, nil
	}

	// Try to find graph across all sessions
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		g, err := loadGraphFromSession(graphID, entry.Name(), sessionsDir)
		if err == nil {
			return g, nil
		}
	}

	return nil, fmt.Errorf("graph not found: %s", graphID)
}

// loadGraphFromSession loads a specific graph from a session.
func loadGraphFromSession(graphID, sessionID, sessionsDir string) (*orchestrator.Graph, error) {
	store, err := orchestrator.CreateSessionGraphStore(sessionID, sessionsDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	return store.Load(graphID)
}

// getGraphStore creates a graph store for a session.
func getGraphStore(sessionID string) (*orchestrator.GraphStore, error) {
	sessionsDir := os.Getenv("MARSHAL_SESSIONS_DIR")
	if sessionsDir == "" {
		home, _ := os.UserHomeDir()
		sessionsDir = home + "/.config/marshal/sessions"
	}

	return orchestrator.CreateSessionGraphStore(sessionID, sessionsDir)
}

// statusEmoji returns an emoji for task status.
func statusEmoji(status pipeline.TaskState) string {
	switch status {
	case pipeline.TaskRunning:
		return "🔵"
	case pipeline.TaskPending, pipeline.TaskWaiting, pipeline.TaskBlocked:
		return "⚪"
	case pipeline.TaskPassed:
		return "✅"
	case pipeline.TaskFailed:
		return "❌"
	case pipeline.TaskCancelled:
		return "🚫"
	case pipeline.TaskSuperseded:
		return "🔄"
	default:
		return "⚪"
	}
}
