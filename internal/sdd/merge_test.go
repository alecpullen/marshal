package sdd

import "testing"

func TestMergeSuccess(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main123")
	git.SetAncestor("main123", "pipe123") // base OK
	git.SetBranch("sdd/T1")
	var p Progress
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1"}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "pipe123"}

	r := Merge(git, ws, &p, dag, rs, "T1")
	if !r.Merged || r.Event != "MERGED" {
		t.Fatalf("expected MERGED, got %+v", r)
	}
	// State should be updated.
	if len(rs.Merged) != 1 || rs.Merged[0] != "T1" {
		t.Errorf("rs.Merged = %v", rs.Merged)
	}
	// DAG status should be merged.
	t1, _ := dag.TaskByID("T1")
	if t1.Status != TaskMerged {
		t.Errorf("T1 status = %q, want merged", t1.Status)
	}
}

func TestMergeWrongBaseBlocks(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main999") // different SHA, not an ancestor
	var p Progress
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1"}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "pipe123"}

	r := Merge(git, ws, &p, dag, rs, "T1")
	if r.Merged {
		t.Fatal("expected BLOCKED on wrong base")
	}
	if r.Event != "BLOCKED" {
		t.Errorf("Event = %q", r.Event)
	}
}

func TestMergeConflictBlocks(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main123")
	git.SetAncestor("main123", "pipe123")
	git.SetBranch("sdd/T1")
	git.SetMergeFFError("sdd/T1", errMergeConflict)
	var p Progress
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1"}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "pipe123"}

	r := Merge(git, ws, &p, dag, rs, "T1")
	if r.Merged {
		t.Fatal("expected BLOCKED on merge conflict")
	}
	if r.Event != "BLOCKED" {
		t.Errorf("Event = %q", r.Event)
	}
}

var errMergeConflict = stringError("not a fast forward")

type stringError string

func (e stringError) Error() string { return string(e) }
