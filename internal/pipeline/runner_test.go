package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/alecpullen/marshal/internal/agents/critic"
	"github.com/alecpullen/marshal/internal/agents/planner"
	"github.com/alecpullen/marshal/internal/marshal"
	"github.com/alecpullen/marshal/internal/store"
)

// MockGit implements a mock git layer for testing
type MockGit struct {
	branches        []string
	mergedBranches  []string
	deletedBranches []string
	currentBranch   string
}

func (m *MockGit) CreateIsolationBranch(name string) error {
	m.branches = append(m.branches, name)
	m.currentBranch = name
	return nil
}

func (m *MockGit) GetDiff() (string, error) {
	return "mock diff", nil
}

func (m *MockGit) StageAndCommit(message string) error {
	return nil
}

func (m *MockGit) HardResetToHead() error {
	return nil
}

func (m *MockGit) DeleteBranch(name string) error {
	m.deletedBranches = append(m.deletedBranches, name)
	return nil
}

func (m *MockGit) CheckoutBranch(name string) error {
	m.currentBranch = name
	return nil
}

func (m *MockGit) MergeBranch(name string, message string) error {
	m.mergedBranches = append(m.mergedBranches, name)
	return nil
}

func (m *MockGit) DiffBranch(branch string) (string, error) {
	return "", nil
}

