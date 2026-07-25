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

	path, err := w.Create("T1")
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

	path, err := w.Create("T1")
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
	if _, err := w.Create("NOPE"); err == nil {
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

	path, err := w.Create("T1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".marshal", "sdd", "worktrees", "T1")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}
