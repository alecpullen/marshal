package sdd

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// stat is a thin wrapper around os.Stat for readability in tests.
func stat(path string) (os.FileInfo, error) { return os.Stat(path) }

func TestLoadRepoStateEmptyWhenNoFile(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	rs, err := LoadRepoState(ws)
	if err != nil {
		t.Fatalf("LoadRepoState: %v", err)
	}
	if rs == nil {
		t.Fatal("rs nil")
	}
	if len(rs.Merged) != 0 {
		t.Fatalf("expected empty Merged, got %v", rs.Merged)
	}
}

func TestRepoStateSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	rs := &RepoState{
		Branch:       "sdd/feature",
		TargetBranch: "main",
		Head:         "abc123",
		Merged:       []string{"T1", "T2"},
		MergedAt:     map[string]string{"T1": "2026-07-25T19:00:00Z", "T2": "2026-07-25T19:30:00Z"},
	}
	if err := rs.Save(ws); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadRepoState(ws)
	if err != nil {
		t.Fatalf("LoadRepoState: %v", err)
	}
	// DeepEqual on maps is brittle for nil vs empty; normalize for assertion.
	if !reflect.DeepEqual(got, rs) {
		t.Fatalf("round trip = %#v, want %#v", got, rs)
	}
}

func TestRepoStateMarkMergedIdempotent(t *testing.T) {
	rs := &RepoState{}
	if err := rs.MarkMerged("T1"); err != nil {
		t.Fatal(err)
	}
	if err := rs.MarkMerged("T1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.Merged) != 1 {
		t.Fatalf("expected 1 merged entry, got %v", rs.Merged)
	}
	if rs.MergedAt["T1"] == "" {
		t.Fatal("MergedAt[T1] empty")
	}
	if rs.LastMergeAt == "" {
		t.Fatal("LastMergeAt empty")
	}
}

func TestRepoStateRepairDropsMissingBranches(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	rs := &RepoState{
		Branch:   "sdd/feature",
		Head:     "old123",
		Merged:   []string{"T1", "T2", "T3"},
		MergedAt: map[string]string{"T1": "x", "T2": "y", "T3": "z"},
	}
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "newhash")
	git.SetBranch("sdd/T1")
	git.SetBranch("sdd/T3")
	// T2 is NOT set, so Repair should drop it.

	if err := rs.Repair(ws, git); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if rs.Head != "newhash" {
		t.Fatalf("Head = %q, want newhash", rs.Head)
	}
	sort.Strings(rs.Merged)
	want := []string{"T1", "T3"}
	if !reflect.DeepEqual(rs.Merged, want) {
		t.Fatalf("Merged = %v, want %v", rs.Merged, want)
	}
	if _, ok := rs.MergedAt["T2"]; ok {
		t.Fatal("T2 should be dropped from MergedAt")
	}

	// Repair should have saved the corrected state.
	got, err := LoadRepoState(ws)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got.Merged)
	if !reflect.DeepEqual(got.Merged, want) {
		t.Fatalf("post-save Merged = %v, want %v", got.Merged, want)
	}
}

func TestRepoStateRepairErrorsOnEmptyBranch(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	rs := &RepoState{}
	git := NewFakeGitOps()
	if err := rs.Repair(ws, git); err == nil {
		t.Fatal("expected error when branch not set")
	}
}

func TestLoadRepoStateWritesToRepoPath(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	rs := &RepoState{Branch: "sdd/feature", Head: "abc"}
	if err := rs.Save(ws); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws.Dir(), "state", "repo.json")
	if _, err := filepath.Abs(want); err != nil {
		t.Fatal(err)
	}
	// Confirm the file landed at the documented path (regression guard for the
	// Workspace.RepoPath helper).
	if _, err := stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
}