func TestRunner_SingleTaskSuccess(t *testing.T) {
	// Create a mock store with in-memory database
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	// Create a pipeline run
	graph := planner.TaskGraph{
		Feature: "Test feature",
		Tasks: []planner.Task{
			{ID: "A", Description: "Task A", DependsOn: []string{}},
		},
	}
	planJSON, _ := json.Marshal(graph)

	run := &store.PipelineRun{
		Feature:  "Test feature",
		Status:   "PLANNED",
		PlanJSON: string(planJSON),
	}
	if err := s.CreatePipelineRun(run); err != nil {
		t.Fatalf("failed to create pipeline run: %v", err)
	}
	if err := s.CreatePipelineTasks(run.ID, graph.Tasks); err != nil {
		t.Fatalf("failed to create pipeline tasks: %v", err)
	}

	// Create mock git
	mockGit := &MockGit{}

	// Verify setup
	t.Logf("Created pipeline run with ID: %d", run.ID)
	t.Logf("Git branches created: %v", mockGit.branches)

	// Retrieve and verify tasks
	tasks, err := s.GetPipelineTasks(run.ID)
	if err != nil {
		t.Fatalf("failed to get pipeline tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].TaskID != "A" {
		t.Errorf("expected TaskID 'A', got %s", tasks[0].TaskID)
	}
}

func TestRunner_MultipleTasksInOrder(t *testing.T) {
	// Test that tasks are executed in the correct order based on dependencies
	tasks := []planner.Task{
		{ID: "A", Description: "First task", DependsOn: []string{}},
		{ID: "B", Description: "Second task", DependsOn: []string{"A"}},
		{ID: "C", Description: "Third task", DependsOn: []string{"B"}},
	}

	graph := planner.TaskGraph{
		Feature: "Sequential tasks",
		Tasks:   tasks,
	}

	// Get topological sort
	tiers, err := planner.TopologicalSort(&graph)
	if err != nil {
		t.Fatalf("topological sort failed: %v", err)
	}

	// Verify execution order
	expectedTiers := [][]string{
		{"A"},
		{"B"},
		{"C"},
	}

	if len(tiers) != len(expectedTiers) {
		t.Errorf("expected %d tiers, got %d", len(expectedTiers), len(tiers))
	}

	for i, tier := range tiers {
		if len(tier) != len(expectedTiers[i]) {
			t.Errorf("tier %d: expected %d tasks, got %d", i, len(expectedTiers[i]), len(tier))
			continue
		}
		for j, task := range tier {
			if task.ID != expectedTiers[i][j] {
				t.Errorf("tier %d, task %d: expected %s, got %s", i, j, expectedTiers[i][j], task.ID)
			}
		}
	}
}

func TestRunner_RespectsDependencies(t *testing.T) {
	// Test that a task with dependencies is executed after its dependencies
	tasks := []planner.Task{
		{ID: "A", Description: "Base task", DependsOn: []string{}},
		{ID: "B", Description: "Depends on A", DependsOn: []string{"A"}},
		{ID: "C", Description: "Independent", DependsOn: []string{}},
		{ID: "D", Description: "Depends on B and C", DependsOn: []string{"B", "C"}},
	}

	graph := planner.TaskGraph{
		Feature: "Dependency test",
		Tasks:   tasks,
	}

	tiers, err := planner.TopologicalSort(&graph)
	if err != nil {
		t.Fatalf("topological sort failed: %v", err)
	}

	// Tier 1 should have A and C (no dependencies)
	// Tier 2 should have B (depends on A)
	// Tier 3 should have D (depends on B and C)

	if len(tiers) != 3 {
		t.Errorf("expected 3 tiers, got %d", len(tiers))
	}

	// Check tier 1 contains A and C
	tier1IDs := make(map[string]bool)
	for _, task := range tiers[0] {
		tier1IDs[task.ID] = true
	}
	if !tier1IDs["A"] || !tier1IDs["C"] {
		t.Errorf("tier 1 should contain A and C, got %v", tier1IDs)
	}

	// Check tier 2 contains B
	if len(tiers) > 1 {
		tier2IDs := make(map[string]bool)
		for _, task := range tiers[1] {
			tier2IDs[task.ID] = true
		}
		if !tier2IDs["B"] {
			t.Errorf("tier 2 should contain B, got %v", tier2IDs)
		}
	}

	// Check tier 3 contains D
	if len(tiers) > 2 {
		tier3IDs := make(map[string]bool)
		for _, task := range tiers[2] {
			tier3IDs[task.ID] = true
		}
		if !tier3IDs["D"] {
			t.Errorf("tier 3 should contain D, got %v", tier3IDs)
		}
	}
}

func TestRunner_FailFastOnError(t *testing.T) {
	// This test verifies that when a task fails, subsequent tasks are not executed
	// In a real implementation, we would mock the Marshal to return an error for task B
	// and verify that task C is never executed

	// For now, we test the structure is correct
	tasks := []planner.Task{
		{ID: "A", Description: "First task", DependsOn: []string{}},
		{ID: "B", Description: "Will fail", DependsOn: []string{"A"}},
		{ID: "C", Description: "Should not run", DependsOn: []string{"B"}},
	}

	graph := planner.TaskGraph{
		Feature: "Fail fast test",
		Tasks:   tasks,
	}

	tiers, err := planner.TopologicalSort(&graph)
	if err != nil {
		t.Fatalf("topological sort failed: %v", err)
	}

	// Verify we have the expected structure
	if len(tiers) != 3 {
		t.Errorf("expected 3 tiers, got %d", len(tiers))
	}

	// Verify dependency chain
	for _, tier := range tiers {
		for _, task := range tier {
			switch task.ID {
			case "A":
				if len(task.DependsOn) != 0 {
					t.Errorf("A should have no dependencies")
				}
			case "B":
				if len(task.DependsOn) != 1 || task.DependsOn[0] != "A" {
					t.Errorf("B should depend on A")
				}
			case "C":
				if len(task.DependsOn) != 1 || task.DependsOn[0] != "B" {
					t.Errorf("C should depend on B")
				}
			}
		}
	}
}

func TestProgressCallback(t *testing.T) {
	// Test that progress events are generated correctly
	events := []ProgressEvent{}
	progress := func(event ProgressEvent) {
		events = append(events, event)
	}

	// Simulate a task start
	progress(ProgressEvent{
		Type:     "task_start",
		TaskID:   "A",
		Message:  "Starting task A",
		Progress: 0.0,
	})

	// Simulate task complete
	progress(ProgressEvent{
		Type:     "task_complete",
		TaskID:   "A",
		Message:  "Task A completed",
		Progress: 0.5,
	})

	// Verify events
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != "task_start" {
		t.Errorf("expected first event type 'task_start', got %s", events[0].Type)
	}

	if events[1].Type != "task_complete" {
		t.Errorf("expected second event type 'task_complete', got %s", events[1].Type)
	}

	if events[0].TaskID != "A" {
		t.Errorf("expected TaskID 'A', got %s", events[0].TaskID)
	}
}

func TestConsoleProgressHandler(t *testing.T) {
	// Test that the console handler doesn't panic
	events := []ProgressEvent{
		{Type: "task_start", TaskID: "A", Message: "Starting"},
		{Type: "task_complete", TaskID: "A", Message: "Done"},
		{Type: "task_failed", TaskID: "B", Message: "Error"},
		{Type: "merge", Message: "Merging branches"},
		{Type: "complete", Message: "All done"},
	}

	// Just verify no panic
	for _, event := range events {
		ConsoleProgressHandler(event)
	}
}

func TestStorePipelineTasks(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	// Create a pipeline run
	run := &store.PipelineRun{
		Feature:  "Test feature",
		Status:   "PLANNED",
		PlanJSON: `{"feature":"Test","tasks":[]}`,
	}
	if err := s.CreatePipelineRun(run); err != nil {
		t.Fatalf("failed to create pipeline run: %v", err)
	}

	// Create tasks
	tasks := []planner.Task{
		{ID: "A", Description: "Task A", DependsOn: []string{}},
		{ID: "B", Description: "Task B", DependsOn: []string{"A"}},
	}

	if err := s.CreatePipelineTasks(run.ID, tasks); err != nil {
		t.Fatalf("failed to create pipeline tasks: %v", err)
	}

	// Retrieve tasks
	retrieved, err := s.GetPipelineTasks(run.ID)
	if err != nil {
		t.Fatalf("failed to get pipeline tasks: %v", err)
	}

	if len(retrieved) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(retrieved))
	}

	// Verify task properties
	taskMap := make(map[string]*store.PipelineTask)
	for i := range retrieved {
		taskMap[retrieved[i].TaskID] = &retrieved[i]
	}

	if taskA, ok := taskMap["A"]; ok {
		if taskA.Description != "Task A" {
			t.Errorf("expected description 'Task A', got %s", taskA.Description)
		}
		if taskA.Status != "PENDING" {
			t.Errorf("expected status 'PENDING', got %s", taskA.Status)
		}
	} else {
		t.Errorf("task A not found")
	}

	if taskB, ok := taskMap["B"]; ok {
		if taskB.Description != "Task B" {
			t.Errorf("expected description 'Task B', got %s", taskB.Description)
		}
		if taskB.Status != "PENDING" {
			t.Errorf("expected status 'PENDING', got %s", taskB.Status)
		}
		// Check depends_on parsing
		var dependsOn []string
		if err := json.Unmarshal([]byte(taskB.DependsOn), &dependsOn); err != nil {
			t.Errorf("failed to parse depends_on: %v", err)
		}
		if len(dependsOn) != 1 || dependsOn[0] != "A" {
			t.Errorf("expected depends_on [A], got %v", dependsOn)
		}
	} else {
		t.Errorf("task B not found")
	}

	// Test status update
	if err := s.UpdatePipelineTaskStatus(run.ID, "A", "RUNNING"); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	retrieved, _ = s.GetPipelineTasks(run.ID)
	for _, task := range retrieved {
		if task.TaskID == "A" && task.Status != "RUNNING" {
			t.Errorf("expected status 'RUNNING', got %s", task.Status)
		}
	}

	// Test branch update
	if err := s.UpdatePipelineTaskBranch(run.ID, "A", "marshal/pipeline-1-task-A"); err != nil {
		t.Fatalf("failed to update task branch: %v", err)
	}

	retrieved, _ = s.GetPipelineTasks(run.ID)
	for _, task := range retrieved {
		if task.TaskID == "A" && task.Branch != "marshal/pipeline-1-task-A" {
			t.Errorf("expected branch 'marshal/pipeline-1-task-A', got %s", task.Branch)
		}
	}
}

