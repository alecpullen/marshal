package pathutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// siblingWorktrees builds a project with two real sibling worktrees and
// returns the symlink-resolved project root plus each worktree, so the paths
// the resolver returns compare equal to the ones the test spells.
func siblingWorktrees(t *testing.T) (project, a, b string) {
	t.Helper()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	a = filepath.Join(project, ".marshal", "worktrees", "a")
	b = filepath.Join(project, ".marshal", "worktrees", "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	return project, a, b
}

// A sibling-relative target under an explicitly granted project root resolves:
// the request is joined once, from the root that owns it, and judged against
// the permits afterwards — joining it separately against every root rejected
// a path that really was inside one.
func TestResolveGrantedSiblingTarget(t *testing.T) {
	project, a, _ := siblingWorktrees(t)
	grants := []RootGrant{{Path: project, Read: true, Write: true}}

	got, err := ResolveGrantedPath(a, grants, filepath.Join("..", "b", "new.txt"), true)
	if err != nil {
		t.Fatalf("ResolveGrantedPath: %v", err)
	}
	want := filepath.Join(project, ".marshal", "worktrees", "b", "new.txt")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A read grant that is not writable stays readable and refuses the write.
func TestReadGrantDoesNotPermitWrite(t *testing.T) {
	project, a, _ := siblingWorktrees(t)
	brief := filepath.Join(project, "brief.md")
	if err := os.WriteFile(brief, []byte("brief\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	grants := []RootGrant{{Path: project, Read: true}}

	got, err := ResolveGrantedPath(a, grants, brief, false)
	if err != nil {
		t.Fatalf("read through a read grant: %v", err)
	}
	if got != brief {
		t.Fatalf("read resolved %q, want %q", got, brief)
	}

	if _, err := ResolveGrantedPath(a, grants, brief, true); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("write err = %v, want ErrOutsideRoots", err)
	}
}

// The absolute spelling of a contained path resolves to the same canonical
// target as its relative spelling.
func TestAbsoluteContainedTarget(t *testing.T) {
	project, a, _ := siblingWorktrees(t)
	grants := []RootGrant{{Path: project, Read: true, Write: true}}

	rel, err := ResolveGrantedPath(a, grants, "sub/new.txt", true)
	if err != nil {
		t.Fatalf("relative: %v", err)
	}
	abs, err := ResolveGrantedPath(a, grants, rel, true)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	if abs != rel {
		t.Fatalf("absolute %q and relative %q disagree", abs, rel)
	}
}

// A target whose parent tail does not exist yet resolves under the permitted
// real root, while a symlink that leads into an ungranted root does not: the
// judgement is made on the resolved target, not on the request.
func TestNewTailAndSymlinkGrant(t *testing.T) {
	project, a, _ := siblingWorktrees(t)
	extra := filepath.Join(project, "extra")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	granted := []RootGrant{{Path: extra, Read: true, Write: true}}

	want := filepath.Join(extra, "newdir", "deep", "new.txt")
	rel, err := filepath.Rel(a, want)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	got, err := ResolveGrantedPath(a, granted, rel, true)
	if err != nil {
		t.Fatalf("absent parent tail: %v", err)
	}
	if got != want {
		t.Fatalf("absent parent tail = %q, want %q", got, want)
	}

	if err := os.Symlink(outside, filepath.Join(extra, "pivot")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	pivotRel, err := filepath.Rel(a, filepath.Join(extra, "pivot", "new.txt"))
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if _, err := ResolveGrantedPath(a, granted, pivotRel, true); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("symlink into an ungranted root: err = %v, want ErrOutsideRoots", err)
	}
}
