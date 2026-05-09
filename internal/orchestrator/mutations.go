package orchestrator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/pipeline"
)

// MutationBuilder helps construct graph mutations fluently.
type MutationBuilder struct {
	mutation *pipeline.GraphMutation
}

// NewMutation creates a new mutation builder.
func NewMutation(mutationType pipeline.MutationType) *MutationBuilder {
	return &MutationBuilder{
		mutation: pipeline.NewGraphMutation(mutationType),
	}
}

// ForTask sets the target task ID for remove/update/replace mutations.
func (b *MutationBuilder) ForTask(taskID string) *MutationBuilder {
	b.mutation.TargetID = taskID
	return b
}

// WithSpec sets the new task spec for add/update/replace mutations.
func (b *MutationBuilder) WithSpec(spec *pipeline.TaskSpec) *MutationBuilder {
	b.mutation.NewSpec = spec
	return b
}

// WithReason sets the human-readable reason for the mutation.
func (b *MutationBuilder) WithReason(reason string) *MutationBuilder {
	b.mutation.Reason = reason
	return b
}

// WithTrigger sets what caused this mutation.
func (b *MutationBuilder) WithTrigger(trigger string) *MutationBuilder {
	b.mutation.Trigger = trigger
	return b
}

// AddEdge adds a new edge to the mutation (for reorder mutations).
func (b *MutationBuilder) AddEdge(from, to string) *MutationBuilder {
	b.mutation.NewEdges = append(b.mutation.NewEdges, [2]string{from, to})
	return b
}

// RemoveEdge removes an edge in the mutation (for reorder mutations).
func (b *MutationBuilder) RemoveEdge(from, to string) *MutationBuilder {
	b.mutation.RemoveEdges = append(b.mutation.RemoveEdges, [2]string{from, to})
	return b
}

// Build returns the constructed mutation.
func (b *MutationBuilder) Build() *pipeline.GraphMutation {
	b.mutation.Timestamp = time.Now().UTC()
	return b.mutation
}

// MutationEngine provides high-level mutation operations.
type MutationEngine struct {
	graph *Graph
}

// NewMutationEngine creates a mutation engine for a graph.
func NewMutationEngine(g *Graph) *MutationEngine {
	return &MutationEngine{graph: g}
}

// InsertTask adds a new task and optionally connects it to the graph.
func (e *MutationEngine) InsertTask(spec *pipeline.TaskSpec, dependencies []string) error {
	// Validate the task doesn't already exist
	if _, exists := e.graph.GetTask(spec.ID); exists {
		return fmt.Errorf("task %s already exists", spec.ID)
	}

	// Validate all dependencies exist
	for _, dep := range dependencies {
		if _, exists := e.graph.GetTask(dep); !exists {
			return fmt.Errorf("dependency %s does not exist", dep)
		}
	}

	// Set the dependencies
	spec.DependsOn = dependencies

	// Build and apply mutation
	mutation := NewMutation(pipeline.MutationAdd).
		WithSpec(spec).
		WithReason(fmt.Sprintf("Insert new task %s", spec.ID)).
		WithTrigger("manual_insert").
		Build()

	if err := e.graph.ApplyMutation(*mutation); err != nil {
		return fmt.Errorf("failed to insert task: %w", err)
	}

	return nil
}

