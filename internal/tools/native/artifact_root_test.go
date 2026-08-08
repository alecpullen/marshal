// internal/tools/native/artifact_root_test.go
package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
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

// The @run alias only works if the model knows it exists. Without this,
// file.read's schema says paths are "relative to the workspace", a worker
// handed "@run/task-1-brief.md" concludes the prefix is a placeholder, and
// it goes hunting the filesystem for a directory named "run".
func TestNamedRootAliasIsDocumentedInToolSchemas(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		NamedRoots:    map[string]string{"@run": t.TempDir()},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for _, name := range []string{"file.read", "file.page", "file.write_patch", "repo.search"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !strings.Contains(string(tool.Schema), `@run/`) {
			t.Errorf("%s schema does not mention the @run alias:\n%s", name, tool.Schema)
		}
		// The schema must still be valid JSON after interpolation.
		var v any
		if err := json.Unmarshal(tool.Schema, &v); err != nil {
			t.Errorf("%s schema is not valid JSON: %v", name, err)
		}
	}
}

// With no named roots configured, path descriptions stay clean.
func TestNoNamedRootsLeavesSchemasUnchanged(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("file.read")
	if strings.Contains(string(tool.Schema), "@") {
		t.Errorf("file.read schema mentions an alias with none configured:\n%s", tool.Schema)
	}
}
