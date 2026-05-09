// Package orchestrator implements the task graph orchestration for Marshal.
// Phase 4a: Static Task Graph with versioning, mutations, and Mermaid output.
package orchestrator

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/alecpullen/marshal/internal/pipeline"
)

// GraphStatus represents the execution status of a task graph.
type GraphStatus string

const (
	GraphPlanning   GraphStatus = "planning"
	GraphReady      GraphStatus = "ready"
	GraphRunning    GraphStatus = "running"
	GraphPaused     GraphStatus = "paused"
	GraphCompleted  GraphStatus = "completed"
	GraphFailed     GraphStatus = "failed"
	GraphReplanning GraphStatus = "replanning"
)

// Graph represents a dependency graph of tasks with versioning and mutation history.
type Graph struct {
	ID        string                      // Session-scoped UUID
	SessionID string                      // Parent session ID
	RootGoal  string                      // Original user goal
	Tasks     map[string]*pipeline.TaskSpec
	Edges     map[string][]string         // task ID -> dependent task IDs (reverse deps)
	Version   int                         // Incremented on each mutation
	History   []pipeline.GraphMutation    // Audit trail of all changes
	Status    GraphStatus                 // Current execution status
	CreatedAt time.Time
	UpdatedAt time.Time
	Config    ReplanConfig                // Replanning behavior configuration

	mu sync.RWMutex // Protects concurrent access
}

// ReplanConfig controls automatic and manual replanning behavior.
type ReplanConfig struct {
	OnFailure        bool // Auto-replan on task failure (default: true)
	OnCriticFeedback bool // Replan when critic suggests changes (default: true)
	ManualOnly       bool // Only replan on explicit /replan command (default: false)
	MaxReplans       int  // Hard limit per graph (default: 5)
	CostBudgetCents  int  // Budget for replanning (default: 50 cents)
}

// DefaultReplanConfig returns sensible defaults.
func DefaultReplanConfig() ReplanConfig {
	return ReplanConfig{
		OnFailure:        true,
		OnCriticFeedback: true,
		ManualOnly:       false,
		MaxReplans:       5,
		CostBudgetCents:  50,
	}
}

// NewGraph creates a new empty graph.
func NewGraph(id, sessionID, rootGoal string) *Graph {
	now := time.Now().UTC()
	return &Graph{
		ID:        id,
		SessionID: sessionID,
		RootGoal:  rootGoal,
		Tasks:     make(map[string]*pipeline.TaskSpec),
		Edges:     make(map[string][]string),
		Version:   1,
		History:   []pipeline.GraphMutation{},
		Status:    GraphPlanning,
		CreatedAt: now,
		UpdatedAt: now,
		Config:    DefaultReplanConfig(),
	}
}