// RemoveTaskAndRewire removes a task and reconnects its dependents to its dependencies.
// This maintains graph connectivity.
func (e *MutationEngine) RemoveTaskAndRewire(taskID string) error {
	task, exists := e.graph.GetTask(taskID)
	if !exists {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Get what depends on this task
	dependents, err := e.graph.GetDependents(taskID)
	if err != nil {
		return fmt.Errorf("failed to get dependents: %w", err)
	}

	// Build edges to add (dependents -> task's dependencies)
	var newEdges [][2]string
	for _, dependent := range dependents {
		for _, dep := range task.DependsOn {
			newEdges = append(newEdges, [2]string{dep, dependent})
		}
	}

	// Build edges to remove (dependents -> task)
	var removeEdges [][2]string
	for _, dependent := range dependents {
		removeEdges = append(removeEdges, [2]string{taskID, dependent})
	}

	// Apply reorder to rewire
	mutation := NewMutation(pipeline.MutationReorder).
		WithReason(fmt.Sprintf("Remove task %s and rewire dependencies", taskID)).
		WithTrigger("rewire_remove")

	for _, edge := range removeEdges {
		mutation.RemoveEdge(edge[0], edge[1])
	}
	for _, edge := range newEdges {
		mutation.AddEdge(edge[0], edge[1])
	}

	if err := e.graph.ApplyMutation(*mutation.Build()); err != nil {
		return fmt.Errorf("failed to rewire: %w", err)
	}

	// Now remove the task itself
	mutation2 := NewMutation(pipeline.MutationRemove).
		ForTask(taskID).
		WithReason(fmt.Sprintf("Remove task %s after rewiring", taskID)).
		WithTrigger("rewire_remove_complete").
		Build()

	if err := e.graph.ApplyMutation(*mutation2); err != nil {
		return fmt.Errorf("failed to remove task: %w", err)
	}

	return nil
}

// ReplaceTaskSubtree replaces a task and all its downstream dependents.
// Useful when a failed task requires redoing work that depended on it.
func (e *MutationEngine) ReplaceTaskSubtree(oldTaskID string, newSpec *pipeline.TaskSpec, newDependents []*pipeline.TaskSpec) error {
	// First, get all downstream tasks
	downstream := e.findAllDownstream(oldTaskID)

	// Remove all downstream tasks (they'll be replaced)
	for _, taskID := range downstream {
		mutation := NewMutation(pipeline.MutationRemove).
			ForTask(taskID).
			WithReason(fmt.Sprintf("Remove downstream task for replacement of %s", oldTaskID)).
			WithTrigger("subtree_replace").
			Build()

		if err := e.graph.ApplyMutation(*mutation); err != nil {
			return fmt.Errorf("failed to remove downstream task %s: %w", taskID, err)
		}
	}

	// Replace the original task
	mutation := NewMutation(pipeline.MutationReplace).
		ForTask(oldTaskID).
		WithSpec(newSpec).
		WithReason(fmt.Sprintf("Replace task %s with new implementation", oldTaskID)).
		WithTrigger("subtree_replace").
		Build()

	if err := e.graph.ApplyMutation(*mutation); err != nil {
		return fmt.Errorf("failed to replace task: %w", err)
	}

	// Insert new dependent tasks
	for _, spec := range newDependents {
		if err := e.InsertTask(spec, []string{newSpec.ID}); err != nil {
			return fmt.Errorf("failed to insert new dependent %s: %w", spec.ID, err)
		}
	}

	return nil
}

// findAllDownstream finds all tasks that transitively depend on a given task.
func (e *MutationEngine) findAllDownstream(taskID string) []string {
	visited := make(map[string]bool)
	var result []string

	var visit func(string)
	visit = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true

		dependents, _ := e.graph.GetDependents(id)
		for _, dep := range dependents {
			if !visited[dep] {
				result = append(result, dep)
				visit(dep)
			}
		}
	}

	visit(taskID)
	return result
}

// InsertParallelTask inserts a new task parallel to an existing one.
// The new task will have the same dependencies as the reference task.
func (e *MutationEngine) InsertParallelTask(referenceTaskID string, newSpec *pipeline.TaskSpec) error {
	refTask, exists := e.graph.GetTask(referenceTaskID)
	if !exists {
		return fmt.Errorf("reference task %s not found", referenceTaskID)
	}

	return e.InsertTask(newSpec, refTask.DependsOn)
}

// InsertSequentialTask inserts a new task after an existing one.
// The new task depends on the reference task, and tasks that depended on the
// reference task now depend on the new task.
func (e *MutationEngine) InsertSequentialTask(afterTaskID string, newSpec *pipeline.TaskSpec) error {
	// First insert the new task depending only on afterTaskID
	if err := e.InsertTask(newSpec, []string{afterTaskID}); err != nil {
		return err
	}

	// Then redirect dependents of afterTaskID to depend on newSpec
	dependents, err := e.graph.GetDependents(afterTaskID)
	if err != nil {
		return fmt.Errorf("failed to get dependents: %w", err)
	}

	// Build mutation to rewire: dependents now depend on newSpec instead of afterTaskID
	mutation := NewMutation(pipeline.MutationReorder).
		WithReason(fmt.Sprintf("Insert %s after %s", newSpec.ID, afterTaskID)).
		WithTrigger("sequential_insert")

	for _, dependent := range dependents {
		if dependent != newSpec.ID { // Don't create a self-loop
			mutation.RemoveEdge(afterTaskID, dependent)
			mutation.AddEdge(newSpec.ID, dependent)
		}
	}

	if err := e.graph.ApplyMutation(*mutation.Build()); err != nil {
		return fmt.Errorf("failed to rewire dependencies: %w", err)
	}

	return nil
}

