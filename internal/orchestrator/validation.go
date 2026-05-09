package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/pipeline"
)

// ValidationResult contains the outcome of graph validation.
type ValidationResult struct {
	Valid       bool
	Errors      []ValidationError
	Warnings    []ValidationWarning
}

// ValidationError represents a critical issue that prevents execution.
type ValidationError struct {
	Type    string
	TaskID  string
	Message string
}

// ValidationWarning represents a non-critical issue.
type ValidationWarning struct {
	Type    string
	TaskID  string
	Message string
}

// String returns a human-readable summary.
func (r ValidationResult) String() string {
	if r.Valid && len(r.Warnings) == 0 {
		return "Graph is valid"
	}

	var sb strings.Builder
	if !r.Valid {
		sb.WriteString(fmt.Sprintf("Graph has %d error(s):\n", len(r.Errors)))
		for _, e := range r.Errors {
			sb.WriteString(fmt.Sprintf("  [ERROR] %s (task: %s): %s\n", e.Type, e.TaskID, e.Message))
		}
	}
	if len(r.Warnings) > 0 {
		sb.WriteString(fmt.Sprintf("Graph has %d warning(s):\n", len(r.Warnings)))
		for _, w := range r.Warnings {
			sb.WriteString(fmt.Sprintf("  [WARN] %s (task: %s): %s\n", w.Type, w.TaskID, w.Message))
		}
	}
	return sb.String()
}

// Validator performs comprehensive graph validation.
type Validator struct {
	strict bool // If true, warnings become errors
}

// NewValidator creates a new validator.
func NewValidator(strict bool) *Validator {
	return &Validator{strict: strict}
}

// Validate performs full graph validation.
func (v *Validator) Validate(g *Graph) ValidationResult {
	result := ValidationResult{Valid: true}

	// Run all validation checks
	v.validateTaskExistence(g, &result)
	v.validateEdges(g, &result)
	v.validateCycles(g, &result)
	v.validateOrphanTasks(g, &result)
	v.validateDeadEndTasks(g, &result)
	v.validateTerminalTasks(g, &result)
	v.validateTaskSpecs(g, &result)

	// If strict mode, convert warnings to errors
	if v.strict && len(result.Warnings) > 0 {
		for _, w := range result.Warnings {
			result.Errors = append(result.Errors, ValidationError{
				Type:    w.Type,
				TaskID:  w.TaskID,
				Message: w.Message,
			})
		}
		result.Warnings = nil
	}

	result.Valid = len(result.Errors) == 0
	return result
}

// validateTaskExistence checks that all referenced tasks exist.
func (v *Validator) validateTaskExistence(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for taskID, deps := range g.Edges {
		if _, ok := g.Tasks[taskID]; !ok {
			r.Errors = append(r.Errors, ValidationError{
				Type:    "OrphanedEdge",
				TaskID:  taskID,
				Message: "Edge entry exists but task does not",
			})
		}

		for _, dep := range deps {
			if _, ok := g.Tasks[dep]; !ok {
				r.Errors = append(r.Errors, ValidationError{
					Type:    "MissingDependency",
					TaskID:  taskID,
					Message: fmt.Sprintf("Depends on non-existent task: %s", dep),
				})
			}
		}
	}
}

// validateEdges checks edge consistency.
func (v *Validator) validateEdges(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Check for self-references
	for taskID, deps := range g.Edges {
		for _, dep := range deps {
			if dep == taskID {
				r.Errors = append(r.Errors, ValidationError{
					Type:    "SelfDependency",
					TaskID:  taskID,
					Message: "Task cannot depend on itself",
				})
			}
		}
	}

	// Check for duplicate dependencies
	for taskID, deps := range g.Edges {
		seen := make(map[string]bool)
		for _, dep := range deps {
			if seen[dep] {
				r.Warnings = append(r.Warnings, ValidationWarning{
					Type:    "DuplicateDependency",
					TaskID:  taskID,
					Message: fmt.Sprintf("Duplicate dependency on %s", dep),
				})
			}
			seen[dep] = true
		}
	}
}