// AddTask adds a task to the graph.
func (g *Graph) AddTask(task *pipeline.TaskSpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if task == nil {
		return fmt.Errorf("cannot add nil task")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID is required")
	}
	if _, exists := g.Tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	// Initialize edges entry
	if _, ok := g.Edges[task.ID]; !ok {
		g.Edges[task.ID] = []string{}
	}

	g.Tasks[task.ID] = task
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// RemoveTask removes a task and all its edges.
func (g *Graph) RemoveTask(taskID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.Tasks[taskID]; !exists {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Remove from all dependency edges
	for id, deps := range g.Edges {
		var newDeps []string
		for _, dep := range deps {
			if dep != taskID {
				newDeps = append(newDeps, dep)
			}
		}
		g.Edges[id] = newDeps
	}

	// Remove the task itself
	delete(g.Tasks, taskID)
	delete(g.Edges, taskID)

	g.UpdatedAt = time.Now().UTC()
	return nil
}

// AddEdge adds a dependency edge: from -> to (to depends on from).
func (g *Graph) AddEdge(from, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Validate both tasks exist
	if _, ok := g.Tasks[from]; !ok {
		return fmt.Errorf("source task %s not found", from)
	}
	if _, ok := g.Tasks[to]; !ok {
		return fmt.Errorf("target task %s not found", to)
	}

	// Check for self-dependency
	if from == to {
		return fmt.Errorf("task cannot depend on itself")
	}

	// Check if edge already exists
	for _, dep := range g.Edges[to] {
		if dep == from {
			return nil // Already exists, idempotent
		}
	}

	// Add the edge
	g.Edges[to] = append(g.Edges[to], from)
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// RemoveEdge removes a dependency edge.
func (g *Graph) RemoveEdge(from, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	deps, ok := g.Edges[to]
	if !ok {
		return fmt.Errorf("task %s has no dependencies", to)
	}

	var newDeps []string
	found := false
	for _, dep := range deps {
		if dep != from {
			newDeps = append(newDeps, dep)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("edge %s -> %s not found", from, to)
	}

	g.Edges[to] = newDeps
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// Ready returns tasks that are ready to execute (all dependencies satisfied).
func (g *Graph) Ready() []*pipeline.TaskSpec {
	g.mu.RLock()
	defer g.mu.RUnlock()

	completed := g.completedTasks()
	var ready []*pipeline.TaskSpec

	for _, task := range g.Tasks {
		// Skip non-pending tasks
		if task.Status != pipeline.TaskPending {
			continue
		}

		// Check if all dependencies are completed
		deps := g.Edges[task.ID]
		allSatisfied := true
		for _, dep := range deps {
			if !completed[dep] {
				allSatisfied = false
				break
			}
		}

		if allSatisfied {
			ready = append(ready, task)
		}
	}

	// Sort for deterministic ordering
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].ID < ready[j].ID
	})

	return ready
}

// completedTasks returns a set of task IDs that are in terminal states.
func (g *Graph) completedTasks() map[string]bool {
	completed := make(map[string]bool)
	for id, task := range g.Tasks {
		if task.Status.IsTerminal() {
			completed[id] = true
		}
	}
	return completed
}

// TopologicalTiers returns tasks grouped by execution tier.
// Tasks in the same tier have no dependencies between them.
func (g *Graph) TopologicalTiers() [][]string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Calculate in-degree for each task
	inDegree := make(map[string]int)
	for id := range g.Tasks {
		inDegree[id] = 0
	}
	for _, deps := range g.Edges {
		for _, dep := range deps {
			if _, ok := g.Tasks[dep]; ok {
				// Count how many tasks depend on this one
				inDegree[dep]++
			}
		}
	}

	// For tier calculation, we need the actual dependency direction
	// Edges[to] = [from1, from2] means to depends on from1 and from2
	// So we need inDegree based on Edges
	actualInDegree := make(map[string]int)
	for id := range g.Tasks {
		actualInDegree[id] = 0
	}
	for taskID, deps := range g.Edges {
		for range deps {
			actualInDegree[taskID]++
		}
	}

	var tiers [][]string
	remaining := len(g.Tasks)
	visited := make(map[string]bool)

	for remaining > 0 {
		var tier []string
		for id := range g.Tasks {
			if actualInDegree[id] == 0 && !visited[id] {
				tier = append(tier, id)
			}
		}

		if len(tier) == 0 && remaining > 0 {
			// Circular dependency detected
			break
		}

		// Mark tier tasks as visited and reduce in-degrees
		for _, id := range tier {
			visited[id] = true
			remaining--
			// For each task that depends on this one, reduce its in-degree
			for otherID, deps := range g.Edges {
				for _, dep := range deps {
					if dep == id {
						actualInDegree[otherID]--
					}
				}
			}
		}

		if len(tier) > 0 {
			tiers = append(tiers, tier)
		}
	}

	return tiers
}

// ApplyMutation applies a graph mutation and increments version.
func (g *Graph) ApplyMutation(m pipeline.GraphMutation) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	m.Timestamp = time.Now().UTC()

	switch m.Type {
	case pipeline.MutationAdd:
		if m.NewSpec == nil {
			return fmt.Errorf("add mutation requires NewSpec")
		}
		if _, exists := g.Tasks[m.NewSpec.ID]; exists {
			return fmt.Errorf("task %s already exists", m.NewSpec.ID)
		}
		g.Tasks[m.NewSpec.ID] = m.NewSpec
		g.Edges[m.NewSpec.ID] = m.NewSpec.DependsOn

	case pipeline.MutationRemove:
		if m.TargetID == "" {
			return fmt.Errorf("remove mutation requires TargetID")
		}
		if _, exists := g.Tasks[m.TargetID]; !exists {
			return fmt.Errorf("task %s not found", m.TargetID)
		}
		// Remove from dependency edges
		for id, deps := range g.Edges {
			var newDeps []string
			for _, dep := range deps {
				if dep != m.TargetID {
					newDeps = append(newDeps, dep)
				}
			}
			g.Edges[id] = newDeps
		}
		delete(g.Tasks, m.TargetID)
		delete(g.Edges, m.TargetID)

	case pipeline.MutationUpdate:
		if m.TargetID == "" || m.NewSpec == nil {
			return fmt.Errorf("update mutation requires TargetID and NewSpec")
		}
		if _, exists := g.Tasks[m.TargetID]; !exists {
			return fmt.Errorf("task %s not found", m.TargetID)
		}
		m.NewSpec.Version = g.Tasks[m.TargetID].Version + 1
		m.NewSpec.ParentID = m.TargetID
		g.Tasks[m.TargetID] = m.NewSpec
		g.Edges[m.TargetID] = m.NewSpec.DependsOn

	case pipeline.MutationReplace:
		if m.TargetID == "" || m.NewSpec == nil {
			return fmt.Errorf("replace mutation requires TargetID and NewSpec")
		}
		if _, exists := g.Tasks[m.TargetID]; !exists {
			return fmt.Errorf("task %s not found", m.TargetID)
		}
		// Remove old task
		for id, deps := range g.Edges {
			var newDeps []string
			for _, dep := range deps {
				if dep != m.TargetID {
					newDeps = append(newDeps, dep)
				}
			}
			g.Edges[id] = newDeps
		}
		delete(g.Tasks, m.TargetID)
		delete(g.Edges, m.TargetID)
		// Add new task
		m.NewSpec.ParentID = m.TargetID
		g.Tasks[m.NewSpec.ID] = m.NewSpec
		g.Edges[m.NewSpec.ID] = m.NewSpec.DependsOn

	case pipeline.MutationReorder:
		// Reorder edges based on NewEdges and RemoveEdges
		for _, edge := range m.RemoveEdges {
			if len(edge) == 2 {
				g.removeEdgeUnsafe(edge[0], edge[1])
			}
		}
		for _, edge := range m.NewEdges {
			if len(edge) == 2 {
				g.addEdgeUnsafe(edge[0], edge[1])
			}
		}

	default:
		return fmt.Errorf("unknown mutation type: %s", m.Type)
	}

	g.History = append(g.History, m)
	g.Version++
	g.UpdatedAt = time.Now().UTC()

	return nil
}

