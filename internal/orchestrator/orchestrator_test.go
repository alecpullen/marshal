package orchestrator

import (
	"testing"
	"time"

	"github.com/alecpullen/marshal/internal/pipeline"
	"github.com/google/uuid"
)

// TestNewGraph tests basic graph creation.
func TestNewGraph(t *testing.T) {
	id := uuid.New().String()[:8]
	sessionID := uuid.New().String()[:8]
	goal := "Test implementation"

	g := NewGraph(id, sessionID, goal)

	if g.ID != id {
		t.Errorf("expected ID %s, got %s", id, g.ID)
	}
	if g.SessionID != sessionID {
		t.Errorf("expected SessionID %s, got %s", sessionID, g.SessionID)
	}
	if g.RootGoal != goal {
		t.Errorf("expected RootGoal %s, got %s", goal, g.RootGoal)
	}
	if g.Version != 1 {
		t.Errorf("expected Version 1, got %d", g.Version)
	}
	if g.Status != GraphPlanning {
		t.Errorf("expected Status GraphPlanning, got %s", g.Status)
	}
	if len(g.Tasks) != 0 {
		t.Errorf("expected empty Tasks, got %d", len(g.Tasks))
	}
}

// TestGraphAddTask tests adding tasks to a graph.
func TestGraphAddTask(t *testing.T) {
	g := NewGraph("test", "session", "test goal")

	task := &pipeline.TaskSpec{
		ID:            "task-1",
		Role:          "codegen",
		Goal:          "Implement feature",
		Description:   "Add new API endpoint",
		DependsOn:     []string{},
		Files:         []string{"api.go"},
		MaxIterations: 3,
		Timeout:       5 * time.Minute,
		CreatedAt:     time.Now(),
		Version:       1,
		Status:        pipeline.TaskPending,
	}

	if err := g.AddTask(task); err != nil {
		t.Errorf("failed to add task: %v", err)
	}

	if len(g.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(g.Tasks))
	}

	// Adding duplicate should fail
	if err := g.AddTask(task); err == nil {
		t.Error("expected error for duplicate task, got nil")
	}

	// Adding nil should fail
	if err := g.AddTask(nil); err == nil {
		t.Error("expected error for nil task, got nil")
	}
}

// TestGraphEdges tests edge operations.
func TestGraphEdges(t *testing.T) {
	g := NewGraph("test", "session", "test goal")

	// Add tasks
	tasks := []*pipeline.TaskSpec{
		{ID: "task-1", Role: "codegen", Goal: "Step 1", Status: pipeline.TaskPending},
		{ID: "task-2", Role: "codegen", Goal: "Step 2", Status: pipeline.TaskPending},
		{ID: "task-3", Role: "codegen", Goal: "Step 3", Status: pipeline.TaskPending},
	}

	for _, task := range tasks {
		if err := g.AddTask(task); err != nil {
			t.Fatalf("failed to add task: %v", err)
		}
	}

	// Add edges: task-2 depends on task-1, task-3 depends on task-2
	if err := g.AddEdge("task-1", "task-2"); err != nil {
		t.Errorf("failed to add edge: %v", err)
	}
	if err := g.AddEdge("task-2", "task-3"); err != nil {
		t.Errorf("failed to add edge: %v", err)
	}

	// Check edges
	deps2, _ := g.GetDependencies("task-2")
	if len(deps2) != 1 || deps2[0] != "task-1" {
		t.Errorf("expected task-2 to depend on task-1, got %v", deps2)
	}

	deps3, _ := g.GetDependencies("task-3")
	if len(deps3) != 1 || deps3[0] != "task-2" {
		t.Errorf("expected task-3 to depend on task-2, got %v", deps3)
	}

	// Self-dependency should fail
	if err := g.AddEdge("task-1", "task-1"); err == nil {
		t.Error("expected error for self-dependency")
	}

	// Non-existent task should fail
	if err := g.AddEdge("task-1", "nonexistent"); err == nil {
		t.Error("expected error for non-existent task")
	}
}