// validateCycles detects circular dependencies using DFS.
func (v *Validator) validateCycles(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := []string{}

	var checkCycle func(string) (bool, []string)
	checkCycle = func(taskID string) (bool, []string) {
		visited[taskID] = true
		recStack[taskID] = true
		path = append(path, taskID)

		deps := g.Edges[taskID]
		for _, dep := range deps {
			if !visited[dep] {
				if found, cyclePath := checkCycle(dep); found {
					return true, cyclePath
				}
			} else if recStack[dep] {
				// Found a cycle - extract the cycle from path
				cycleStart := -1
				for i, id := range path {
					if id == dep {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cycle := append(path[cycleStart:], taskID)
					return true, cycle
				}
				return true, []string{dep, taskID}
			}
		}

		path = path[:len(path)-1]
		recStack[taskID] = false
		return false, nil
	}

	for id := range g.Tasks {
		if !visited[id] {
			if found, cycle := checkCycle(id); found {
				r.Errors = append(r.Errors, ValidationError{
					Type:    "CircularDependency",
					TaskID:  cycle[0],
					Message: fmt.Sprintf("Cycle detected: %s", strings.Join(cycle, " -> ")),
				})
				// Clear path for next iteration
				path = []string{}
			}
		}
	}
}

// validateOrphanTasks detects tasks with no connections.
func (v *Validator) validateOrphanTasks(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Find tasks with no dependencies and no dependents
	for id := range g.Tasks {
		hasDeps := len(g.Edges[id]) > 0
		hasDependents := false

		for _, deps := range g.Edges {
			for _, dep := range deps {
				if dep == id {
					hasDependents = true
					break
				}
			}
			if hasDependents {
				break
			}
		}

		if !hasDeps && !hasDependents && len(g.Tasks) > 1 {
			r.Warnings = append(r.Warnings, ValidationWarning{
				Type:    "IsolatedTask",
				TaskID:  id,
				Message: "Task has no dependencies and no dependents - will execute in parallel with all other root tasks",
			})
		}
	}
}

// validateDeadEndTasks detects tasks that would prevent graph completion.
func (v *Validator) validateDeadEndTasks(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Find tasks with failed dependencies that would block execution
	for id, task := range g.Tasks {
		if task.Status == pipeline.TaskFailed {
			// Check if this failure blocks other tasks
			dependents, _ := g.GetDependents(id)
			if len(dependents) > 0 {
				r.Warnings = append(r.Warnings, ValidationWarning{
					Type:    "BlockingFailure",
					TaskID:  id,
					Message: fmt.Sprintf("Failed task blocks %d dependent task(s): %v", len(dependents), dependents),
				})
			}
		}
	}
}

// validateTerminalTasks checks for inconsistent terminal states.
func (v *Validator) validateTerminalTasks(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for id, task := range g.Tasks {
		if !task.Status.IsTerminal() {
			continue
		}

		// Check for completed tasks with incomplete dependents
		if task.Status == pipeline.TaskPassed {
			deps, _ := g.GetDependencies(id)
			for _, depID := range deps {
				if dep, ok := g.Tasks[depID]; ok {
					if !dep.Status.IsTerminal() {
						r.Warnings = append(r.Warnings, ValidationWarning{
							Type:    "PrematureCompletion",
							TaskID:  id,
							Message: fmt.Sprintf("Task completed but dependency %s is still in %v state", depID, dep.Status),
						})
					} else if dep.Status == pipeline.TaskFailed {
						r.Errors = append(r.Errors, ValidationError{
							Type:    "InvalidCompletion",
							TaskID:  id,
							Message: fmt.Sprintf("Task completed but dependency %s failed", depID),
						})
					}
				}
			}
		}

		// Check for terminal state without timestamps
		if task.Status == pipeline.TaskPassed || task.Status == pipeline.TaskFailed {
			if task.CompletedAt == nil {
				r.Warnings = append(r.Warnings, ValidationWarning{
					Type:    "MissingTimestamp",
					TaskID:  id,
					Message: "Task in terminal state but missing completion timestamp",
				})
			}
		}
	}
}

// validateTaskSpecs validates individual task specifications.
func (v *Validator) validateTaskSpecs(g *Graph, r *ValidationResult) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for id, task := range g.Tasks {
		// Validate required fields
		if task.Role == "" {
			r.Errors = append(r.Errors, ValidationError{
				Type:    "MissingRole",
				TaskID:  id,
				Message: "Task has no role assigned",
			})
		}

		if task.Goal == "" {
			r.Errors = append(r.Errors, ValidationError{
				Type:    "MissingGoal",
				TaskID:  id,
				Message: "Task has no goal",
			})
		}

		// Validate dependency references match Edges
		edgeDeps := g.Edges[id]
		if len(task.DependsOn) != len(edgeDeps) {
			r.Warnings = append(r.Warnings, ValidationWarning{
				Type:    "DependencyMismatch",
				TaskID:  id,
				Message: fmt.Sprintf("Task.DependsOn has %d entries but graph.Edges has %d", len(task.DependsOn), len(edgeDeps)),
			})
		}

		// Check for timeout that seems unreasonable
		if task.Timeout > 30*time.Minute {
			r.Warnings = append(r.Warnings, ValidationWarning{
				Type:    "LongTimeout",
				TaskID:  id,
				Message: fmt.Sprintf("Timeout is %v, which is unusually long", task.Timeout),
			})
		}

		// Validate iteration limits
		if task.MaxIterations < 1 {
			r.Errors = append(r.Errors, ValidationError{
				Type:    "InvalidIterations",
				TaskID:  id,
				Message: "MaxIterations must be at least 1",
			})
		}
		if task.MaxIterations > 10 {
			r.Warnings = append(r.Warnings, ValidationWarning{
				Type:    "HighIterations",
				TaskID:  id,
				Message: fmt.Sprintf("MaxIterations is %d, which may cause long execution times", task.MaxIterations),
			})
		}
	}
}

// QuickValidate performs a fast validation (cycles and existence only).
func QuickValidate(g *Graph) error {
	v := NewValidator(false)
	result := v.Validate(g)
	if !result.Valid {
		return fmt.Errorf("graph validation failed: %s", result.Errors[0].Message)
	}
	return nil
}
