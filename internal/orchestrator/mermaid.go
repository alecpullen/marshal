package orchestrator

import (
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/pipeline"
)

// MermaidOptions controls diagram generation.
type MermaidOptions struct {
	// ShowStatusColors colors nodes based on task status
	ShowStatusColors bool
	// ShowLabels shows task descriptions as labels
	ShowLabels bool
	// Compact uses shorter node IDs
	Compact bool
	// Direction is the flow direction (TD, LR, BT, RL)
	Direction string
	// MaxNodes limits the number of nodes shown (0 = unlimited)
	MaxNodes int
	// HighlightPath highlights the critical path
	HighlightPath bool
	// GroupByTier groups nodes by execution tier
	GroupByTier bool
}

// DefaultMermaidOptions returns sensible defaults.
func DefaultMermaidOptions() MermaidOptions {
	return MermaidOptions{
		ShowStatusColors: true,
		ShowLabels:       true,
		Compact:          false,
		Direction:        "TD", // Top-down
		MaxNodes:         0,
		HighlightPath:    false,
		GroupByTier:      false,
	}
}

// MermaidGenerator creates Mermaid flowchart diagrams.
type MermaidGenerator struct {
	graph   *Graph
	options MermaidOptions
}

// NewMermaidGenerator creates a diagram generator.
func NewMermaidGenerator(g *Graph, opts MermaidOptions) *MermaidGenerator {
	return &MermaidGenerator{
		graph:   g,
		options: opts,
	}
}

// Generate creates the full Mermaid diagram.
func (g *MermaidGenerator) Generate() string {
	g.graph.mu.RLock()
	defer g.graph.mu.RUnlock()

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("flowchart %s\n", g.options.Direction))

	// Title/subgraph with goal
	if g.graph.RootGoal != "" {
		sb.WriteString(fmt.Sprintf("\tsubgraph \"%s\"\n", truncate(g.graph.RootGoal, 40)))
	}

	// Generate nodes
	nodes := g.generateNodes()

	// Apply max nodes limit
	if g.options.MaxNodes > 0 && len(nodes) > g.options.MaxNodes {
		nodes = nodes[:g.options.MaxNodes]
		sb.WriteString("\t\t%% Note: Diagram truncated to " + fmt.Sprintf("%d", g.options.MaxNodes) + " nodes\n")
	}

	for _, node := range nodes {
		sb.WriteString(node)
	}

	// Generate edges
	edges := g.generateEdges(nodes)
	for _, edge := range edges {
		sb.WriteString(edge)
	}

	// Close subgraph
	if g.graph.RootGoal != "" {
		sb.WriteString("\tend\n")
	}

	// Add legend
	if g.options.ShowStatusColors {
		sb.WriteString("\n\t%% Legend\n")
		sb.WriteString(fmt.Sprintf("\tstyle %s fill:#3498db\n", g.nodeID("legend-running")))
		sb.WriteString(fmt.Sprintf("\tstyle %s fill:#95a5a6\n", g.nodeID("legend-pending")))
		sb.WriteString(fmt.Sprintf("\tstyle %s fill:#2ecc71\n", g.nodeID("legend-passed")))
		sb.WriteString(fmt.Sprintf("\tstyle %s fill:#e74c3c\n", g.nodeID("legend-failed")))
	}

	return sb.String()
}

// generateNodes creates node definitions.
func (g *MermaidGenerator) generateNodes() []string {
	var nodes []string

	taskOrder := g.getTaskOrder()

	for _, taskID := range taskOrder {
		task, ok := g.graph.Tasks[taskID]
		if !ok {
			continue
		}

		nodeDef := g.formatNode(task)
		nodes = append(nodes, nodeDef)

		// Add style directive if showing colors
		if g.options.ShowStatusColors {
			style := g.getNodeStyle(task)
			if style != "" {
				nodes = append(nodes, fmt.Sprintf("\tstyle %s %s\n", g.nodeID(task.ID), style))
			}
		}
	}

	return nodes
}

// formatNode creates a single node definition.
func (g *MermaidGenerator) formatNode(task *pipeline.TaskSpec) string {
	id := g.nodeID(task.ID)

	if g.options.ShowLabels && task.Description != "" {
		label := g.formatLabel(task)
		return fmt.Sprintf("\t%s[%s]\n", id, label)
	}

	return fmt.Sprintf("\t%s[%s]\n", id, task.ID)
}