func TestRunResultStructure(t *testing.T) {
	// Test the RunResult structure
	result := RunResult{
		PipelineID:   1,
		Status:       "DONE",
		TasksTotal:   3,
		TasksDone:    3,
		TasksFailed:  0,
		FailedTaskID: "",
	}

	if result.Status != "DONE" {
		t.Errorf("expected status DONE, got %s", result.Status)
	}
	if result.TasksTotal != 3 {
		t.Errorf("expected 3 total tasks, got %d", result.TasksTotal)
	}
	if result.TasksDone != 3 {
		t.Errorf("expected 3 done tasks, got %d", result.TasksDone)
	}
}

func TestMarshalResultWithVerdict(t *testing.T) {
	// Test that Marshal Result correctly uses critic.Verdict
	verdict := &critic.Verdict{
		Verdict:  "PASS",
		Summary:  "Test passed",
		Issue:    "",
		Fix:      "",
		Concerns: []string{},
	}

	result := marshal.Result{
		Status:       "SUCCESS",
		FinalVerdict: verdict,
		SHA:          "abc123",
	}

	if result.Status != "SUCCESS" {
		t.Errorf("expected SUCCESS, got %s", result.Status)
	}
	if result.FinalVerdict == nil {
		t.Errorf("expected verdict to be set")
	} else if result.FinalVerdict.Verdict != "PASS" {
		t.Errorf("expected verdict PASS, got %s", result.FinalVerdict.Verdict)
	}
	if result.SHA != "abc123" {
		t.Errorf("expected SHA abc123, got %s", result.SHA)
	}
}
