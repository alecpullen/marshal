// internal/pathutil/scratch_test.go — the roots a session may use as scratch
// space, and the home directory a scratch grant must never reach.
package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTempRootsAreAbsoluteResolvedAndUnique pins the shape every caller relies
// on: absolute entries in resolved form, with no duplicates and no blank.
func TestTempRootsAreAbsoluteResolvedAndUnique(t *testing.T) {
	roots := TempRoots()
	if len(roots) == 0 {
		t.Fatal("TempRoots returned no root")
	}
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			t.Fatal("TempRoots returned an empty root")
		}
		if !filepath.IsAbs(root) {
			t.Fatalf("TempRoots returned a relative root: %q", root)
		}
		if seen[root] {
			t.Fatalf("TempRoots returned %q twice", root)
		}
		seen[root] = true
		if runtime.GOOS == "windows" {
			continue
		}
		// A grant is compared against resolved targets, so an entry that is
		// itself still a link would never match the directory it names.
		if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
			t.Fatalf("TempRoots returned an unresolved root: %q (resolves to %q)", root, resolved)
		}
	}
}

// TestTempRootsIsTheSessionsOwnTemporaryDirectory pins that the grant is the
// session's temporary directory and not every well-known temporary location:
// a host whose TMPDIR points elsewhere must not hand out its shared /tmp, which
// belongs to every other process on the machine.
func TestTempRootsIsTheSessionsOwnTemporaryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR is not how Windows names the temporary directory")
	}
	own := filepath.Join(t.TempDir(), "session-temp")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("TMPDIR", own)

	resolvedOwn, err := filepath.EvalSymlinks(own)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", own, err)
	}
	roots := TempRoots()
	if len(roots) == 0 || roots[0] != resolvedOwn {
		t.Fatalf("TempRoots() = %v, want the session's own temporary directory %q", roots, resolvedOwn)
	}
	for _, root := range roots {
		if root == "/tmp" || root == "/private/tmp" {
			t.Fatalf("TempRoots granted the shared temporary directory %q", root)
		}
	}
}

// TestHomeDirReportsWhetherItIsKnown pins the shape of HomeDir: a caller that
// uses it as a carve-out needs "unknown" to be distinguishable from "none".
func TestHomeDirReportsWhetherItIsKnown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got, ok := HomeDir()
	if !ok {
		t.Fatal("HomeDir reported no home directory with HOME set")
	}
	if got != home {
		t.Fatalf("HomeDir() = %q, want %q", got, home)
	}
}