// formatLabel creates a rich label with status emoji and description.
func (g *MermaidGenerator) formatLabel(task *pipeline.TaskSpec) string {
	emoji := g.getStatusEmoji(task.Status)
	desc := truncate(task.Description, 30)

	if g.options.Compact {
		return fmt.Sprintf("%s %s", emoji, desc)
	}

	return fmt.Sprintf("%s %s: %s", emoji, task.ID, desc)
}

// getStatusEmoji returns an emoji for task status.
func (g *MermaidGenerator) getStatusEmoji(status pipeline.TaskState) string {
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

// getNodeStyle returns CSS style for a node based on status.
func (g *MermaidGenerator) getNodeStyle(task *pipeline.TaskSpec) string {
	switch task.Status {
	case pipeline.TaskRunning:
		return "fill:#3498db,stroke:#2980b9,stroke-width:2px"
	case pipeline.TaskPending, pipeline.TaskWaiting, pipeline.TaskBlocked:
		return "fill:#95a5a6,stroke:#7f8c8d"
	case pipeline.TaskPassed:
		return "fill:#2ecc71,stroke:#27ae60"
	case pipeline.TaskFailed:
		return "fill:#e74c3c,stroke:#c0392b,stroke-width:2px"
	case pipeline.TaskCancelled:
		return "fill:#f39c12,stroke:#e67e22"
	case pipeline.TaskSuperseded:
		return "fill:#9b59b6,stroke:#8e44ad,stroke-dasharray: 5 5"
	default:
		return ""
	}
}

// generateEdges creates connection definitions.
func (g *MermaidGenerator) generateEdges(definedNodes []string) []string {
	var edges []string
	definedSet := make(map[string]bool)

	for _, node := range definedNodes {
		// Extract node ID from definition
		parts := strings.Fields(node)
		if len(parts) >= 2 {
			definedSet[parts[0]] = true
		}
	}

	for taskID, deps := range g.graph.Edges {
		// Skip if task not in our node set
		taskNodeID := g.nodeID(taskID)
		if !definedSet[taskNodeID] {
			continue
		}

		for _, dep := range deps {
			depNodeID := g.nodeID(dep)
			if !definedSet[depNodeID] {
				continue
			}

			edge := fmt.Sprintf("\t%s --> %s\n", depNodeID, taskNodeID)
			edges = append(edges, edge)
		}
	}

	return edges
}

// getTaskOrder returns tasks in a consistent order.
func (g *MermaidGenerator) getTaskOrder() []string {
	if g.options.GroupByTier {
		return g.getTieredOrder()
	}

	// Simple alphabetical order
	var order []string
	for id := range g.graph.Tasks {
		order = append(order, id)
	}
	return order
}

// getTieredOrder returns tasks grouped by execution tier.
func (g *MermaidGenerator) getTieredOrder() []string {
	tiers := g.graph.TopologicalTiers()

	var order []string
	for _, tier := range tiers {
		for _, taskID := range tier {
			order = append(order, taskID)
		}
	}

	return order
}

// nodeID sanitizes a task ID for Mermaid.
func (g *MermaidGenerator) nodeID(taskID string) string {
	// Mermaid IDs should be alphanumeric and underscores only
	id := strings.ReplaceAll(taskID, "-", "_")
	id = strings.ReplaceAll(id, ".", "_")
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, " ", "_")

	// Ensure it starts with a letter
	if len(id) > 0 && !isLetter(id[0]) {
		id = "n" + id
	}

	return id
}

