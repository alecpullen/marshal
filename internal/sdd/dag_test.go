package sdd

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestDAGLoadEmptyWhenNoFile(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	dag, err := LoadDAG(ws)
	if err != nil {
		t.Fatalf("LoadDAG: %v", err)
	}
	if dag == nil {
		t.Fatal("dag nil")
	}
	if len(dag.Tasks) != 0 {
		t.Fatalf("expected empty Tasks, got %d", len(dag.Tasks))
	}
}

func TestDAGSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	dag := &DAG{
		SpecPath: ".marshal/sdd/spec.md",
		Tasks: []DAGTask{
			{ID: "T1", Title: "Foundation", Status: TaskPending, Files: []string{"internal/sdd/types.go"}, Acceptance: []string{"go build ./internal/sdd/"}},
			{ID: "T2", Title: "Git ops", Status: TaskPending, Deps: []string{"T1"}},
		},
	}
	if err := dag.Save(ws); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Verify the file is on disk and has the expected content shape.
	data, err := os.ReadFile(filepath.Join(ws.Dir(), "dag.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("dag.json is empty")
	}

	got, err := LoadDAG(ws)
	if err != nil {
		t.Fatalf("LoadDAG: %v", err)
	}
	if !reflect.DeepEqual(got, dag) {
		t.Fatalf("round trip = %#v, want %#v", got, dag)
	}
}

func TestDAGSetTaskStatus(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	dag := &DAG{
		Tasks: []DAGTask{
			{ID: "T1", Status: TaskPending},
			{ID: "T2", Status: TaskPending, Deps: []string{"T1"}},
		},
	}
	if err := dag.SetTaskStatus("T1", TaskMerged); err != nil {
		t.Fatalf("SetTaskStatus T1: %v", err)
	}
	if dag.Tasks[0].Status != TaskMerged {
		t.Fatalf("T1 status = %q, want %q", dag.Tasks[0].Status, TaskMerged)
	}
	if err := dag.SetTaskStatus("NOPE", TaskInProgress); err == nil {
		t.Fatal("expected error for unknown task id, got nil")
	}
}

func TestDAGReadyTasksRespectsMergedDeps(t *testing.T) {
	dag := &DAG{
		Tasks: []DAGTask{
			{ID: "T1", Status: TaskMerged},
			{ID: "T2", Status: TaskPending, Deps: []string{"T1"}},
			{ID: "T3", Status: TaskPending, Deps: []string{"T1", "T2"}},
			{ID: "T4", Status: TaskPending, Deps: []string{}},
		},
	}
	got := dag.ReadyTasks()
	// Expected: T2 (T1 is merged) and T4 (no deps). T3 is not ready (T2 not yet merged).
	want := []string{"T2", "T4"}
	ids := make([]string, len(got))
	for i, t := range got {
		ids[i] = t.ID
	}
	sort.Strings(ids)
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ReadyTasks ids = %v, want %v", ids, want)
	}
}

func TestDAGReadyTasksSkipsNonPending(t *testing.T) {
	dag := &DAG{
		Tasks: []DAGTask{
			{ID: "T1", Status: TaskMerged},
			{ID: "T2", Status: TaskInProgress, Deps: []string{"T1"}}, // in_progress, not pending
			{ID: "T3", Status: TaskBlocked, Deps: []string{}},
		},
	}
	got := dag.ReadyTasks()
	if len(got) != 0 {
		t.Fatalf("expected 0 ready tasks, got %d: %+v", len(got), got)
	}
}

func TestDAGTaskByID(t *testing.T) {
	dag := &DAG{
		Tasks: []DAGTask{
			{ID: "T1", Title: "Foundation"},
			{ID: "T2", Title: "Git ops"},
		},
	}
	t1, ok := dag.TaskByID("T1")
	if !ok || t1.Title != "Foundation" {
		t.Fatalf("TaskByID T1 = %+v, %v", t1, ok)
	}
	if _, ok := dag.TaskByID("NOPE"); ok {
		t.Fatal("expected false for unknown id")
	}
}
