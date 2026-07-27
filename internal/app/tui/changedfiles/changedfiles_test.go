package changedfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

func initRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return string(out[:len(out)-1])
}

func TestReadModifiedAndAdded(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "b.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	got := Read(dir, base)
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(got), got)
	}
	byPath := map[string]int{}
	for _, f := range got {
		byPath[f.Path] = f.Added
	}
	if byPath["a.txt"] != 1 {
		t.Errorf("a.txt added = %d, want 1", byPath["a.txt"])
	}
	if byPath["b.txt"] != 1 {
		t.Errorf("b.txt added = %d, want 1", byPath["b.txt"])
	}
}

func TestReadCleanTree(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)
	if got := Read(dir, base); len(got) != 0 {
		t.Errorf("Read(clean) = %+v, want empty", got)
	}
}

func TestReadNonRepoReturnsNil(t *testing.T) {
	gitOrSkip(t)
	if got := Read(t.TempDir(), "HEAD"); got != nil {
		t.Errorf("Read(non-repo) = %+v, want nil", got)
	}
}

func TestReadEmptyBaseRefReturnsNil(t *testing.T) {
	if got := Read(t.TempDir(), ""); got != nil {
		t.Errorf("Read(no base ref) = %+v, want nil", got)
	}
}
