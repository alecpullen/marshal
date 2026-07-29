package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReviewPackage(t *testing.T) {
	g := NewFakeGitOps()
	g.LogOut = "abc1234 task 1 — first thing"
	g.StatOut = " a.go | 10 ++++++++++"
	g.DiffOut = "diff --git a/a.go b/a.go\n+func A() {}"

	path := filepath.Join(t.TempDir(), "task-1-review.md")
	if err := WriteReviewPackage(g, "/work", "base..head", path); err != nil {
		t.Fatalf("WriteReviewPackage: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	got := string(data)
	for _, want := range []string{"base..head", g.LogOut, g.StatOut, g.DiffOut} {
		if !strings.Contains(got, want) {
			t.Errorf("package missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, g.StatOut) > strings.Index(got, g.DiffOut) {
		t.Error("stat summary must come before the full diff")
	}
}

// A task that needed fix rounds spans several commits. The package must
// cover the whole range, not just its last commit — the trap the
// superpowers skill warns about with HEAD~1.
func TestWriteReviewPackageMultiCommitRange(t *testing.T) {
	repo := initRepo(t)
	g := CLIGitOps{}
	base, err := g.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	writeRepoFile(t, repo, "one.txt", "one\n")
	commitAll(t, repo, "task 1 — first thing")
	writeRepoFile(t, repo, "two.txt", "two\n")
	commitAll(t, repo, "task 1 — review fix (round 1)")
	head, err := g.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}

	path := filepath.Join(t.TempDir(), "task-1-review.md")
	if err := WriteReviewPackage(g, repo, base+".."+head, path); err != nil {
		t.Fatalf("WriteReviewPackage: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	got := string(data)
	for _, want := range []string{"first thing", "review fix (round 1)", "one.txt", "two.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("package missing %q from the multi-commit range:\n%s", want, got)
		}
	}
}