// isLetter checks if a byte is a letter.
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// truncate shortens a string to maxLen, adding ellipsis if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// GenerateSimple creates a simple text-based tree diagram.
func (g *MermaidGenerator) GenerateSimple() string {
	g.graph.mu.RLock()
	defer g.graph.mu.RUnlock()

	var sb strings.Builder

	// Header with stats
	stats := g.graph.Stats()
	sb.WriteString(fmt.Sprintf("Task Graph: %s\n", g.graph.RootGoal))
	sb.WriteString(fmt.Sprintf("Status: %s | Tasks: %d | Version: %d\n", stats.Status, stats.TotalTasks, stats.GraphVersion))
	sb.WriteString(strings.Repeat("=", 50) + "\n\n")

	// Generate tier view
	tiers := g.graph.TopologicalTiers()
	for i, tier := range tiers {
		sb.WriteString(fmt.Sprintf("Tier %d:\n", i+1))
		for _, taskID := range tier {
			task, ok := g.graph.Tasks[taskID]
			if !ok {
				continue
			}

			emoji := g.getStatusEmoji(task.Status)
			sb.WriteString(fmt.Sprintf("  %s %s: %s\n", emoji, task.ID, truncate(task.Description, 40)))

			// Show dependencies
			if len(task.DependsOn) > 0 {
				sb.WriteString(fmt.Sprintf("     (depends on: %s)\n", strings.Join(task.DependsOn, ", ")))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// GenerateASCII creates an ASCII art tree diagram.
func (g *MermaidGenerator) GenerateASCII() string {
	g.graph.mu.RLock()
	defer g.graph.mu.RUnlock()

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Task Graph: %s\n", g.graph.RootGoal))
	sb.WriteString(strings.Repeat("=", 50) + "\n")

	// Find root nodes (no dependencies)
	var roots []string
	for id := range g.graph.Tasks {
		if len(g.graph.Edges[id]) == 0 {
			roots = append(roots, id)
		}
	}

	// Print tree from each root
	visited := make(map[string]bool)
	for i, root := range roots {
		if i > 0 {
			sb.WriteString("\n")
		}
		g.printTree(&sb, root, "", visited, true)
	}

	return sb.String()
}

// printTree recursively prints a tree node.
func (g *MermaidGenerator) printTree(sb *strings.Builder, taskID, prefix string, visited map[string]bool, isLast bool) {
	if visited[taskID] {
		return // Avoid cycles in display
	}
	visited[taskID] = true

	task, ok := g.graph.Tasks[taskID]
	if !ok {
		return
	}

	// Print current node
	connector := "├── "
	if isLast {
		connector = "└── "
	}

	if prefix == "" {
		connector = ""
	}

	emoji := g.getStatusEmoji(task.Status)
	sb.WriteString(fmt.Sprintf("%s%s%s: %s\n", prefix, connector, emoji, task.ID))

	// Find dependents
	dependents, _ := g.graph.GetDependents(taskID)

	// Print children
	newPrefix := prefix
	if prefix != "" {
		if isLast {
			newPrefix += "    "
		} else {
			newPrefix += "│   "
		}
	}

	for i, dep := range dependents {
		g.printTree(sb, dep, newPrefix, visited, i == len(dependents)-1)
	}
}

// ExportMarkdown creates a markdown file with the diagram.
func (g *MermaidGenerator) ExportMarkdown() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Task Graph: %s\n\n", g.graph.RootGoal))

	// Stats table
	stats := g.graph.Stats()
	sb.WriteString("## Statistics\n\n")
	sb.WriteString(fmt.Sprintf("- **Total Tasks**: %d\n", stats.TotalTasks))
	sb.WriteString(fmt.Sprintf("- **Status**: %s\n", stats.Status))
	sb.WriteString(fmt.Sprintf("- **Version**: %d\n", stats.GraphVersion))
	sb.WriteString(fmt.Sprintf("- **Passed**: %d | **Failed**: %d | **Running**: %d | **Pending**: %d\n\n",
		stats.CompletedTasks, stats.FailedTasks, stats.RunningTasks, stats.PendingTasks))

	// Mermaid diagram
	sb.WriteString("## Dependency Graph\n\n")
	sb.WriteString("```mermaid\n")
	sb.WriteString(g.Generate())
	sb.WriteString("```\n\n")

	// Task list
	sb.WriteString("## Tasks\n\n")
	sb.WriteString("| ID | Role | Status | Description |\n")
	sb.WriteString("|---|---|---|---|\n")

	tasks := g.graph.AllTasks()
	for _, task := range tasks {
		status := pipeline.TaskStateString(task.Status)
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			task.ID, task.Role, status, truncate(task.Description, 50)))
	}

	// Mutation history (last 10)
	if len(g.graph.History) > 0 {
		sb.WriteString("\n## Recent Mutations\n\n")
		sb.WriteString("| # | Type | Target | Reason | Time |\n")
		sb.WriteString("|---|---|---|---|---|\n")

		start := len(g.graph.History) - 10
		if start < 0 {
			start = 0
		}
		for i, m := range g.graph.History[start:] {
			target := m.TargetID
			if target == "" && m.NewSpec != nil {
				target = m.NewSpec.ID
			}
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s |\n",
				start+i+1, m.Type, target, truncate(m.Reason, 30), m.Timestamp.Format("15:04:05")))
		}
	}

	return sb.String()
}
