package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewPathsCreatesDirAndGitignore(t *testing.T) {
	root := t.TempDir()
	p, err := NewPaths(root, "2026-07-27-sample-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	want := filepath.Join(root, ".marshal", "pipeline", "2026-07-27-sample-plan")
	if p.Dir != want {
		t.Errorf("Dir = %q, want %q", p.Dir, want)
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatalf("stat run dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".marshal", "pipeline", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if strings.TrimSpace(string(data)) != "*" {
		t.Errorf(".gitignore = %q, want %q", data, "*")
	}
}

func TestNewPathsIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, err := NewPaths(root, "slug"); err != nil {
		t.Fatalf("first NewPaths: %v", err)
	}
	if _, err := NewPaths(root, "slug"); err != nil {
		t.Fatalf("second NewPaths: %v", err)
	}
}

func TestPathsFilenames(t *testing.T) {
	p := Paths{Dir: "/run"}
	cases := []struct{ got, want string }{
		{p.Brief(3), "/run/task-3-brief.md"},
		{p.Report(3), "/run/task-3-report.md"},
		{p.Package(3, 0), "/run/task-3-review.md"},
		{p.Package(3, 2), "/run/task-3-review-round2.md"},
		{p.BranchPackage(), "/run/branch-review.md"},
		{p.Ledger(), "/run/progress.md"},
		{p.WorktreesDir(), "/run/worktrees"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("path = %q, want %q", c.got, c.want)
		}
	}
}
