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
