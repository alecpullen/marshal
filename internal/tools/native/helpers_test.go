package native

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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

	// resolvedEqual compares two paths, treating them as equal if they
	// agree after symlink resolution (the multi-root resolver returns
	// symlink-resolved paths; test fixtures use the un-resolved form).
	resolvedEqual := func(a, b string) bool {
		ra, errRA := filepath.EvalSymlinks(a)
		rb, errRB := filepath.EvalSymlinks(b)
		if errRA != nil || errRB != nil {
			return a == b
		}
		return ra == rb
	}

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
