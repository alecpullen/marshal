package native

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeResolve_NormalFile(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "sub"))
	mustWrite(t, filepath.Join(root, "sub", "f.txt"), "x")

	got, err := SafeResolve(root, "sub/f.txt")
	if err != nil {
		t.Fatalf("SafeResolve: %v", err)
	}
	// SafeResolve performs EvalSymlinks on root and the resolved path,
	// so we need to expect the real filesystem path (e.g. /private/var
	// on macOS where /var is a symlink).
	want, err := filepath.EvalSymlinks(filepath.Join(root, "sub", "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafeResolve_DotDotTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := SafeResolve(root, "../etc/passwd"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("dotdot: got %v, want ErrPathEscapes", err)
	}
}

func TestSafeResolve_AbsoluteRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := SafeResolve(root, "/etc/passwd"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("absolute: got %v, want ErrPathEscapes", err)
	}
}

func TestSafeResolve_NewFile(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "sub"))

	// The leaf file doesn't exist yet — SafeResolve should still resolve
	// the path by resolving the parent and appending the leaf.
	got, err := SafeResolve(root, "sub/newfile.txt")
	if err != nil {
		t.Fatalf("SafeResolve new file: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(want, "newfile.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafeResolve_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeResolve(root, "link/secret.txt"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("symlink: got %v, want ErrPathEscapes", err)
	}
}

func TestDetectDoubledWorktree_Hit(t *testing.T) {
	p := filepath.Join(".marshal", "worktrees", "feat-x", ".marshal", "worktrees", "feat-x", "foo.go")
	got, ok := detectDoubledWorktree(p)
	if !ok {
		t.Fatalf("detectDoubledWorktree(%q) = _, false; want true", p)
	}
	want := filepath.Join(".marshal", "worktrees", "feat-x", "foo.go")
	if got != want {
		t.Errorf("detectDoubledWorktree(%q) = %q, want %q", p, got, want)
	}
}

func TestDetectDoubledWorktree_Miss(t *testing.T) {
	p := filepath.Join(".marshal", "worktrees", "feat-x", "foo.go")
	if got, ok := detectDoubledWorktree(p); ok {
		t.Errorf("detectDoubledWorktree(%q) = %q, true; want false", p, got)
	}
}

func TestExpandHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := expandHomeDir("~/x")
	if err != nil {
		t.Fatalf("expandHomeDir(~/x): %v", err)
	}
	if want := filepath.Join(home, "x"); got != want {
		t.Errorf("expandHomeDir(~/x) = %q, want %q", got, want)
	}

	if got, err := expandHomeDir("/etc/passwd"); err != nil || got != "/etc/passwd" {
		t.Errorf("expandHomeDir(/etc/passwd) = %q, %v; want unchanged", got, err)
	}

	if got, err := expandHomeDir("relative/x"); err != nil || got != "relative/x" {
		t.Errorf("expandHomeDir(relative/x) = %q, %v; want unchanged", got, err)
	}
}

// TestIsReadAllowedOutsideWorkspace pins the read-only Marshal-owned
// allowlist. It injects a fake HOME so the assertion is hermetic. The
// postmortems-evil case guards against a naive strings.HasPrefix that would
// accept a sibling directory sharing the allowlisted prefix.
func TestIsReadAllowedOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	allowed := []string{
		filepath.Join(home, ".config", "marshal", "postmortems", "p", "sess_x.json"),
		filepath.Join(home, ".config", "marshal", "skills", "foo", "SKILL.md"),
	}
	for _, abs := range allowed {
		if !IsReadAllowedOutsideWorkspace(abs) {
			t.Errorf("IsReadAllowedOutsideWorkspace(%q) = false, want true", abs)
		}
	}

	denied := []string{
		"/etc/passwd",
		// config.toml can carry literal provider api_key values, so it is
		// deliberately not readable; config.read serves it masked.
		filepath.Join(home, ".config", "marshal", "config.toml"),
		filepath.Join(home, ".config", "marshal", "other.txt"),
		filepath.Join(home, ".config", "marshal", "postmortems-evil", "x.json"),
	}
	for _, abs := range denied {
		if IsReadAllowedOutsideWorkspace(abs) {
			t.Errorf("IsReadAllowedOutsideWorkspace(%q) = true, want false", abs)
		}
	}
}

// TestIsReadAllowedOutsideWorkspace_SymlinkPivot pins that a symlink planted
// inside an allowlisted directory cannot pivot the read anywhere on disk.
// The comparison runs on the RESOLVED target, not the cleaned request.
func TestIsReadAllowedOutsideWorkspace_SymlinkPivot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "TOP-SECRET\n")

	postmortems := filepath.Join(home, ".config", "marshal", "postmortems")
	mustMkdir(t, postmortems)
	if err := os.Symlink(outside, filepath.Join(postmortems, "leak")); err != nil {
		t.Fatal(err)
	}

	pivot := filepath.Join(postmortems, "leak", "secret.txt")
	if IsReadAllowedOutsideWorkspace(pivot) {
		t.Errorf("IsReadAllowedOutsideWorkspace(%q) = true, want false (symlink pivot)", pivot)
	}

	// The allowlisted directory itself stays readable through its real path.
	if !IsReadAllowedOutsideWorkspace(filepath.Join(postmortems, "real.json")) {
		t.Error("allowlisted directory became unreadable")
	}
}

// TestIsReadAllowedOutsideWorkspace_RootSymlinkRejected pins that an
// allowlisted directory which is ITSELF a symlink does not become an alias for
// wherever it points. Resolving both sides closes pivots inside the tree, but
// a symlink at the entry itself would otherwise re-open the pivot for every
// path beneath it.
func TestIsReadAllowedOutsideWorkspace_RootSymlinkRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	victim := t.TempDir()
	mustWrite(t, filepath.Join(victim, "secret.txt"), "TOP-SECRET\n")

	marshalDir := filepath.Join(home, ".config", "marshal")
	mustMkdir(t, marshalDir)
	if err := os.Symlink(victim, filepath.Join(marshalDir, "postmortems")); err != nil {
		t.Fatal(err)
	}

	if IsReadAllowedOutsideWorkspace(filepath.Join(marshalDir, "postmortems", "secret.txt")) {
		t.Error("a symlinked allowlist root was accepted as an alias for its target")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
