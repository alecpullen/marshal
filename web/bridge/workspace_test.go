package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if err := w.AddProject("/home/u/repo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	a := Agent{ID: "s1", Project: "/home/u/repo", Name: "fix bug", Mode: "edit", CreatedAt: time.Now()}
	if err := w.PutAgent(a); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.Projects(); len(got) != 1 || got[0] != "/home/u/repo" {
		t.Fatalf("Projects() = %v", got)
	}
	got, ok := reloaded.Agent("s1")
	if !ok {
		t.Fatal("agent s1 missing after reload")
	}
	if got.Name != "fix bug" || got.Mode != "edit" {
		t.Fatalf("agent round-trip lost fields: %+v", got)
	}
}

func TestWorkspaceQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWorkspace(path)
	backup, err := w.Load()
	if err != nil {
		t.Fatalf("Load must not fail on corrupt file: %v", err)
	}
	if backup == "" {
		t.Fatal("Load must report the quarantine path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
	if len(w.Projects()) != 0 {
		t.Fatalf("expected empty projects after quarantine, got %v", w.Projects())
	}
	if err := w.AddProject("/home/u/repo"); err != nil {
		t.Fatalf("AddProject after quarantine: %v", err)
	}
}

func TestWorkspaceRemoveProjectDropsItsAgents(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.AddProject("/home/u/a")
	_ = w.AddProject("/home/u/b")
	_ = w.PutAgent(Agent{ID: "s1", Project: "/home/u/a"})
	_ = w.PutAgent(Agent{ID: "s2", Project: "/home/u/b"})

	if err := w.RemoveProject("/home/u/a"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, ok := w.Agent("s1"); ok {
		t.Error("agent of removed project must be dropped")
	}
	if _, ok := w.Agent("s2"); !ok {
		t.Error("agent of other project must survive")
	}
}

func TestWorkspaceMarkAllInterrupted(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.PutAgent(Agent{ID: "s1"})
	if err := w.MarkAllInterrupted(); err != nil {
		t.Fatalf("MarkAllInterrupted: %v", err)
	}
	got, _ := w.Agent("s1")
	if !got.Interrupted {
		t.Error("agent must be marked interrupted")
	}
}

func TestWorkspaceAddProjectRejectsRelative(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("relative/path"); err == nil {
		t.Fatal("expected error for relative project root")
	}
}

func TestWorkspaceAddProjectIsIdempotent(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.AddProject("/home/u/repo")
	_ = w.AddProject("/home/u/repo")
	if got := w.Projects(); len(got) != 1 {
		t.Fatalf("expected 1 project, got %v", got)
	}
}
