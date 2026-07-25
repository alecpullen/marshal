package sdd

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteAndReadStateDump(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	dag := &DAG{
		Tasks: []DAGTask{
			{ID: "T1", Title: "Foundation", Status: TaskMerged, Deps: []string{}},
			{ID: "T2", Title: "Git ops", Status: TaskPending, Deps: []string{"T1"}},
		},
	}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "abc123", Merged: []string{"T1"}}

	path, err := WriteStateDump(ws, dag, rs, 3)
	if err != nil {
		t.Fatalf("WriteStateDump: %v", err)
	}
	if path != filepath.Join(ws.Dir(), "state-dump.json") {
		t.Errorf("path = %q", path)
	}

	got, err := ReadStateDump(path)
	if err != nil {
		t.Fatalf("ReadStateDump: %v", err)
	}
	if got.PipelineBranch != "sdd/feature" || got.PipelineHead != "abc123" || got.TargetBranch != "main" {
		t.Errorf("fields = %+v", got)
	}
	if got.LastBatchID != 3 {
		t.Errorf("LastBatchID = %d", got.LastBatchID)
	}
	if len(got.Merged) != 1 || got.Merged[0] != "T1" {
		t.Errorf("Merged = %v", got.Merged)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("Tasks len = %d", len(got.Tasks))
	}
	// T2 should be Ready (T1 is merged, T2 is pending with dep T1).
	if !got.Tasks[1].Ready {
		t.Errorf("T2 should be Ready, got %+v", got.Tasks[1])
	}
	if got.Tasks[1].Status != "pending" {
		t.Errorf("T2 Status = %q", got.Tasks[1].Status)
	}
}

func TestStateDumpJSONRoundTrip(t *testing.T) {
	sd := StateDump{
		PipelineBranch: "sdd/feature",
		PipelineHead:   "abc123",
		TargetBranch:   "main",
		Tasks:          []TaskDump{{ID: "T1", Title: "x", Status: "merged", Deps: []string{}, Ready: false}},
		Merged:         []string{"T1"},
		LastBatchID:    2,
	}
	data, err := json.Marshal(sd)
	if err != nil {
		t.Fatal(err)
	}
	var got StateDump
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sd) {
		t.Fatalf("round trip = %#v, want %#v", got, sd)
	}
}