// MergeTasks combines multiple tasks into one.
// The merged task gets the union of all dependencies.
func (e *MutationEngine) MergeTasks(mergedID string, taskIDs []string, mergedSpec *pipeline.TaskSpec) error {
	if len(taskIDs) < 2 {
		return fmt.Errorf("need at least 2 tasks to merge")
	}

	// Collect all dependencies
	allDeps := make(map[string]bool)
	for _, taskID := range taskIDs {
		task, exists := e.graph.GetTask(taskID)
		if !exists {
			return fmt.Errorf("task %s not found", taskID)
		}
		for _, dep := range task.DependsOn {
			// Don't depend on other tasks being merged
			isOtherTask := false
			for _, otherID := range taskIDs {
				if dep == otherID {
					isOtherTask = true
					break
				}
			}
			if !isOtherTask {
				allDeps[dep] = true
			}
		}
	}

	// Convert to slice
	deps := make([]string, 0, len(allDeps))
	for dep := range allDeps {
		deps = append(deps, dep)
	}

	// Set the merged spec's ID and dependencies
	mergedSpec.ID = mergedID
	mergedSpec.DependsOn = deps

	// Collect all dependents of tasks being merged
	allDependents := make(map[string]bool)
	for _, taskID := range taskIDs {
		dependents, _ := e.graph.GetDependents(taskID)
		for _, dep := range dependents {
			// Don't include other tasks being merged
			isOtherTask := false
			for _, otherID := range taskIDs {
				if dep == otherID {
					isOtherTask = true
					break
				}
			}
			if !isOtherTask {
				allDependents[dep] = true
			}
		}
	}

	// Remove all tasks being merged
	for _, taskID := range taskIDs {
		mutation := NewMutation(pipeline.MutationRemove).
			ForTask(taskID).
			WithReason(fmt.Sprintf("Remove task for merge into %s", mergedID)).
			WithTrigger("task_merge").
			Build()

		if err := e.graph.ApplyMutation(*mutation); err != nil {
			return fmt.Errorf("failed to remove task %s: %w", taskID, err)
		}
	}

	// Add the merged task
	if err := e.InsertTask(mergedSpec, deps); err != nil {
		return fmt.Errorf("failed to insert merged task: %w", err)
	}

	// Rewire dependents to point to merged task
	mutation := NewMutation(pipeline.MutationReorder).
		WithReason(fmt.Sprintf("Rewire dependents to merged task %s", mergedID)).
		WithTrigger("task_merge")

	for dependent := range allDependents {
		mutation.AddEdge(mergedID, dependent)
	}

	if len(mutation.mutation.NewEdges) > 0 {
		if err := e.graph.ApplyMutation(*mutation.Build()); err != nil {
			return fmt.Errorf("failed to rewire dependents: %w", err)
		}
	}

	return nil
}