// TestGraphTopologicalTiers tests tier calculation.
func TestGraphTopologicalTiers(t *testing.T) {
	g := NewGraph("test", "session", "test goal")

	// Create a diamond dependency graph:
	//     task-1
	//    /      \
	// task-2  task-3
	//    \      /
	//     task-4

	tasks := []*pipeline.TaskSpec{
		{ID: "task-1", Role: "codegen", Goal: "Root", Status: pipeline.TaskPending},
		{ID: "task-2", Role: "codegen", Goal: "Left", Status: pipeline.TaskPending},
		{ID: "task-3", Role: "codegen", Goal: "Right", Status: pipeline.TaskPending},
		{ID: "task-4", Role: "codegen", Goal: "Merge", Status: pipeline.TaskPending},
	}

	for _, task := range tasks {
		g.AddTask(task)
	}

	g.AddEdge("task-1", "task-2")
	g.AddEdge("task-1", "task-3")
	g.AddEdge("task-2", "task-4")
	g.AddEdge("task-3", "task-4")

	tiers := g.TopologicalTiers()

	if len(tiers) != 3 {
		t.Errorf("expected 3 tiers, got %d", len(tiers))
	}

	// Tier 0: task-1
	if len(tiers[0]) != 1 || tiers[0][0] != "task-1" {
		t.Errorf("expected tier 0 to be [task-1], got %v", tiers[0])
	}

	// Tier 1: task-2, task-3 (order doesn't matter)
	if len(tiers[1]) != 2 {
		t.Errorf("expected tier 1 to have 2 tasks, got %d", len(tiers[1]))
	}

	// Tier 2: task-4
	if len(tiers[2]) != 1 || tiers[2][0] != "task-4" {
		t.Errorf("expected tier 2 to be [task-4], got %v", tiers[2])
	}
}

// TestGraphValidate tests validation.
func TestGraphValidate(t *testing.T) {
	// Valid graph
	g := NewGraph("test", "session", "test goal")
	g.AddTask(&pipeline.TaskSpec{ID: "task-1", Role: "codegen", Goal: "Step 1", Status: pipeline.TaskPending})
	g.AddTask(&pipeline.TaskSpec{ID: "task-2", Role: "codegen", Goal: "Step 2", Status: pipeline.TaskPending})
	g.AddEdge("task-1", "task-2")

	if err := g.Validate(); err != nil {
		t.Errorf("valid graph failed validation: %v", err)
	}

	// Graph with cycle
	g2 := NewGraph("test2", "session", "test goal")
	g2.AddTask(&pipeline.TaskSpec{ID: "task-a", Role: "codegen", Goal: "A", Status: pipeline.TaskPending})
	g2.AddTask(&pipeline.TaskSpec{ID: "task-b", Role: "codegen", Goal: "B", Status: pipeline.TaskPending})
	g2.AddTask(&pipeline.TaskSpec{ID: "task-c", Role: "codegen", Goal: "C", Status: pipeline.TaskPending})
	g2.AddEdge("task-a", "task-b")
	g2.AddEdge("task-b", "task-c")
	g2.AddEdge("task-c", "task-a") // Creates cycle

	if err := g2.Validate(); err == nil {
		t.Error("expected validation error for cyclic graph")
	}
}

// TestGraphReady tests the Ready() function.
func TestGraphReady(t *testing.T) {
	g := NewGraph("test", "session", "test goal")

	// Chain: task-1 -> task-2 -> task-3
	g.AddTask(&pipeline.TaskSpec{
		ID: "task-1", Role: "codegen", Goal: "Step 1", Status: pipeline.TaskPending,
		MaxIterations: 3, Timeout: 5 * time.Minute,
	})
	g.AddTask(&pipeline.TaskSpec{
		ID: "task-2", Role: "codegen", Goal: "Step 2", Status: pipeline.TaskPending,
		MaxIterations: 3, Timeout: 5 * time.Minute,
	})
	g.AddTask(&pipeline.TaskSpec{
		ID: "task-3", Role: "codegen", Goal: "Step 3", Status: pipeline.TaskPending,
		MaxIterations: 3, Timeout: 5 * time.Minute,
	})
	g.AddEdge("task-1", "task-2")
	g.AddEdge("task-2", "task-3")

	// Initially, only task-1 should be ready
	ready := g.Ready()
	if len(ready) != 1 {
		t.Errorf("expected 1 ready task initially, got %d", len(ready))
	} else if ready[0].ID != "task-1" {
		t.Errorf("expected task-1 ready initially, got %s", ready[0].ID)
	}

	// Mark task-1 as running first (required by state machine), then passed
	if err := g.SetTaskStatus("task-1", pipeline.TaskRunning); err != nil {
		t.Fatalf("failed to set task-1 to running: %v", err)
	}
	if err := g.SetTaskStatus("task-1", pipeline.TaskPassed); err != nil {
		t.Fatalf("failed to set task-1 to passed: %v", err)
	}

	// Now task-2 should be ready
	ready = g.Ready()
	if len(ready) != 1 {
		t.Errorf("expected 1 ready task after task-1 passed, got %d", len(ready))
	} else if ready[0].ID != "task-2" {
		t.Errorf("expected task-2 ready after task-1 passed, got %s", ready[0].ID)
	}

	// Mark task-2 as running then passed
	if err := g.SetTaskStatus("task-2", pipeline.TaskRunning); err != nil {
		t.Fatalf("failed to set task-2 to running: %v", err)
	}
	if err := g.SetTaskStatus("task-2", pipeline.TaskPassed); err != nil {
		t.Fatalf("failed to set task-2 to passed: %v", err)
	}

	// Now task-3 should be ready
	ready = g.Ready()
	if len(ready) != 1 {
		t.Errorf("expected 1 ready task after task-2 passed, got %d", len(ready))
	} else if ready[0].ID != "task-3" {
		t.Errorf("expected task-3 ready after task-2 passed, got %s", ready[0].ID)
	}
}