// addEdgeUnsafe adds an edge without locking (internal use only).
func (g *Graph) addEdgeUnsafe(from, to string) {
	for _, dep := range g.Edges[to] {
		if dep == from {
			return // Already exists
		}
	}
	g.Edges[to] = append(g.Edges[to], from)
}

// removeEdgeUnsafe removes an edge without locking (internal use only).
func (g *Graph) removeEdgeUnsafe(from, to string) {
	deps, ok := g.Edges[to]
	if !ok {
		return
	}
	var newDeps []string
	for _, dep := range deps {
		if dep != from {
			newDeps = append(newDeps, dep)
		}
	}
	g.Edges[to] = newDeps
}

// Validate checks the graph for cycles and orphaned tasks.
func (g *Graph) Validate() error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Check for orphaned dependencies
	for taskID, deps := range g.Edges {
		if _, ok := g.Tasks[taskID]; !ok {
			return fmt.Errorf("orphaned edge entry for non-existent task: %s", taskID)
		}
		for _, dep := range deps {
			if _, ok := g.Tasks[dep]; !ok {
				return fmt.Errorf("task %s depends on non-existent task: %s", taskID, dep)
			}
		}
	}

	// Check for cycles using DFS
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var checkCycle func(string) error
	checkCycle = func(taskID string) error {
		visited[taskID] = true
		recStack[taskID] = true

		deps := g.Edges[taskID]
		for _, dep := range deps {
			if !visited[dep] {
				if err := checkCycle(dep); err != nil {
					return err
				}
			} else if recStack[dep] {
				return fmt.Errorf("circular dependency detected: %s -> %s", taskID, dep)
			}
		}

		recStack[taskID] = false
		return nil
	}

	for id := range g.Tasks {
		if !visited[id] {
			if err := checkCycle(id); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetTask retrieves a task by ID.
func (g *Graph) GetTask(id string) (*pipeline.TaskSpec, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	task, ok := g.Tasks[id]
	return task, ok
}

// SetTaskStatus updates a task's status.
func (g *Graph) SetTaskStatus(taskID string, status pipeline.TaskState) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	task, ok := g.Tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if !task.Status.CanTransitionTo(status) {
			return fmt.Errorf("invalid state transition: %v -> %v", task.Status, status)
	}

	task.Status = status
	now := time.Now().UTC()
	if status == pipeline.TaskRunning {
		task.StartedAt = &now
	}
	if status.IsTerminal() {
		task.CompletedAt = &now
	}

	g.UpdatedAt = now
	return nil
}

// UpdateGraphStatus updates the overall graph status based on task states.
func (g *Graph) UpdateGraphStatus() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.Status == GraphPlanning || g.Status == GraphPaused || g.Status == GraphReplanning {
		return // Don't auto-update these
	}

	hasRunning := false
	hasFailed := false
	hasPending := false
	allTerminal := true

	for _, task := range g.Tasks {
		switch task.Status {
		case pipeline.TaskRunning:
			hasRunning = true
			allTerminal = false
		case pipeline.TaskFailed:
			hasFailed = true
		case pipeline.TaskPending, pipeline.TaskWaiting, pipeline.TaskBlocked:
			hasPending = true
			allTerminal = false
		}
	}

	if len(g.Tasks) == 0 {
		g.Status = GraphReady
		return
	}

	if hasRunning {
		g.Status = GraphRunning
	} else if hasFailed {
		g.Status = GraphFailed
	} else if allTerminal {
		g.Status = GraphCompleted
	} else if hasPending {
		g.Status = GraphReady
	}
}

