package sdd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssembleRescueBundle(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	// Seed a DAG, state, progress, and a report.
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Title: "Foundation", Status: TaskMerged}}}
	dag.Save(ws)
	rs := &RepoState{Branch: "sdd/feature", Head: "abc123", Merged: []string{"T1"}}
	rs.Save(ws)
	var p Progress
	p.Append(ws, "T1", "MERGED", "branch", "sdd/T1")
	writeFile(filepath.Join(ws.Dir(), "reports", "T1.md"), "status: DONE\n\nimpl done\n")

	path, err := AssembleRescueBundle(ws)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "T1") {
		t.Error("bundle should mention T1")
	}
	if !strings.Contains(content, "sdd/feature") {
		t.Error("bundle should mention the pipeline branch")
	}
	if !strings.Contains(content, "MERGED") {
		t.Error("bundle should include the progress log")
	}
	if !strings.Contains(content, "impl done") {
		t.Error("bundle should include report contents")
	}
}
