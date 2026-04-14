package pipeline_test

import (
	"context"
	"testing"

	"github.com/alecpullen/marshal/internal/pipeline"
)

// Test complex DAG: A → B → D, A → C → D (diamond pattern)
func TestNewScheduler_DiamondDependency(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "A", Description: "base", Files: []string{"a.go"}, DependsOn: []string{}},
		{ID: "B", Description: "left", Files: []string{"b.go"}, DependsOn: []string{"A"}},
		{ID: "C", Description: "right", Files: []string{"c.go"}, DependsOn: []string{"A"}},
		{ID: "D", Description: "merge", Files: []string{"d.go"}, DependsOn: []string{"B", "C"}},
	}
	s, err := pipeline.NewScheduler(tasks)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	tiers := s.Tiers()
	if len(tiers) != 3 {
		t.Errorf("expected 3 tiers for diamond DAG, got %d", len(tiers))
	}
	// Tier 0: A
	if len(tiers[0]) != 1 || tiers[0][0].ID != "A" {
		t.Errorf("tier 0 should have only A, got %v", tiers[0])
	}
	// Tier 1: B and C (in parallel)
	if len(tiers[1]) != 2 {
		t.Errorf("tier 1 should have B and C, got %v", tiers[1])
	}
	// Tier 2: D
	if len(tiers[2]) != 1 || tiers[2][0].ID != "D" {
		t.Errorf("tier 2 should have only D, got %v", tiers[2])
	}
}

// Test that scheduler properly validates unknown dependencies
func TestNewScheduler_UnknownDependency(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", DependsOn: []string{"nonexistent"}},
	}
	_, err := pipeline.NewScheduler(tasks)
	if err == nil {
		t.Error("expected error for unknown dependency")
	}
}

// Test chain of dependencies
func TestNewScheduler_LongChain(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "1", DependsOn: []string{}},
		{ID: "task-2", Description: "2", DependsOn: []string{"task-1"}},
		{ID: "task-3", Description: "3", DependsOn: []string{"task-2"}},
		{ID: "task-4", Description: "4", DependsOn: []string{"task-3"}},
		{ID: "task-5", Description: "5", DependsOn: []string{"task-4"}},
	}
	s, err := pipeline.NewScheduler(tasks)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	tiers := s.Tiers()
	if len(tiers) != 5 {
		t.Errorf("expected 5 tiers for chain of 5, got %d", len(tiers))
	}
	for i, tier := range tiers {
		if len(tier) != 1 {
			t.Errorf("tier %d should have 1 task, got %d", i, len(tier))
		}
	}
}

// Test AllPassed with mixed results
func TestScheduler_AllPassed_Mixed(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "will pass", Files: []string{"a.go"}},
		{ID: "task-2", Description: "will fail", Files: []string{"b.go"}},
	}
	s, _ := pipeline.NewScheduler(tasks)

	// Simulate: task-1 passed, task-2 failed
	tasks[0].State = pipeline.TaskPassed
	tasks[1].State = pipeline.TaskFailed

	if s.AllPassed() {
		t.Error("expected AllPassed() to be false when one task failed")
	}
}

// Test FailedTasks returns empty slice when all passed
func TestScheduler_FailedTasks_AllPassed(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "passes", Files: []string{"a.go"}},
	}
	s, _ := pipeline.NewScheduler(tasks)
	tasks[0].State = pipeline.TaskPassed

	failed := s.FailedTasks()
	if len(failed) != 0 {
		t.Errorf("expected 0 failed tasks, got %d", len(failed))
	}
}

// Test context cancellation during Run
func TestScheduler_Run_ContextCancellation(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "slow task", Files: []string{"a.go"}},
	}
	s, _ := pipeline.NewScheduler(tasks)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	runTask := func(ctx context.Context, t *pipeline.PipelineTask) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	err := s.Run(ctx, runTask)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}
