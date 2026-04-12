package planner_test

import (
	"context"
	"testing"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/planner"
)

// mockBackend for testing planner generation
type mockPlannerBackend struct {
	response string
}

func (m *mockPlannerBackend) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	return backend.Response{Content: m.response}, nil
}
func (m *mockPlannerBackend) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) {
	return nil, nil
}
func (m *mockPlannerBackend) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockPlannerBackend) SupportsTools() bool { return false }
func (m *mockPlannerBackend) SupportsJSONMode() bool { return true }
func (m *mockPlannerBackend) Model() string { return "test-planner" }

func TestGenerate_ValidJSON(t *testing.T) {
	json := `{
  "prompt": "add user authentication",
  "tasks": [
    {"id": "task-1", "description": "Create auth middleware", "files_likely_affected": ["auth/middleware.go"], "depends_on": []},
    {"id": "task-2", "description": "Add login endpoint", "files_likely_affected": ["api/login.go"], "depends_on": ["task-1"]}
  ]
}`
	mock := &mockPlannerBackend{response: json}
	
	graph, err := planner.Generate(context.Background(), mock, "add user authentication")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	
	if len(graph.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(graph.Tasks))
	}
	if graph.Tasks[0].ID != "task-1" {
		t.Errorf("expected task-1 first, got %s", graph.Tasks[0].ID)
	}
	if len(graph.Tasks[1].DependsOn) != 1 || graph.Tasks[1].DependsOn[0] != "task-1" {
		t.Errorf("expected task-2 to depend on task-1, got %v", graph.Tasks[1].DependsOn)
	}
}

func TestGenerate_WithMarkdownFences(t *testing.T) {
	json := "```json\n" + `{
  "prompt": "fix bug",
  "tasks": [{"id": "task-1", "description": "Fix it", "files_likely_affected": ["fix.go"], "depends_on": []}]
}` + "\n```"
	mock := &mockPlannerBackend{response: json}
	
	graph, err := planner.Generate(context.Background(), mock, "fix bug")
	if err != nil {
		t.Fatalf("Generate failed with markdown fences: %v", err)
	}
	
	if len(graph.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(graph.Tasks))
	}
}

func TestGenerate_WithThinkingBlocks(t *testing.T) {
	json := `<thinking>
I need to break this down into tasks.
</thinking>
{
  "prompt": "refactor code",
  "tasks": [{"id": "task-1", "description": "Refactor", "files_likely_affected": ["ref.go"], "depends_on": []}]
}`
	mock := &mockPlannerBackend{response: json}
	
	graph, err := planner.Generate(context.Background(), mock, "refactor code")
	if err != nil {
		t.Fatalf("Generate failed with thinking blocks: %v", err)
	}
	
	if len(graph.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(graph.Tasks))
	}
}

func TestGenerate_InvalidJSON(t *testing.T) {
	mock := &mockPlannerBackend{response: "not valid json"}
	
	_, err := planner.Generate(context.Background(), mock, "do something")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestGenerate_InvalidGraph(t *testing.T) {
	// Valid JSON but invalid graph (circular dependency)
	json := `{
  "prompt": "bad tasks",
  "tasks": [
    {"id": "task-1", "description": "First", "files_likely_affected": ["a.go"], "depends_on": ["task-2"]},
    {"id": "task-2", "description": "Second", "files_likely_affected": ["b.go"], "depends_on": ["task-1"]}
  ]
}`
	mock := &mockPlannerBackend{response: json}
	
	_, err := planner.Generate(context.Background(), mock, "bad tasks")
	if err == nil {
		t.Error("expected error for invalid graph (circular dependency)")
	}
}
