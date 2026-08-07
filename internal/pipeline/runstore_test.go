package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunStoreManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	m := Manifest{
		SchemaVersion:  1,
		RunID:          "run-abc",
		PlanPath:       filepath.Join(dir, "plan.md"),
		PlanHash:       "sha256-hash",
		RepoRoot:       dir,
		TargetBranch:   "main",
		PipelineBranch: "pipeline/test-plan",
		BaseSHA:        "base123",
		WorktreePath:   filepath.Join(paths.Dir, "worktrees", "pipeline-test-plan"),
	}
	if err := rs.CreateManifest(m); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	got, err := rs.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if got.RunID != m.RunID || got.PlanHash != m.PlanHash {
		t.Errorf("manifest round-trip mismatch: got %+v, want %+v", got, m)
	}
}

func TestRunStoreManifestImmutable(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	m := Manifest{RunID: "run-1", PlanPath: "p.md"}
	if err := rs.CreateManifest(m); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	m2 := Manifest{RunID: "run-2", PlanPath: "p.md"}
	if err := rs.CreateManifest(m2); err == nil {
		t.Fatal("second CreateManifest should fail (immutable), got nil")
	}
}

func TestRunStoreManifestMissingIsError(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	if _, err := rs.Manifest(); err == nil {
		t.Fatal("Manifest on missing file should return error")
	}
}

func TestRunStoreCheckpointAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	c1 := Checkpoint{Seq: 1, RunID: "r1", Phase: PhaseImplementing, TaskN: 1}
	c2 := Checkpoint{Seq: 2, RunID: "r1", Phase: PhaseCommitting, TaskN: 1, HeadSHA: "abc123"}
	if err := rs.AppendCheckpoint(c1); err != nil {
		t.Fatalf("AppendCheckpoint 1: %v", err)
	}
	if err := rs.AppendCheckpoint(c2); err != nil {
		t.Fatalf("AppendCheckpoint 2: %v", err)
	}

	last, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !ok {
		t.Fatal("LastCheckpoint returned ok=false")
	}
	if last.Seq != 2 || last.HeadSHA != "abc123" {
		t.Errorf("last = %+v, want seq=2 head=abc123", last)
	}

	all, err := rs.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Replay returned %d, want 2", len(all))
	}
	if all[0].Seq != 1 || all[1].Seq != 2 {
		t.Errorf("replay order wrong: %d %d", all[0].Seq, all[1].Seq)
	}
}

func TestRunStorePartialFinalLineIgnored(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	c1 := Checkpoint{Seq: 1, Phase: PhaseImplementing}
	if err := rs.AppendCheckpoint(c1); err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}
	// Simulate a crash: append a partial line without newline.
	cpPath := rs.checkpointPath()
	f, _ := os.OpenFile(cpPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":2,"phase":"committing"`) // no newline, incomplete JSON
	f.Close()

	last, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !ok || last.Seq != 1 {
		t.Errorf("last = %+v ok=%v, want seq=1", last, ok)
	}
}

func TestRunStoreNoCheckpoints(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	_, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if ok {
		t.Fatal("LastCheckpoint on empty store should return ok=false")
	}
	all, err := rs.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("Replay on empty store returned %d, want 0", len(all))
	}
}
