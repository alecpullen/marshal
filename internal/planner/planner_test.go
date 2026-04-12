package planner_test

import (
	"testing"

	"github.com/alec/marshal/internal/planner"
)

func TestValidate_ValidGraph(t *testing.T) {
	g := &planner.TaskGraph{
		Prompt: "test",
		Tasks: []planner.Task{
			{ID: "task-1", Description: "first", FilesLikelyAffected: []string{"a.go"}, DependsOn: []string{}},
			{ID: "task-2", Description: "second", FilesLikelyAffected: []string{"b.go"}, DependsOn: []string{"task-1"}},
		},
	}
	if err := planner.Validate(g); err != nil {
		t.Errorf("expected valid graph, got: %v", err)
	}
}

func TestValidate_MissingID(t *testing.T) {
	g := &planner.TaskGraph{
		Tasks: []planner.Task{
			{ID: "", Description: "no id"},
		},
	}
	if err := planner.Validate(g); err == nil {
		t.Error("expected error for missing task id")
	}
}

func TestValidate_DuplicateID(t *testing.T) {
	g := &planner.TaskGraph{
		Tasks: []planner.Task{
			{ID: "task-1", Description: "first"},
			{ID: "task-1", Description: "duplicate"},
		},
	}
	if err := planner.Validate(g); err == nil {
		t.Error("expected error for duplicate task id")
	}
}

func TestValidate_UnknownDependency(t *testing.T) {
	g := &planner.TaskGraph{
		Tasks: []planner.Task{
			{ID: "task-1", Description: "first", DependsOn: []string{"nonexistent"}},
		},
	}
	if err := planner.Validate(g); err == nil {
		t.Error("expected error for unknown dependency")
	}
}

func TestValidate_CircularDependency(t *testing.T) {
	g := &planner.TaskGraph{
		Tasks: []planner.Task{
			{ID: "task-1", Description: "first", DependsOn: []string{"task-2"}},
			{ID: "task-2", Description: "second", DependsOn: []string{"task-1"}},
		},
	}
	if err := planner.Validate(g); err == nil {
		t.Error("expected error for circular dependency")
	}
}

func TestValidate_SelfDependency(t *testing.T) {
	g := &planner.TaskGraph{
		Tasks: []planner.Task{
			{ID: "task-1", Description: "self-ref", DependsOn: []string{"task-1"}},
		},
	}
	if err := planner.Validate(g); err == nil {
		t.Error("expected error for self-dependency")
	}
}
