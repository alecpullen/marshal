package planner

import (
	"context"
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
)

// ── ValidateGraph tests ───────────────────────────────────────────────────────

func TestValidateGraph_Empty(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{}}
	if err := ValidateGraph(g); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateGraph_NoCycle(t *testing.T) {
	// A, B→A, C→A, D→{B,C}
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{"A"}},
		{ID: "C", DependsOn: []string{"A"}},
		{ID: "D", DependsOn: []string{"B", "C"}},
	}}
	if err := ValidateGraph(g); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateGraph_LinearChain(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{"A"}},
		{ID: "C", DependsOn: []string{"B"}},
	}}
	if err := ValidateGraph(g); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateGraph_DirectCycle(t *testing.T) {
	// A→B, B→A
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{"B"}},
		{ID: "B", DependsOn: []string{"A"}},
	}}
	err := ValidateGraph(g)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("expected 'cycle detected' in error, got: %v", err)
	}
}

func TestValidateGraph_SelfLoop(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{"A"}},
	}}
	err := ValidateGraph(g)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("expected 'cycle detected' in error, got: %v", err)
	}
}

func TestValidateGraph_ThreeNodeCycle(t *testing.T) {
	// A→B, B→C, C→A
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{"B"}},
		{ID: "B", DependsOn: []string{"C"}},
		{ID: "C", DependsOn: []string{"A"}},
	}}
	err := ValidateGraph(g)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("expected 'cycle detected' in error, got: %v", err)
	}
	// All three nodes should appear in the cycle path
	for _, id := range []string{"A", "B", "C"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("expected %q in cycle error, got: %v", id, err)
		}
	}
}

func TestValidateGraph_UnknownDep(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{"Z"}}, // Z doesn't exist
	}}
	err := ValidateGraph(g)
	if err == nil {
		t.Fatal("expected error for unknown dep, got nil")
	}
	if !strings.Contains(err.Error(), "unknown task") {
		t.Errorf("expected 'unknown task' in error, got: %v", err)
	}
}

// ── TopologicalSort tests ─────────────────────────────────────────────────────

func taskIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return ids
}

func containsAll(slice []string, items ...string) bool {
	set := make(map[string]bool, len(slice))
	for _, s := range slice {
		set[s] = true
	}
	for _, item := range items {
		if !set[item] {
			return false
		}
	}
	return true
}

func TestTopoSort_SingleTask(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
	}}
	tiers, err := TopologicalSort(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tiers) != 1 || len(tiers[0]) != 1 || tiers[0][0].ID != "A" {
		t.Errorf("expected [[A]], got %v", tiers)
	}
}

func TestTopoSort_AllIndependent(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{}},
		{ID: "C", DependsOn: []string{}},
	}}
	tiers, err := TopologicalSort(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tiers) != 1 {
		t.Fatalf("expected 1 tier, got %d: %v", len(tiers), tiers)
	}
	ids := taskIDs(tiers[0])
	if !containsAll(ids, "A", "B", "C") {
		t.Errorf("expected A, B, C in tier 1, got %v", ids)
	}
}

func TestTopoSort_Linear(t *testing.T) {
	// A, B→A, C→B → [[A],[B],[C]]
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{"A"}},
		{ID: "C", DependsOn: []string{"B"}},
	}}
	tiers, err := TopologicalSort(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d: %v", len(tiers), tiers)
	}
	if tiers[0][0].ID != "A" {
		t.Errorf("tier 1 should be [A], got %v", taskIDs(tiers[0]))
	}
	if tiers[1][0].ID != "B" {
		t.Errorf("tier 2 should be [B], got %v", taskIDs(tiers[1]))
	}
	if tiers[2][0].ID != "C" {
		t.Errorf("tier 3 should be [C], got %v", taskIDs(tiers[2]))
	}
}