// TestGraphStats tests statistics calculation.
func TestGraphStats(t *testing.T) {
	g := NewGraph("test", "session", "test goal")

	g.AddTask(&pipeline.TaskSpec{ID: "t1", Role: "codegen", Goal: "Task 1", Status: pipeline.TaskPending})
	g.AddTask(&pipeline.TaskSpec{ID: "t2", Role: "codegen", Goal: "Task 2", Status: pipeline.TaskRunning})
	g.AddTask(&pipeline.TaskSpec{ID: "t3", Role: "codegen", Goal: "Task 3", Status: pipeline.TaskPassed})
	g.AddTask(&pipeline.TaskSpec{ID: "t4", Role: "codegen", Goal: "Task 4", Status: pipeline.TaskFailed})

	stats := g.Stats()

	if stats.TotalTasks != 4 {
		t.Errorf("expected TotalTasks 4, got %d", stats.TotalTasks)
	}
	if stats.PendingTasks != 1 {
		t.Errorf("expected PendingTasks 1, got %d", stats.PendingTasks)
	}
	if stats.RunningTasks != 1 {
		t.Errorf("expected RunningTasks 1, got %d", stats.RunningTasks)
	}
	if stats.CompletedTasks != 1 {
		t.Errorf("expected CompletedTasks 1, got %d", stats.CompletedTasks)
	}
	if stats.FailedTasks != 1 {
		t.Errorf("expected FailedTasks 1, got %d", stats.FailedTasks)
	}
}

// TestGraphMutation tests mutation application.
func TestGraphMutation(t *testing.T) {
	g := NewGraph("test", "session", "test goal")
	g.AddTask(&pipeline.TaskSpec{ID: "task-1", Role: "codegen", Goal: "Original", Status: pipeline.TaskPending})

	// Apply add mutation
	m := pipeline.NewGraphMutation(pipeline.MutationAdd)
	m.NewSpec = &pipeline.TaskSpec{
		ID:            "task-2",
		Role:          "codegen",
		Goal:          "New task",
		DependsOn:     []string{"task-1"},
		Status:        pipeline.TaskPending,
		CreatedAt:     time.Now(),
		Version:       1,
	}
	m.Reason = "Adding new task"
	m.Trigger = "test"

	if err := g.ApplyMutation(*m); err != nil {
		t.Errorf("failed to apply add mutation: %v", err)
	}

	if len(g.Tasks) != 2 {
		t.Errorf("expected 2 tasks after add, got %d", len(g.Tasks))
	}

	if g.Version != 2 {
		t.Errorf("expected version 2 after mutation, got %d", g.Version)
	}

	if len(g.History) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(g.History))
	}
}

// TestMermaidGeneration tests Mermaid diagram generation.
func TestMermaidGeneration(t *testing.T) {
	g := NewGraph("test", "session", "Test Graph")
	g.AddTask(&pipeline.TaskSpec{ID: "task-a", Role: "codegen", Goal: "Step A", Description: "First step", Status: pipeline.TaskPending})
	g.AddTask(&pipeline.TaskSpec{ID: "task-b", Role: "codegen", Goal: "Step B", Description: "Second step", Status: pipeline.TaskRunning})
	g.AddEdge("task-a", "task-b")

	opts := DefaultMermaidOptions()
	gen := NewMermaidGenerator(g, opts)

	diagram := gen.Generate()

	if diagram == "" {
		t.Error("expected non-empty diagram")
	}

	if !contains(diagram, "flowchart") {
		t.Error("expected diagram to contain 'flowchart'")
	}

	if !contains(diagram, "task_a") {
		t.Error("expected diagram to contain task_a")
	}

	if !contains(diagram, "task_b") {
		t.Error("expected diagram to contain task_b")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
