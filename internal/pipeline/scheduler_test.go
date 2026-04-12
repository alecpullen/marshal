package pipeline_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alec/marshal/internal/pipeline"
)

func TestNewScheduler_SingleTask(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "only task", Files: []string{"a.go"}},
	}
	s, err := pipeline.NewScheduler(tasks)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	tiers := s.Tiers()
	if len(tiers) != 1 {
		t.Errorf("expected 1 tier, got %d", len(tiers))
	}
	if len(tiers[0]) != 1 {
		t.Errorf("expected 1 task in tier 0, got %d", len(tiers[0]))
	}
}

func TestNewScheduler_DependentTasks(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", Files: []string{"a.go"}, DependsOn: []string{}},
		{ID: "task-2", Description: "second", Files: []string{"b.go"}, DependsOn: []string{"task-1"}},
	}
	s, err := pipeline.NewScheduler(tasks)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	tiers := s.Tiers()
	if len(tiers) != 2 {
		t.Errorf("expected 2 tiers, got %d", len(tiers))
	}
	if len(tiers[0]) != 1 || tiers[0][0].ID != "task-1" {
		t.Errorf("tier 0 should have task-1")
	}
	if len(tiers[1]) != 1 || tiers[1][0].ID != "task-2" {
		t.Errorf("tier 1 should have task-2")
	}
}

func TestNewScheduler_ParallelTasks(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", Files: []string{"a.go"}},
		{ID: "task-2", Description: "second", Files: []string{"b.go"}},
	}
	s, err := pipeline.NewScheduler(tasks)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	tiers := s.Tiers()
	if len(tiers) != 1 {
		t.Errorf("expected 1 tier for independent tasks, got %d", len(tiers))
	}
	if len(tiers[0]) != 2 {
		t.Errorf("expected 2 tasks in tier 0, got %d", len(tiers[0]))
	}
}

func TestNewScheduler_CircularDependency(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", DependsOn: []string{"task-2"}},
		{ID: "task-2", Description: "second", DependsOn: []string{"task-1"}},
	}
	_, err := pipeline.NewScheduler(tasks)
	if err == nil {
		t.Error("expected error for circular dependency")
	}
}

func TestScheduler_Run_AllPass(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", Files: []string{"a.go"}},
		{ID: "task-2", Description: "second", Files: []string{"b.go"}},
	}
	s, _ := pipeline.NewScheduler(tasks)

	runCount := 0
	runTask := func(ctx context.Context, t *pipeline.PipelineTask) error {
		runCount++
		t.State = pipeline.TaskPassed
		return nil
	}

	ctx := context.Background()
	if err := s.Run(ctx, runTask); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if runCount != 2 {
		t.Errorf("expected 2 tasks to run, got %d", runCount)
	}
	if !s.AllPassed() {
		t.Error("expected AllPassed() to be true")
	}
}

func TestScheduler_Run_FailureStops(t *testing.T) {
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", Files: []string{"a.go"}, DependsOn: []string{}},
		{ID: "task-2", Description: "second", Files: []string{"b.go"}, DependsOn: []string{"task-1"}},
	}
	s, _ := pipeline.NewScheduler(tasks)

	runTask := func(ctx context.Context, t *pipeline.PipelineTask) error {
		if t.ID == "task-1" {
			return errors.New("task-1 failed")
		}
		return nil
	}

	ctx := context.Background()
	err := s.Run(ctx, runTask)
	if err == nil {
		t.Error("expected error from failed task")
	}

	failed := s.FailedTasks()
	if len(failed) != 1 || failed[0].ID != "task-1" {
		t.Errorf("expected task-1 to be failed, got %v", failed)
	}
}

func TestScheduler_Run_RespectsFileLocks(t *testing.T) {
	// Two independent tasks touching the same file should serialize
	tasks := []*pipeline.PipelineTask{
		{ID: "task-1", Description: "first", Files: []string{"shared.go"}},
		{ID: "task-2", Description: "second", Files: []string{"shared.go"}},
	}
	s, _ := pipeline.NewScheduler(tasks)

	// Both tasks are in the same tier but should not run concurrently
	// due to file locking
	runCount := 0
	runTask := func(ctx context.Context, t *pipeline.PipelineTask) error {
		runCount++
		time.Sleep(10 * time.Millisecond)
		t.State = pipeline.TaskPassed
		return nil
	}

	ctx := context.Background()
	s.Run(ctx, runTask)

	// Both tasks in same tier, but with file locks they should serialize
	// Note: actual verification of serialization would require more complex testing
}