func TestTopoSort_Diamond(t *testing.T) {
	// A, B→A, C→A, D→{B,C} → [[A],[B,C],[D]]
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{}},
		{ID: "B", DependsOn: []string{"A"}},
		{ID: "C", DependsOn: []string{"A"}},
		{ID: "D", DependsOn: []string{"B", "C"}},
	}}
	tiers, err := TopologicalSort(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d: %v", len(tiers), tiers)
	}
	if tiers[0][0].ID != "A" {
		t.Errorf("tier 1 should be [A], got %v", taskIDs(tiers[0]))
	}
	if !containsAll(taskIDs(tiers[1]), "B", "C") {
		t.Errorf("tier 2 should contain B and C, got %v", taskIDs(tiers[1]))
	}
	if tiers[2][0].ID != "D" {
		t.Errorf("tier 3 should be [D], got %v", taskIDs(tiers[2]))
	}
}

func TestTopoSort_Cycle(t *testing.T) {
	g := &TaskGraph{Feature: "f", Tasks: []Task{
		{ID: "A", DependsOn: []string{"B"}},
		{ID: "B", DependsOn: []string{"A"}},
	}}
	_, err := TopologicalSort(g)
	if err == nil {
		t.Fatal("expected error for cyclic graph, got nil")
	}
}

// ── Planner agent tests ───────────────────────────────────────────────────────

type mockBackend struct {
	content string
	err     error
}

func (m *mockBackend) Complete(_ context.Context, _ string, _ []backend.Message) (backend.Response, error) {
	return backend.Response{Content: m.content, PromptTokens: 10, CompletionTokens: 5}, m.err
}

func (m *mockBackend) Name() string { return "mock" }

func TestPlanner_ValidPlan(t *testing.T) {
	validJSON := `{
		"feature": "add timesheet tracking",
		"tasks": [
			{"id": "A", "description": "Create DB schema", "files_likely_affected": ["store.go"], "depends_on": []},
			{"id": "B", "description": "Add API handlers", "files_likely_affected": ["api.go"], "depends_on": ["A"]}
		]
	}`

	p := New(&mockBackend{content: validJSON}, config.AgentConfig{Model: "test"})
	result, err := p.Plan(context.Background(), "add timesheet tracking")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Graph.Feature != "add timesheet tracking" {
		t.Errorf("expected feature 'add timesheet tracking', got %q", result.Graph.Feature)
	}
	if len(result.Graph.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(result.Graph.Tasks))
	}
	if result.PromptTokens != 10 || result.CompletionTokens != 5 {
		t.Errorf("unexpected token counts: prompt=%d completion=%d", result.PromptTokens, result.CompletionTokens)
	}
}

func TestPlanner_InvalidJSON(t *testing.T) {
	p := New(&mockBackend{content: "not json at all"}, config.AgentConfig{Model: "test"})
	_, err := p.Plan(context.Background(), "some feature")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestPlanner_CycleInResponse(t *testing.T) {
	cyclicJSON := `{
		"feature": "cyclic feature",
		"tasks": [
			{"id": "A", "description": "task A", "files_likely_affected": [], "depends_on": ["B"]},
			{"id": "B", "description": "task B", "files_likely_affected": [], "depends_on": ["A"]}
		]
	}`
	p := New(&mockBackend{content: cyclicJSON}, config.AgentConfig{Model: "test"})
	_, err := p.Plan(context.Background(), "cyclic feature")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected 'cycle' in error, got: %v", err)
	}
}

func TestPlanner_JSONEmbeddedInText(t *testing.T) {
	// Simulate model that wraps JSON in prose (defensive parsing test)
	wrappedJSON := `Here is the plan:
{"feature": "test feature", "tasks": [{"id": "A", "description": "do it", "files_likely_affected": ["main.go"], "depends_on": []}]}
Done.`

	p := New(&mockBackend{content: wrappedJSON}, config.AgentConfig{Model: "test"})
	result, err := p.Plan(context.Background(), "test feature")
	if err != nil {
		t.Fatalf("unexpected error parsing embedded JSON: %v", err)
	}
	if len(result.Graph.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(result.Graph.Tasks))
	}
}
