package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes a command in the given directory.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s %v\noutput: %s\nerr: %v", name, args, output, err)
	}
}

// setupTempRepo creates a fresh git repository with an initial commit.
func setupTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize repo
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")

	// Create initial commit
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	run(t, dir, "git", "add", "README.md")
	run(t, dir, "git", "commit", "-m", "init")

	return dir
}

func TestNew_ValidRepo(t *testing.T) {
	dir := setupTempRepo(t)

	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if g.RepoRoot() == "" {
		t.Error("RepoRoot should not be empty")
	}

	if g.BaseBranch() != "main" && g.BaseBranch() != "master" {
		t.Errorf("BaseBranch should be main or master, got: %s", g.BaseBranch())
	}
}

func TestNew_InvalidRepo(t *testing.T) {
	dir := t.TempDir() // Not a git repo

	_, err := New(dir)
	if err == nil {
		t.Error("New should fail for non-git directory")
	}
}

func TestGit_CreateIsolationBranch(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	branchName := "test-branch"
	if err := g.CreateIsolationBranch(branchName); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	current, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}

	if current != branchName {
		t.Errorf("Expected current branch %s, got %s", branchName, current)
	}
}

func TestGit_CreateIsolationBranch_Duplicate(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	branchName := "test-branch"
	if err := g.CreateIsolationBranch(branchName); err != nil {
		t.Fatalf("First CreateIsolationBranch failed: %v", err)
	}

	// Try to create again - should fail
	if err := g.CreateIsolationBranch(branchName); err == nil {
		t.Error("CreateIsolationBranch should fail for duplicate branch name")
	}
}

