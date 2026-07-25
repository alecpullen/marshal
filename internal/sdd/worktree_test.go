package sdd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeCreateJIT(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	w := NewWorktree(ws, &DAG{Tasks: []DAGTask{
		{ID: "T1", Title: "Foundation"},
	}}, git)

	path, err := w.Create("T1", "sdd/feature")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasSuffix(path, "T1") {
		t.Fatalf("path = %q, want suffix T1", path)
	}
	// DAGTask should now have the worktree metadata.
	t1, _ := w.DAG.TaskByID("T1")
	if t1.Branch != "sdd/T1" {
		t.Errorf("Branch = %q, want sdd/T1", t1.Branch)
	}
	if t1.Base != "pipe123" {
		t.Errorf("Base = %q, want pipe123", t1.Base)
	}
	if t1.WorktreePath != path {
		t.Errorf("WorktreePath = %q, want %q", t1.WorktreePath, path)
	}
	// GitOps should have been called to create the worktree.
	calls := git.Calls("worktree")
	if len(calls) != 1 {
		t.Fatalf("worktree calls = %d, want 1", len(calls))
	}
}

func TestWorktreeCreateIdempotentResume(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	// Pre-existing branch and recorded metadata: the FakeGitOps will
	// return an error on WorktreeAdd (because the branch is already
	// registered), which is exactly the idempotency case.
	git.SetRef("sdd/feature", "pipe123")
	git.SetBranch("sdd/T1")
	w := NewWorktree(ws, &DAG{Tasks: []DAGTask{
		{ID: "T1", Title: "Foundation", Branch: "sdd/T1", Base: "old456", WorktreePath: "/tmp/sdd/T1"},
	}}, git)

	path, err := w.Create("T1", "sdd/feature")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if path != "/tmp/sdd/T1" {
		t.Errorf("path = %q, want /tmp/sdd/T1 (idempotent resume)", path)
	}
	// No new worktree call should have been issued.
	if calls := git.Calls("worktree"); len(calls) != 0 {
		t.Errorf("worktree calls = %d, want 0 (resume path)", len(calls))
	}
}

func TestWorktreeCreateUnknownTask(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	git := NewFakeGitOps()
	w := NewWorktree(ws, &DAG{}, git)
	if _, err := w.Create("NOPE", "sdd/feature"); err == nil {
		t.Fatal("expected error for unknown task id")
	}
}

func TestWorktreePathUsesWorktreesDir(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	w := NewWorktree(ws, &DAG{Tasks: []DAGTask{
		{ID: "T1", Title: "x"},
	}}, git)

	path, err := w.Create("T1", "sdd/feature")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".marshal", "sdd", "worktrees", "T1")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestWorktreeCreateForce(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "newHEAD")
	// NOTE: we do NOT pre-register sdd/T1 as a branch. CreateForce does not
	// delete the branch before recreating (that's P3's branch-base-guard).
	// The DAGTask metadata records the old branch name, but the branch may
	// not actually exist in git after a prior cleanup.
	// Pre-seed a DAGTask with old metadata (simulating a prior failed run).
	w := NewWorktree(ws, &DAG{Tasks: []DAGTask{
		{ID: "T1", Title: "Foundation", Branch: "sdd/T1", Base: "old456", WorktreePath: "/tmp/sdd/T1"},
	}}, git)

	path, err := w.CreateForce("T1", "sdd/feature")
	if err != nil {
		t.Fatalf("CreateForce: %v", err)
	}
	// The returned path should be the new canonical path, not the old one.
	wantPath := filepath.Join(dir, ".marshal", "sdd", "worktrees", "T1")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	// DAGTask should be updated with new metadata.
	t1, _ := w.DAG.TaskByID("T1")
	if t1.Branch != "sdd/T1" {
		t.Errorf("Branch = %q, want sdd/T1", t1.Branch)
	}
	if t1.Base != "newHEAD" {
		t.Errorf("Base = %q, want newHEAD", t1.Base)
	}
	if t1.WorktreePath != path {
		t.Errorf("WorktreePath = %q, want %q", t1.WorktreePath, path)
	}
	// Should have recorded at least one worktree call (remove + add).
	calls := git.Calls("worktree")
	if len(calls) < 1 {
		t.Fatalf("worktree calls = %d, want >= 1", len(calls))
	}
}

func TestWorktreeCleanupStale(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	git := NewFakeGitOps()
	// DAG has only T1.
	w := NewWorktree(ws, &DAG{Tasks: []DAGTask{
		{ID: "T1", Title: "Foundation"},
	}}, git)
	// Pre-seed orphan branches in the fake.
	git.SetBranch("sdd/T2")
	git.SetBranch("sdd/T3")

	if err := w.CleanupStale(); err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	// The fake's ListSDDBranches returns sdd/T2 and sdd/T3. Both are stale
	// (not in the DAG). WorktreeRemove is called for each (best-effort).
	// The branches are deleted from the fake.
	if git.BranchExists("sdd/T2") {
		t.Error("sdd/T2 should be deleted")
	}
	if git.BranchExists("sdd/T3") {
		t.Error("sdd/T3 should be deleted")
	}
}

func TestWorktreeCleanupStaleUsesInterface(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetBranch("sdd/T1")
	git.SetBranch("sdd/T3") // orphan
	dag := &DAG{Tasks: []DAGTask{{ID: "T1"}}}
	w := NewWorktree(ws, dag, git)
	if err := w.CleanupStale(); err != nil {
		t.Fatal(err)
	}
	// sdd/T3 should be deleted; sdd/T1 should remain.
	if !git.BranchExists("sdd/T1") {
		t.Error("sdd/T1 should remain")
	}
	if git.BranchExists("sdd/T3") {
		t.Error("sdd/T3 should be deleted")
	}
}
