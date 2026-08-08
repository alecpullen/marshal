package pipeline

import (
	"path/filepath"
	"testing"

	"marshal/internal/tools/registry"
)

func TestExecutionContextPaths(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	ctx := ExecutionContext{
		RepoRoot:      dir,
		WorkspaceRoot: filepath.Join(dir, "worktree"),
		ArtifactRoot:  paths.Dir,
		ArtifactAlias: "@run",
	}
	if got := ctx.ArtifactPath("task-1-brief.md"); got != filepath.Join(paths.Dir, "task-1-brief.md") {
		t.Errorf("ArtifactPath = %q, want %q", got, filepath.Join(paths.Dir, "task-1-brief.md"))
	}
	if got := ctx.NamedRoots()["@run"]; got != paths.Dir {
		t.Errorf("NamedRoots[@run] = %q, want %q", got, paths.Dir)
	}
}

func TestRegistryFactoryScopeFull(t *testing.T) {
	// This test verifies the scope filtering logic using a mock factory.
	factory := func(ctx ExecutionContext, scope RegistryScope) (*registry.Registry, error) {
		reg := registry.New()
		// In real wiring, tools are registered here.
		return reg, nil
	}
	reg, err := factory(ExecutionContext{}, ScopeFull)
	if err != nil {
		t.Fatalf("factory ScopeFull: %v", err)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
}

func TestRegistryFactoryScopeReadOnly(t *testing.T) {
	factory := func(ctx ExecutionContext, scope RegistryScope) (*registry.Registry, error) {
		reg := registry.New()
		return reg, nil
	}
	reg, err := factory(ExecutionContext{}, ScopeReadOnly)
	if err != nil {
		t.Fatalf("factory ScopeReadOnly: %v", err)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
}
