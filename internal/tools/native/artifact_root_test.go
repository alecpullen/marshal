// internal/tools/native/artifact_root_test.go
package native

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNamedRunAlias(t *testing.T) {
	worktree := t.TempDir()
	artifactRoot := t.TempDir()
	namedRoots := map[string]string{"@run": artifactRoot}

	rel := "@run/task-1-brief.md"
	got, err := resolveNamedRoot(namedRoots, worktree, nil, rel)
	if err != nil {
		t.Fatalf("resolveNamedRoot: %v", err)
	}
	want := filepath.Join(artifactRoot, "task-1-brief.md")
	realRoot, err := filepath.EvalSymlinks(artifactRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	want = filepath.Join(realRoot, "task-1-brief.md")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveNamedUnknownAliasRejected(t *testing.T) {
	worktree := t.TempDir()
	namedRoots := map[string]string{"@run": t.TempDir()}

	_, err := resolveNamedRoot(namedRoots, worktree, nil, "@unknown/file.md")
	if err == nil {
		t.Fatal("unknown alias should be rejected")
	}
}

func TestResolveNamedAliasTraversalRejected(t *testing.T) {
	worktree := t.TempDir()
	namedRoots := map[string]string{"@run": t.TempDir()}

	_, err := resolveNamedRoot(namedRoots, worktree, nil, "@run/../../../etc/passwd")
	if err == nil {
		t.Fatal("traversal outside @run root should be rejected")
	}
}

func TestResolveNamedNoAliasFallsToMultiResolver(t *testing.T) {
	worktree := t.TempDir()
	namedRoots := map[string]string{"@run": t.TempDir()}

	// A plain relative path (no alias prefix) should resolve against the
	// worktree root via the existing multi-root resolver.
	got, err := resolveNamedRoot(namedRoots, worktree, nil, "internal/foo.go")
	if err != nil {
		t.Fatalf("resolveNamedRoot plain path: %v", err)
	}
	want := filepath.Join(worktree, "internal", "foo.go")
	realRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	want = filepath.Join(realRoot, "internal", "foo.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveNamedAliasWritesToArtifactRoot(t *testing.T) {
	artifactRoot := t.TempDir()
	namedRoots := map[string]string{"@run": artifactRoot}

	got, err := resolveNamedRoot(namedRoots, t.TempDir(), nil, "@run/task-1-report.md")
	if err != nil {
		t.Fatalf("resolveNamedRoot: %v", err)
	}
	// Verify the path is inside artifactRoot and the file can be written.
	if err := os.WriteFile(got, []byte("test"), 0o644); err != nil {
		t.Fatalf("write to resolved path: %v", err)
	}
}