func TestGit_GetDiff(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Initially, no changes
	diff, err := g.GetDiff()
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff != "" {
		t.Errorf("Expected empty diff, got: %s", diff)
	}

	// Make a change
	g.CreateIsolationBranch("test-branch")
	testFile := filepath.Join(dir, "test.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Now should have a diff
	diff, err = g.GetDiff()
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff == "" {
		t.Error("Expected non-empty diff after file creation")
	}
	if !strings.Contains(diff, "test.go") {
		t.Error("Diff should mention test.go")
	}
}

func TestGit_StageAndCommit(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	g.CreateIsolationBranch("test-branch")

	// Create a file
	testFile := filepath.Join(dir, "feature.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Commit
	commitMsg := "Add feature"
	if err := g.StageAndCommit(commitMsg); err != nil {
		t.Fatalf("StageAndCommit failed: %v", err)
	}

	// After commit, diff should be empty
	diff, err := g.GetDiff()
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff != "" {
		t.Errorf("Expected empty diff after commit, got: %s", diff)
	}

	// Verify commit exists
	cmd := exec.Command("git", "-C", dir, "log", "--oneline", "-1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(string(output), commitMsg) {
		t.Errorf("Commit message not found in log: %s", output)
	}
}

func TestGit_StageAndCommit_NoChanges(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	g.CreateIsolationBranch("test-branch")

	// Commit with no changes - should succeed without error
	if err := g.StageAndCommit("Empty commit"); err != nil {
		t.Fatalf("StageAndCommit should succeed with no changes: %v", err)
	}
}

func TestGit_HardResetToHead(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	g.CreateIsolationBranch("test-branch")

	// Create and commit a file
	testFile := filepath.Join(dir, "file.go")
	if err := os.WriteFile(testFile, []byte("original content\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := g.StageAndCommit("Add file"); err != nil {
		t.Fatalf("StageAndCommit failed: %v", err)
	}

	// Modify the file
	if err := os.WriteFile(testFile, []byte("modified content\n"), 0644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	// Verify file is modified
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "modified content\n" {
		t.Error("File should have modified content")
	}

	// Reset
	if err := g.HardResetToHead(); err != nil {
		t.Fatalf("HardResetToHead failed: %v", err)
	}

	// Verify file is back to original
	content, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file after reset: %v", err)
	}
	if string(content) != "original content\n" {
		t.Errorf("Expected original content after reset, got: %s", content)
	}
}

func TestGit_DeleteBranch(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	branchName := "feature-branch"
	g.CreateIsolationBranch(branchName)

	// Verify branch exists
	cmd := exec.Command("git", "-C", dir, "branch", "--list", branchName)
	output, _ := cmd.CombinedOutput()
	if !strings.Contains(string(output), branchName) {
		t.Fatal("Branch should exist before deletion")
	}

	// Delete branch
	if err := g.DeleteBranch(branchName); err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}

	// Verify branch is gone
	cmd = exec.Command("git", "-C", dir, "branch", "--list", branchName)
	output, _ = cmd.CombinedOutput()
	if strings.Contains(string(output), branchName) {
		t.Error("Branch should not exist after deletion")
	}
}

func TestGit_DeleteBranch_Current(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create and switch to new branch
	branchName := "current-branch"
	if err := g.CreateIsolationBranch(branchName); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	// Delete current branch (should switch to base first)
	if err := g.DeleteBranch(branchName); err != nil {
		t.Fatalf("DeleteBranch should handle current branch: %v", err)
	}

	// Verify we're on base branch now
	current, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if current != g.BaseBranch() {
		t.Errorf("Should be on base branch %s, got %s", g.BaseBranch(), current)
	}
}

func TestGit_MergeBranch(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	branchName := "feature-branch"
	if err := g.CreateIsolationBranch(branchName); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	// Create a file and commit
	testFile := filepath.Join(dir, "feature.go")
	if err := os.WriteFile(testFile, []byte("package feature\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := g.StageAndCommit("Add feature"); err != nil {
		t.Fatalf("StageAndCommit failed: %v", err)
	}

	// Merge back to base
	mergeMsg := "Merge feature branch"
	if err := g.MergeBranch(branchName, mergeMsg); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify we're on base branch
	current, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if current != g.BaseBranch() {
		t.Errorf("Should be on base branch after merge, got: %s", current)
	}

	// Verify merge commit exists
	cmd := exec.Command("git", "-C", dir, "log", "--oneline", "-1")
	output, _ := cmd.CombinedOutput()
	if !strings.Contains(string(output), mergeMsg) {
		t.Errorf("Merge commit message not found in log: %s", output)
	}
}

func TestGit_IsWorkingTreeDirty(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	g.CreateIsolationBranch("test-branch")

	// Clean tree
	dirty, err := g.IsWorkingTreeDirty()
	if err != nil {
		t.Fatalf("IsWorkingTreeDirty failed: %v", err)
	}
	if dirty {
		t.Error("Working tree should be clean initially")
	}

	// Create a file
	testFile := filepath.Join(dir, "dirty.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Now dirty
	dirty, err = g.IsWorkingTreeDirty()
	if err != nil {
		t.Fatalf("IsWorkingTreeDirty failed: %v", err)
	}
	if !dirty {
		t.Error("Working tree should be dirty after file creation")
	}
}

func TestAdapter_InterfaceCompliance(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// This should compile - verifies interface compliance
	var _ interface {
		CreateIsolationBranch(name string) error
		GetDiff() (string, error)
		StageAndCommit(message string) error
		HardResetToHead() error
		DeleteBranch(name string) error
	} = NewAdapter(g)
}

func TestAdapter_Methods(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	adapter := NewAdapter(g)

	// Test all methods work through adapter
	if err := adapter.CreateIsolationBranch("adapter-test"); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	// Create and commit a file through adapter
	testFile := filepath.Join(dir, "adapter.go")
	if err := os.WriteFile(testFile, []byte("package test\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := adapter.StageAndCommit("Adapter test commit"); err != nil {
		t.Fatalf("StageAndCommit failed: %v", err)
	}

	diff, err := adapter.GetDiff()
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff != "" {
		t.Errorf("Expected empty diff after commit, got: %s", diff)
	}

	// Cleanup
	if err := adapter.DeleteBranch("adapter-test"); err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}
}

func TestAdapter_CheckoutBranch(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	adapter := NewAdapter(g)

	// Create a branch first
	if err := adapter.CreateIsolationBranch("checkout-test"); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	// Checkout back to base
	if err := adapter.CheckoutBranch(g.BaseBranch()); err != nil {
		t.Fatalf("CheckoutBranch failed: %v", err)
	}

	// Verify current branch
	current, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if current != g.BaseBranch() {
		t.Errorf("Expected base branch %s, got %s", g.BaseBranch(), current)
	}
}

func TestAdapter_MergeBranch(t *testing.T) {
	dir := setupTempRepo(t)
	g, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	adapter := NewAdapter(g)

	// Create and switch to new branch
	if err := adapter.CreateIsolationBranch("merge-adapter-test"); err != nil {
		t.Fatalf("CreateIsolationBranch failed: %v", err)
	}

	// Create and commit a file
	testFile := filepath.Join(dir, "merge_feature.go")
	if err := os.WriteFile(testFile, []byte("package merge\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := adapter.StageAndCommit("Adapter merge test"); err != nil {
		t.Fatalf("StageAndCommit failed: %v", err)
	}

	// Merge back to base
	mergeMsg := "Merge adapter test branch"
	if err := adapter.MergeBranch("merge-adapter-test", mergeMsg); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify we're on base branch
	current, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if current != g.BaseBranch() {
		t.Errorf("Expected base branch after merge, got: %s", current)
	}
}