// SplitTask divides a task into multiple parallel subtasks.
func (e *MutationEngine) SplitTask(taskID string, subtasks []*pipeline.TaskSpec) error {
	if len(subtasks) == 0 {
		return fmt.Errorf("need at least one subtask")
	}

	parentTask, exists := e.graph.GetTask(taskID)
	if !exists {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Get dependents of the parent task
	dependents, err := e.graph.GetDependents(taskID)
	if err != nil {
		return fmt.Errorf("failed to get dependents: %w", err)
	}

	// Insert all subtasks with parent's dependencies
	for _, subtask := range subtasks {
		if err := e.InsertTask(subtask, parentTask.DependsOn); err != nil {
			return fmt.Errorf("failed to insert subtask %s: %w", subtask.ID, err)
		}
	}

	// Create a merge task that depends on all subtasks
	mergeTask := &pipeline.TaskSpec{
		ID:          taskID + "-merge",
		Role:        parentTask.Role,
		Goal:        fmt.Sprintf("Merge results from %s subtasks", taskID),
		Description: "Synchronization point for split task",
		DependsOn:   make([]string, len(subtasks)),
		Files:       parentTask.Files,
		MaxIterations: 1,
		Timeout:     parentTask.Timeout,
		CreatedAt:   time.Now().UTC(),
		Version:     1,
		Status:      pipeline.TaskPending,
	}
	for i, subtask := range subtasks {
		mergeTask.DependsOn[i] = subtask.ID
	}

	// Extract subtask IDs for dependencies
	subtaskIDs := make([]string, len(subtasks))
	for i, subtask := range subtasks {
		subtaskIDs[i] = subtask.ID
	}
	if err := e.InsertTask(mergeTask, subtaskIDs); err != nil {
		return fmt.Errorf("failed to insert merge task: %w", err)
	}

	// Rewire dependents to depend on merge task instead of parent
	mutation := NewMutation(pipeline.MutationReorder).
		WithReason(fmt.Sprintf("Split task %s into subtasks", taskID)).
		WithTrigger("task_split")

	for _, dependent := range dependents {
		mutation.RemoveEdge(taskID, dependent)
		mutation.AddEdge(mergeTask.ID, dependent)
	}

	if err := e.graph.ApplyMutation(*mutation.Build()); err != nil {
		return fmt.Errorf("failed to rewire dependents: %w", err)
	}

	// Remove the original task
	removeMutation := NewMutation(pipeline.MutationRemove).
		ForTask(taskID).
		WithReason(fmt.Sprintf("Remove original task %s after split", taskID)).
		WithTrigger("task_split_complete").
		Build()

	if err := e.graph.ApplyMutation(*removeMutation); err != nil {
		return fmt.Errorf("failed to remove original task: %w", err)
	}

	return nil
}

// BatchMutations applies multiple mutations atomically.
// If any mutation fails, previous mutations in the batch are rolled back.
func (e *MutationEngine) BatchMutations(mutations []pipeline.GraphMutation) error {
	// Store current state for rollback
	backup := e.graph.Clone()

	for i, mutation := range mutations {
		if err := e.graph.ApplyMutation(mutation); err != nil {
			// Rollback by restoring backup
			e.graph.Tasks = backup.Tasks
			e.graph.Edges = backup.Edges
			e.graph.Version = backup.Version
			// Don't restore history - log the failed attempt
			return fmt.Errorf("mutation %d failed: %w", i, err)
		}
	}

	return nil
}

// ExportMutations serializes all mutations to JSON.
func (e *MutationEngine) ExportMutations() ([]byte, error) {
	e.graph.mu.RLock()
	defer e.graph.mu.RUnlock()

	return json.MarshalIndent(e.graph.History, "", "  ")
}

// ImportMutations reconstructs a graph from mutation history.
func ImportMutations(history []pipeline.GraphMutation) (*Graph, error) {
	if len(history) == 0 {
		return nil, fmt.Errorf("no mutations to import")
	}

	// Find the initial graph creation (first mutation should be adds)
	var sessionID, rootGoal string
	for _, m := range history {
		if m.Type == pipeline.MutationAdd && m.NewSpec != nil {
			// Use first task's creation time as reference
			break
		}
	}

	// Generate new IDs
	graphID := fmt.Sprintf("imported-%d", time.Now().Unix())
	if sessionID == "" {
		sessionID = graphID
	}

	g := NewGraph(graphID, sessionID, rootGoal)

	for i, mutation := range history {
		if err := g.ApplyMutation(mutation); err != nil {
			return nil, fmt.Errorf("mutation %d failed during import: %w", i, err)
		}
	}

	return g, nil
}

// MutationFilter allows filtering mutations by type, reason, etc.
type MutationFilter struct {
	Types   []pipeline.MutationType
	After   *time.Time
	Before  *time.Time
	Trigger string
}

// FilterMutations returns mutations matching the filter criteria.
func (e *MutationEngine) FilterMutations(filter MutationFilter) []pipeline.GraphMutation {
	e.graph.mu.RLock()
	defer e.graph.mu.RUnlock()

	var filtered []pipeline.GraphMutation

	for _, m := range e.graph.History {
		// Check type filter
		if len(filter.Types) > 0 {
			match := false
			for _, t := range filter.Types {
				if m.Type == t {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		// Check time filters
		if filter.After != nil && m.Timestamp.Before(*filter.After) {
			continue
		}
		if filter.Before != nil && m.Timestamp.After(*filter.Before) {
			continue
		}

		// Check trigger filter
		if filter.Trigger != "" && m.Trigger != filter.Trigger {
			continue
		}

		filtered = append(filtered, m)
	}

	return filtered
}
