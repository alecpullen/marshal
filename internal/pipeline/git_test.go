package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a git repo with one commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeRepoFile(t, dir, "seed.txt", "seed\n")
	commitAll(t, dir, "seed")
	return dir
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestCLIGitOpsDirtyCommitAndRange(t *testing.T) {
	repo := initRepo(t)
	g := CLIGitOps{}

	dirty, err := g.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if dirty {
		t.Fatal("IsDirty on a clean repo = true, want false")
	}

	base, err := g.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}

	writeRepoFile(t, repo, "new.txt", "hello\n")
	dirty, err = g.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if !dirty {
		t.Fatal("IsDirty with an untracked file = false, want true")
	}

	head, err := g.CommitAll(repo, "task 1 — first thing")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if head == base {
		t.Fatal("CommitAll returned the old HEAD")
	}
	dirty, _ = g.IsDirty(repo)
	if dirty {
		t.Fatal("IsDirty after CommitAll = true, want false")
	}

	rng := base + ".." + head
	log, err := g.LogOneline(repo, rng)
	if err != nil {
		t.Fatalf("LogOneline: %v", err)
	}
	if !strings.Contains(log, "task 1 — first thing") {
		t.Errorf("LogOneline = %q, want the commit subject", log)
	}
	stat, err := g.DiffStat(repo, rng)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !strings.Contains(stat, "new.txt") {
		t.Errorf("DiffStat = %q, want new.txt", stat)
	}
	diff, err := g.Diff(repo, rng, 10)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+hello") {
		t.Errorf("Diff = %q, want the added line", diff)
	}
}

func TestCLIGitOpsCommitAllNothingToCommit(t *testing.T) {
	repo := initRepo(t)
	g := CLIGitOps{}
	if _, err := g.CommitAll(repo, "empty"); err == nil {
		t.Fatal("CommitAll with a clean tree: want error, got nil")
	}
}
