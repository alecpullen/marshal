// Package planner provides the planning agent that decomposes feature descriptions
// into dependency-ordered task graphs.
package planner

import (
	"fmt"
	"strings"
)

// Task represents a single implementation sub-task in the feature plan.
type Task struct {
	ID                  string   `json:"id"`
	Description         string   `json:"description"`
	FilesLikelyAffected []string `json:"files_likely_affected"`
	DependsOn           []string `json:"depends_on"`
	Skill               string   `json:"skill,omitempty"`
}

// TaskGraph is the top-level planning output returned by the planner agent.
type TaskGraph struct {
	Feature string `json:"feature"`
	Tasks   []Task `json:"tasks"`
}

// ValidateGraph checks structural integrity: no unknown dependency IDs, no cycles.
// Returns a descriptive error with the cycle path if a cycle is detected.
func ValidateGraph(g *TaskGraph) error {
	// Build set of known IDs
	known := make(map[string]bool, len(g.Tasks))
	for _, t := range g.Tasks {
		known[t.ID] = true
	}

	// Check all deps reference known IDs
	for _, t := range g.Tasks {
		for _, dep := range t.DependsOn {
			if !known[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}

	// Build adjacency list and run cycle detection
	adj := make(map[string][]string, len(g.Tasks))
	for _, t := range g.Tasks {
		adj[t.ID] = t.DependsOn
	}

	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	for _, t := range g.Tasks {
		if !visited[t.ID] {
			if cycle := detectCycle(t.ID, adj, visited, inStack, nil); cycle != nil {
				return fmt.Errorf("cycle detected: %s", strings.Join(cycle, " -> "))
			}
		}
	}

	return nil
}

// detectCycle performs DFS-based cycle detection.
// Returns the cycle path as a slice of IDs (including the repeated node), or nil if no cycle.
func detectCycle(id string, adj map[string][]string, visited, inStack map[string]bool, path []string) []string {
	visited[id] = true
	inStack[id] = true
	path = append(path, id)

	for _, dep := range adj[id] {
		if !visited[dep] {
			if cycle := detectCycle(dep, adj, visited, inStack, path); cycle != nil {
				return cycle
			}
		} else if inStack[dep] {
			// Found a back-edge — reconstruct the cycle from dep to current
			start := -1
			for i, node := range path {
				if node == dep {
					start = i
					break
				}
			}
			cycle := append(path[start:], dep) // close the loop
			return cycle
		}
	}

	inStack[id] = false
	return nil
}

// TopologicalSort returns tasks grouped into execution tiers using Kahn's algorithm.
// Each []Task slice is a tier of tasks that can run in parallel (all deps in prior tiers).
// Returns an error if the graph contains a cycle.
func TopologicalSort(g *TaskGraph) ([][]Task, error) {
	if err := ValidateGraph(g); err != nil {
		return nil, err
	}

	// Map ID → Task for quick lookup
	taskByID := make(map[string]Task, len(g.Tasks))
	for _, t := range g.Tasks {
		taskByID[t.ID] = t
	}

	// Compute in-degree (number of unresolved dependencies) for each task
	inDegree := make(map[string]int, len(g.Tasks))
	// Build reverse adjacency: dep → tasks that depend on it
	dependents := make(map[string][]string, len(g.Tasks))
	for _, t := range g.Tasks {
		if _, ok := inDegree[t.ID]; !ok {
			inDegree[t.ID] = 0
		}
		for _, dep := range t.DependsOn {
			inDegree[t.ID]++
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	// Seed queue with all zero-in-degree tasks (preserving original order)
	var queue []string
	for _, t := range g.Tasks {
		if inDegree[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}

	var tiers [][]Task
	processed := 0

	for len(queue) > 0 {
		// Current queue is one tier
		tier := make([]Task, 0, len(queue))
		for _, id := range queue {
			tier = append(tier, taskByID[id])
		}
		tiers = append(tiers, tier)
		processed += len(queue)

		// Build next tier: tasks whose in-degree drops to 0 after processing this tier
		var nextQueue []string
		for _, id := range queue {
			for _, dep := range dependents[id] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					nextQueue = append(nextQueue, dep)
				}
			}
		}
		queue = nextQueue
	}

	if processed != len(g.Tasks) {
		// Shouldn't happen since ValidateGraph passed, but be defensive
		return nil, fmt.Errorf("cycle detected during topological sort")
	}

	return tiers, nil
}