// ShouldReplan checks if replanning should occur based on config and error.
func (g *Graph) ShouldReplan(err error, manual bool) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if manual {
		return true // Always replan on explicit request
	}

	if g.Config.ManualOnly {
		return false // Manual-only mode
	}

	// Check replan count against budget
	replanCount := 0
	for _, m := range g.History {
		if m.Type == pipeline.MutationReplace || m.Type == pipeline.MutationAdd {
			replanCount++
		}
	}

	if replanCount >= g.Config.MaxReplans {
		return false // Budget exhausted
	}

	// Auto-replan on failure if enabled
	if err != nil && g.Config.OnFailure {
		return true
	}

	return false
}

// Stats returns execution statistics.
func (g *Graph) Stats() GraphStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	stats := GraphStats{
		TotalTasks:   len(g.Tasks),
		GraphVersion: g.Version,
		Status:       string(g.Status),
	}

	for _, task := range g.Tasks {
		switch task.Status {
		case pipeline.TaskPassed:
			stats.CompletedTasks++
		case pipeline.TaskFailed:
			stats.FailedTasks++
		case pipeline.TaskRunning:
			stats.RunningTasks++
		case pipeline.TaskPending, pipeline.TaskWaiting, pipeline.TaskBlocked:
			stats.PendingTasks++
		}
	}

	return stats
}

