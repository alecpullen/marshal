package native

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// resolvedEqual compares two paths, treating them as equal if they agree
// after symlink resolution (the multi-root resolver returns symlink-resolved
// paths; test fixtures use the un-resolved form).
func resolvedEqual(a, b string) bool {
	ra, errRA := filepath.EvalSymlinks(a)
	rb, errRB := filepath.EvalSymlinks(b)
	if errRA != nil || errRB != nil {
		return a == b
	}
	return ra == rb
}

func TestResolveWorkspacePathMulti(t *testing.T) {
	// Create two temp dirs: primary root and an additional root.
	root := t.TempDir()
	extraRoot := t.TempDir()

	// Create a file in the additional root at a path that, when accessed
	// from the primary root, uses ../ to escape — the multi-root resolver
	// should still find it under the additional root.
	extraFileRel := filepath.Join("..", filepath.Base(extraRoot), "extra_file.txt")
	extraFilePath := filepath.Join(extraRoot, "extra_file.txt")
	if err := os.WriteFile(extraFilePath, []byte("extra"), 0644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	// Create a file in the primary root.
	primaryFile := "primary_file.txt"
	primaryPath := filepath.Join(root, primaryFile)
	if err := os.WriteFile(primaryPath, []byte("primary"), 0644); err != nil {
		t.Fatalf("write primary file: %v", err)
	}

	additionalRoots := []string{extraRoot}

	// Test 1: path that escapes primary root but is valid under additional root.
	got, err := resolveWorkspacePathMulti(root, additionalRoots, extraFileRel)
	if err != nil {
		t.Fatalf("additional root path returned error: %v", err)
	}
	if !resolvedEqual(got, extraFilePath) {
		t.Fatalf("resolveWorkspacePathMulti(root, addl, %q) = %q, want %q", extraFileRel, got, extraFilePath)
	}

	// Test 2: path in the primary root succeeds.
	got, err = resolveWorkspacePathMulti(root, additionalRoots, primaryFile)
	if err != nil {
		t.Fatalf("primary root path returned error: %v", err)
	}
	if !resolvedEqual(got, primaryPath) {
		t.Fatalf("resolveWorkspacePathMulti(root, addl, %q) = %q, want %q", primaryFile, got, primaryPath)
	}

	// Test 3: path that escapes both roots is rejected.
	_, err = resolveWorkspacePathMulti(root, additionalRoots, "../nonexistent_dir/file.txt")
	if err == nil {
		t.Fatal("resolveWorkspacePathMulti traversal returned nil error")
	}

	// Test 4: absolute path is rejected.
	_, err = resolveWorkspacePathMulti(root, additionalRoots, "/etc/passwd")
	if err == nil {
		t.Fatal("resolveWorkspacePathMulti absolute path returned nil error")
	}

	// Test 5: bare "." returns the (symlink-resolved) primary root.
	got, err = resolveWorkspacePathMulti(root, additionalRoots, ".")
	if err != nil {
		t.Fatalf("resolveWorkspacePathMulti '.' returned error: %v", err)
	}
	if !resolvedEqual(got, root) {
		t.Fatalf("resolveWorkspacePathMulti(root, addl, '.') = %q, want %q", got, root)
	}

	// Test 6: nil additional roots behaves like single-root resolve.
	var nilRoots []string
	got, err = resolveWorkspacePathMulti(root, nilRoots, primaryFile)
	if err != nil {
		t.Fatalf("primary root path with nil additional roots returned error: %v", err)
	}
	if !resolvedEqual(got, primaryPath) {
		t.Fatalf("with nil additional roots: got %q, want %q", got, primaryPath)
	}

	// Test 7: nil additional roots rejects escaping paths.
	_, err = resolveWorkspacePathMulti(root, nilRoots, "../outside")
	if err == nil {
		t.Fatal("nil additional roots should reject escaping paths")
	}

	// Test 8: subdirectory within additional root via traversal.
	subDirName := "sub_extra"
	subDirPath := filepath.Join(extraRoot, subDirName)
	if err := os.MkdirAll(subDirPath, 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	subFilePath := filepath.Join(subDirPath, "nested.txt")
	if err := os.WriteFile(subFilePath, []byte("nested"), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	extraBase := filepath.Base(extraRoot)
	subFileRel := filepath.Join("..", extraBase, subDirName, "nested.txt")
	got, err = resolveWorkspacePathMulti(root, additionalRoots, subFileRel)
	if err != nil {
		t.Fatalf("nested path in additional root returned error: %v", err)
	}
	if !resolvedEqual(got, subFilePath) {
		t.Fatalf("nested path: got %q, want %q", got, subFilePath)
	}

	// Test 9: existing resolveWorkspacePath still works (regression).
	got, err = resolveWorkspacePath(root, primaryFile)
	if err != nil {
		t.Fatalf("resolveWorkspacePath regression failed: %v", err)
	}
	if !resolvedEqual(got, primaryPath) {
		t.Fatalf("resolveWorkspacePath regression: got %q, want %q", got, primaryPath)
	}
	if _, err := resolveWorkspacePath(root, "../outside"); err == nil {
		t.Fatal("resolveWorkspacePath traversal regression returned nil error")
	}
}

func TestResolveWorkspacePathMultiRead(t *testing.T) {
	root := t.TempDir()
	extraRoot := t.TempDir()

	// Create a file in the additional root.
	extraFilePath := filepath.Join(extraRoot, "extra_file.txt")
	if err := os.WriteFile(extraFilePath, []byte("extra"), 0644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	// Create a file in the primary root.
	primaryPath := filepath.Join(root, "primary_file.txt")
	if err := os.WriteFile(primaryPath, []byte("primary"), 0644); err != nil {
		t.Fatalf("write primary file: %v", err)
	}

	additionalRoots := []string{extraRoot}

	// Absolute path of a file in the primary root resolves.
	got, err := resolveWorkspacePathMultiRead(root, nil, primaryPath)
	if err != nil {
		t.Fatalf("absolute primary path returned error: %v", err)
	}
	if !resolvedEqual(got, primaryPath) {
		t.Fatalf("absolute primary path = %q, want %q", got, primaryPath)
	}

	// Absolute path of a file in an additional root resolves.
	got, err = resolveWorkspacePathMultiRead(root, additionalRoots, extraFilePath)
	if err != nil {
		t.Fatalf("absolute additional-root path returned error: %v", err)
	}
	if !resolvedEqual(got, extraFilePath) {
		t.Fatalf("absolute additional-root path = %q, want %q", got, extraFilePath)
	}

	// /etc/passwd is rejected with ErrPathEscapes and a message naming the
	// allowed roots.
	_, err = resolveWorkspacePathMultiRead(root, nil, "/etc/passwd")
	if err == nil {
		t.Fatal("absolute path outside all roots returned nil error")
	}
	if !errors.Is(err, ErrPathEscapes) {
		t.Fatalf("got %v, want ErrPathEscapes", err)
	}
	if !strings.Contains(err.Error(), "allowed roots") || !strings.Contains(err.Error(), "outside all") {
		t.Fatalf("error should name the allowed roots, got: %v", err)
	}

	// Symlink escape: an absolute path through a symlink pointing outside
	// the root must be rejected.
	if runtime.GOOS != "windows" {
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		_, err = resolveWorkspacePathMultiRead(root, nil, filepath.Join(root, "link", "secret.txt"))
		if err == nil {
			t.Fatal("symlink-escape absolute path returned nil error")
		}
		if !errors.Is(err, ErrPathEscapes) {
			t.Fatalf("symlink escape: got %v, want ErrPathEscapes", err)
		}
	}

	// Relative behaviour unchanged: `..`-prefixed path into the additional
	// root still resolves.
	extraFileRel := filepath.Join("..", filepath.Base(extraRoot), "extra_file.txt")
	got, err = resolveWorkspacePathMultiRead(root, additionalRoots, extraFileRel)
	if err != nil {
		t.Fatalf("relative additional-root path returned error: %v", err)
	}
	if !resolvedEqual(got, extraFilePath) {
		t.Fatalf("relative additional-root path = %q, want %q", got, extraFilePath)
	}

	// Relative traversal to a nonexistent dir is still rejected.
	_, err = resolveWorkspacePathMultiRead(root, additionalRoots, "../nonexistent_dir/file.txt")
	if err == nil {
		t.Fatal("relative traversal to nonexistent dir returned nil error")
	}

	// Nonexistent leaf inside a root resolves (new-file case); reading it
	// then fails at os.Stat, not at resolution. The returned path is the
	// symlink-resolved root plus the non-existing tail, so compare against
	// the resolved root.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks root: %v", err)
	}
	wantNew := filepath.Join(resolvedRoot, "missing_dir", "new.txt")
	got, err = resolveWorkspacePathMultiRead(root, nil, filepath.Join(root, "missing_dir", "new.txt"))
	if err != nil {
		t.Fatalf("nonexistent leaf inside root returned error: %v", err)
	}
	if got != wantNew {
		t.Fatalf("nonexistent leaf = %q, want %q", got, wantNew)
	}
}

func TestResolveNamedRootRead(t *testing.T) {
	worktree := t.TempDir()
	artifactRoot := t.TempDir()
	namedRoots := map[string]string{"@run": artifactRoot}

	// Absolute sub-path under the alias root succeeds in read mode.
	absBrief := filepath.Join(artifactRoot, "task-1-brief.md")
	if err := os.WriteFile(absBrief, []byte("brief"), 0644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	got, err := resolveNamedRootRead(namedRoots, worktree, nil, absBrief)
	if err != nil {
		t.Fatalf("absolute alias-root path returned error: %v", err)
	}
	if !resolvedEqual(got, absBrief) {
		t.Fatalf("absolute alias-root path = %q, want %q", got, absBrief)
	}

	// /etc/passwd fails in read mode.
	_, err = resolveNamedRootRead(namedRoots, worktree, nil, "/etc/passwd")
	if err == nil {
		t.Fatal("absolute path outside all roots returned nil error")
	}
	if !errors.Is(err, ErrPathEscapes) {
		t.Fatalf("got %v, want ErrPathEscapes", err)
	}

	// Unknown alias still fails.
	_, err = resolveNamedRootRead(namedRoots, worktree, nil, "@unknown/f")
	if err == nil {
		t.Fatal("unknown alias returned nil error")
	}

	// Strict resolveNamedRoot still rejects an absolute path inside the
	// alias root (write semantics pinned).
	_, err = resolveNamedRoot(namedRoots, worktree, nil, absBrief)
	if err == nil {
		t.Fatal("strict resolveNamedRoot accepted an absolute path")
	}
}

func TestResolveWorkspacePathMultiRejectsTraversalToNonexistentRoot(t *testing.T) {
	root := t.TempDir()
	additionalRoots := []string{"/nonexistent/path"}

	_, err := resolveWorkspacePathMulti(root, additionalRoots, "../outside")
	if err == nil {
		t.Fatal("expected error for path that escapes all roots")
	}
}

func TestResolveWorkspacePath_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests not supported on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := resolveWorkspacePath(root, "link/secret.txt")
	if !errors.Is(err, ErrPathEscapes) {
		t.Errorf("got %v, want ErrPathEscapes", err)
	}
}
