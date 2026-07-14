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
