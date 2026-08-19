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

func TestReadModifiedFileOnlyAdditionsGetsM(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)

	// Append lines to existing file — numstat shows additions-only.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := Read(dir, base)
	if len(got) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(got), got)
	}
	if got[0].Path != "a.txt" {
		t.Errorf("path = %q, want a.txt", got[0].Path)
	}
	if got[0].Status != 'M' {
		t.Errorf("status = %q, want 'M' (modified, not added)", got[0].Status)
	}
}

func TestParseNameStatusRenameKeysByNewPath(t *testing.T) {
	// A rename line carries two paths; the map must be keyed by the new
	// path so it matches the entries parseNumstat produces.
	m := parseNameStatus("R100\told.txt\tnew.txt\nM\tmodified.txt\n")
	if got := m["new.txt"]; got != 'R' {
		t.Errorf("new.txt status = %q, want 'R'", got)
	}
	if _, ok := m["old.txt"]; ok {
		t.Error("old.txt should not be a key (numstat reports the new path)")
	}
	if got := m["modified.txt"]; got != 'M' {
		t.Errorf("modified.txt status = %q, want 'M'", got)
	}
}

func TestReadUntrackedUnstagedNewFileIncluded(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)

	// Create a new file but do NOT stage it. The diff passes won't see it;
	// only the ls-files --others pass reports it.
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := Read(dir, base)
	found := false
	for _, f := range got {
		if f.Path == "untracked.txt" {
			found = true
			if f.Status != 'A' {
				t.Errorf("status = %q, want 'A' (added)", f.Status)
			}
		}
	}
	if !found {
		t.Fatalf("untracked.txt not in results: %+v", got)
	}
}

func TestReadIgnoresGitignoredUntracked(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)

	// A gitignored file must never surface in the rail.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write ignored.txt: %v", err)
	}

	got := Read(dir, base)
	for _, f := range got {
		if f.Path == "ignored.txt" {
			t.Fatalf("gitignored file should not appear: %+v", got)
		}
	}
}

func TestReadNewFileGetsA(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	base := initRepo(t, dir)

	// Create a genuinely new file and stage it.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "new.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	got := Read(dir, base)
	found := false
	for _, f := range got {
		if f.Path == "new.txt" {
			found = true
			if f.Status != 'A' {
				t.Errorf("status = %q, want 'A' (added)", f.Status)
			}
		}
	}
	if !found {
		t.Fatalf("new.txt not in results: %+v", got)
	}
}
