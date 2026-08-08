package sddauthor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeModelRunner struct {
	run func(context.Context, string) error
}

func (f fakeModelRunner) Run(ctx context.Context, prompt string) error {
	return f.run(ctx, prompt)
}

func TestRunnerInspectsTheWrittenPlan(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, ".marshal", "plans", "feature.md")
	model := fakeModelRunner{run: func(_ context.Context, prompt string) error {
		if !strings.Contains(prompt, "approved design") {
			t.Fatalf("prompt omitted design handoff: %q", prompt)
		}
		if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(planPath, []byte("# Plan\n\n## Task 1: Add file\n\n"+
			"```marshal.file path=\"created.txt\"\nhello\n```\n"), 0o644)
	}}
	author := NewRunner(model)

	result, err := author.Run(context.Background(), Request{
		Goal:     "add a file",
		Design:   "approved design",
		RepoRoot: root,
		PlanPath: planPath,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Inspection == nil || result.Inspection.Report.Total.DetOps != 1 {
		t.Fatalf("inspection = %+v", result.Inspection)
	}
}

func TestRunnerRejectsMissingCandidate(t *testing.T) {
	root := t.TempDir()
	author := NewRunner(fakeModelRunner{run: func(context.Context, string) error { return nil }})
	_, err := author.Run(context.Background(), Request{
		Goal: "missing", RepoRoot: root, PlanPath: filepath.Join(root, "missing.md"),
	})
	if err == nil || !strings.Contains(err.Error(), "candidate plan") {
		t.Fatalf("err = %v, want missing candidate error", err)
	}
}

func TestRemoveCandidateRemovesOnlyGeneratedPlan(t *testing.T) {
	root := t.TempDir()
	plansDir := filepath.Join(root, ".marshal", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(plansDir, "feature.md")
	if err := os.WriteFile(generated, []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "app.go")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	traversal := filepath.Join(root, "..", "escape.md")

	if err := RemoveCandidate(generated, root, ".marshal/plans"); err != nil {
		t.Fatalf("RemoveCandidate(generated): %v", err)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Error("generated plan should be removed")
	}
	if err := RemoveCandidate(source, root, ".marshal/plans"); err == nil {
		t.Error("RemoveCandidate must reject a source path")
	}
	if err := RemoveCandidate(traversal, root, ".marshal/plans"); err == nil {
		t.Error("RemoveCandidate must reject a traversal path")
	}
	if _, err := os.Stat(source); err != nil {
		t.Error("source file must be untouched")
	}
}

// TestRunnerRejectsSymlinkedPlansDir ensures a symlinked plans_dir that
// resolves outside the repository cannot be used to land a candidate
// plan. The containment check must canonicalize before comparing so a
// relative repoRoot that itself points inside /private via /var/tmp
// cannot be confused by the plans-dir alias.
func TestRunnerRejectsSymlinkedPlansDir(t *testing.T) {
	repoRoot := t.TempDir()
	external := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repoRoot, ".marshal", "plans")); err != nil {
		t.Skipf("symlink(2) unavailable on this platform: %v", err)
	}
	// Candidate lives inside the symlinked directory; the lexical
	// repository path is inside repoRoot. Canonical resolution must
	// detect the escape.
	planPath := filepath.Join(filepath.Join(repoRoot, ".marshal", "plans"), "feature.md")
	if err := os.WriteFile(planPath, []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	author := NewRunner(fakeModelRunner{run: func(context.Context, string) error { return nil }})
	_, err := author.Run(context.Background(), Request{
		Goal:     "feature",
		RepoRoot: repoRoot,
		PlanPath: planPath,
	})
	if err == nil || !strings.Contains(err.Error(), "outside the repository root") {
		t.Fatalf("err = %v, want symlink-escape rejection", err)
	}
}
