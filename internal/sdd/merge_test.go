package sdd

import (
	"path/filepath"
	"testing"
)

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

func TestFinalizeConfirmOK(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main123")
	git.SetAncestor("main123", "pipe123")
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main"}
	r := FinalizeConfirm(git, rs)
	if !r.Pass || r.Event != "FINALIZE_OK" {
		t.Fatalf("expected FINALIZE_OK, got %+v", r)
	}
}

func TestFinalizeConfirmWrongBase(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main999")
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main"}
	r := FinalizeConfirm(git, rs)
	if r.Pass {
		t.Fatal("expected WRONG_BASE")
	}
}

func TestCheckpointCreate(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	cp := Checkpoint{Tag: "after-T1", SHA: "abc12345", Timestamp: "2026-07-25T19:00:00Z", Branch: "sdd/feature", Merged: []string{"T1"}, Message: "merged T1"}
	if err := CheckpointCreate(ws, cp); err != nil {
		t.Fatal(err)
	}
	// File should exist at checkpoints/abc12345.json (short SHA).
	path := filepath.Join(ws.Dir(), "checkpoints", "abc12345.json")
	if _, err := stat(path); err != nil {
		t.Fatalf("checkpoint file missing: %v", err)
	}
}

func TestBranchRebranch(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("sdd/newfeature", "newhead456")
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main"}
	if err := BranchRebranch(git, rs, "sdd/newfeature"); err != nil {
		t.Fatal(err)
	}
	if rs.Branch != "sdd/newfeature" {
		t.Errorf("rs.Branch = %q, want sdd/newfeature", rs.Branch)
	}
	if rs.Head != "newhead456" {
		t.Errorf("rs.Head = %q, want newhead456", rs.Head)
	}
}

func TestBranchRebranchUnknownBranch(t *testing.T) {
	git := NewFakeGitOps()
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main"}
	if err := BranchRebranch(git, rs, "sdd/nonexistent"); err == nil {
		t.Fatal("expected error for unknown branch")
	}
}