// GraphStats holds summary statistics.
type GraphStats struct {
	TotalTasks     int
	CompletedTasks int
	FailedTasks    int
	RunningTasks   int
	PendingTasks   int
	GraphVersion   int
	Status         string
}

// String returns a human-readable summary.
func (s GraphStats) String() string {
	return fmt.Sprintf("Graph: %d tasks (%d passed, %d failed, %d running, %d pending) v%d [%s]",
		s.TotalTasks, s.CompletedTasks, s.FailedTasks, s.RunningTasks, s.PendingTasks,
		s.GraphVersion, s.Status)
}

// AllTasks returns all tasks in the graph (copy).
func (g *Graph) AllTasks() []*pipeline.TaskSpec {
	g.mu.RLock()
	defer g.mu.RUnlock()

	tasks := make([]*pipeline.TaskSpec, 0, len(g.Tasks))
	for _, task := range g.Tasks {
		tasks = append(tasks, task)
	}

	// Sort for deterministic ordering
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	return tasks
}

// TasksInTier returns tasks for a specific tier index.
func (g *Graph) TasksInTier(tierIdx int) []*pipeline.TaskSpec {
	tiers := g.TopologicalTiers()
	if tierIdx >= len(tiers) {
		return nil
	}

	var tasks []*pipeline.TaskSpec
	for _, id := range tiers[tierIdx] {
		if task, ok := g.GetTask(id); ok {
			tasks = append(tasks, task)
		}
	}

	return tasks
}

// GetDependencies returns all dependencies of a task.
func (g *Graph) GetDependencies(taskID string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if _, ok := g.Tasks[taskID]; !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}

	deps := g.Edges[taskID]
	result := make([]string, len(deps))
	copy(result, deps)
	return result, nil
}

// GetDependents returns all tasks that depend on a given task.
func (g *Graph) GetDependents(taskID string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if _, ok := g.Tasks[taskID]; !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}

	var dependents []string
	for id, deps := range g.Edges {
		for _, dep := range deps {
			if dep == taskID {
				dependents = append(dependents, id)
				break
			}
		}
	}

	return dependents, nil
}

// IsTaskReady checks if a specific task has all dependencies satisfied.
func (g *Graph) IsTaskReady(taskID string) (bool, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	task, ok := g.Tasks[taskID]
	if !ok {
		return false, fmt.Errorf("task %s not found", taskID)
	}

	if task.Status != pipeline.TaskPending {
		return false, nil // Not in pending state
	}

	completed := g.completedTasks()
	deps := g.Edges[taskID]
	for _, dep := range deps {
		if !completed[dep] {
			return false, nil
		}
	}

	return true, nil
}

// Clone creates a deep copy of the graph.
func (g *Graph) Clone() *Graph {
	g.mu.RLock()
	defer g.mu.RUnlock()

	clone := &Graph{
		ID:        g.ID + "-clone",
		SessionID: g.SessionID,
		RootGoal:  g.RootGoal,
		Tasks:     make(map[string]*pipeline.TaskSpec, len(g.Tasks)),
		Edges:     make(map[string][]string, len(g.Edges)),
		Version:   1,
		History:   make([]pipeline.GraphMutation, 0, len(g.History)),
		Status:    GraphPlanning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Config:    g.Config,
	}

	// Copy tasks (shallow copy of TaskSpec is OK as they don't mutate during execution)
	for id, task := range g.Tasks {
		clone.Tasks[id] = task
	}

	// Copy edges
	for id, deps := range g.Edges {
		clone.Edges[id] = make([]string, len(deps))
		copy(clone.Edges[id], deps)
	}

	return clone
}

// String returns a human-readable summary of the graph.
func (g *Graph) String() string {
	stats := g.Stats()
	return fmt.Sprintf("Graph[%s] %s: %s (v%d)",
		g.ID[:8], g.RootGoal, stats.String(), g.Version)
}
