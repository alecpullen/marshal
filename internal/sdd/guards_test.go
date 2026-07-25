package sdd

import (
	"testing"
)

func TestAllowedFilesCheckPass(t *testing.T) {
	git := NewFakeGitOps()
	// DiffStat returns canned output.
	git.SetDiffStat("base123", "sdd/T1", " internal/foo.go | 2 +-\n internal/bar.go | 1 +\n")
	c := &Contract{AllowedFiles: "- internal/foo.go\n- internal/bar.go\n"}
	r := AllowedFilesCheck(git, "sdd/T1", "base123", c)
	if !r.Pass {
		t.Fatalf("expected Pass, got %+v", r)
	}
	if r.Event != "ALLOWED_FILES_PASS" {
		t.Errorf("Event = %q", r.Event)
	}
}

func TestAllowedFilesCheckViolation(t *testing.T) {
	git := NewFakeGitOps()
	git.SetDiffStat("base123", "sdd/T1", " internal/foo.go | 2 +-\n internal/sneaky.go | 1 +\n")
	c := &Contract{AllowedFiles: "- internal/foo.go\n"}
	r := AllowedFilesCheck(git, "sdd/T1", "base123", c)
	if r.Pass {
		t.Fatal("expected violation, got Pass")
	}
	if r.Event != "ALLOWED_FILES_VIOLATION" {
		t.Errorf("Event = %q", r.Event)
	}
	if len(r.Files) != 1 || r.Files[0] != "internal/sneaky.go" {
		t.Errorf("Files = %v, want [internal/sneaky.go]", r.Files)
	}
}

func TestAllowedFilesCheckEmptyDiffPasses(t *testing.T) {
	git := NewFakeGitOps()
	git.SetDiffStat("base123", "sdd/T1", "")
	c := &Contract{AllowedFiles: "- internal/foo.go\n"}
	r := AllowedFilesCheck(git, "sdd/T1", "base123", c)
	if !r.Pass {
		t.Fatalf("empty diff should pass, got %+v", r)
	}
}

func TestEditGuardClean(t *testing.T) {
	git := NewFakeGitOps()
	// Pipeline branch diff shows only .marshal/sdd/ files (expected).
	git.SetDiffStat("pipeBase", "sdd/feature", " .marshal/sdd/progress.md | 2 +-\n .marshal/sdd/dag.json | 1 +\n")
	r := EditGuard(git, "sdd/feature", "pipeBase", &DAG{})
	if !r.Pass {
		t.Fatalf("expected clean (only .marshal/sdd/ edits), got %+v", r)
	}
	if r.Event != "EDIT_GUARD_CLEAN" {
		t.Errorf("Event = %q", r.Event)
	}
}

func TestEditGuardCatchesSourceEdit(t *testing.T) {
	git := NewFakeGitOps()
	git.SetDiffStat("pipeBase", "sdd/feature", " .marshal/sdd/progress.md | 1 +\n internal/foo.go | 2 +-\n")
	r := EditGuard(git, "sdd/feature", "pipeBase", &DAG{})
	if r.Pass {
		t.Fatal("expected violation (internal/foo.go edited on pipeline branch)")
	}
	if r.Event != "ORCHESTRATOR_EDIT" {
		t.Errorf("Event = %q", r.Event)
	}
	if len(r.Files) != 1 || r.Files[0] != "internal/foo.go" {
		t.Errorf("Files = %v, want [internal/foo.go]", r.Files)
	}
}

func TestEditGuardEmptyDiffPasses(t *testing.T) {
	git := NewFakeGitOps()
	git.SetDiffStat("pipeBase", "sdd/feature", "")
	r := EditGuard(git, "sdd/feature", "pipeBase", &DAG{})
	if !r.Pass {
		t.Fatalf("empty diff should pass, got %+v", r)
	}
}

func TestAllowedFilesCheckBulletStripping(t *testing.T) {
	git := NewFakeGitOps()
	git.SetDiffStat("base123", "sdd/T1", " internal/foo.go | 2 +-\n")
	// AllowedFiles with no bullets, just paths on lines.
	c := &Contract{AllowedFiles: "internal/foo.go\n"}
	r := AllowedFilesCheck(git, "sdd/T1", "base123", c)
	if !r.Pass {
		t.Fatalf("bare-path allowed list should match, got %+v", r)
	}
}
