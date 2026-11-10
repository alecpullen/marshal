package native

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// searchReg builds a registry whose workspace is root. When state is
// non-nil it is the agent doing the searching; workspace may be a different
// state (the agent.run child case, where the parent owns the workspace).
func searchReg(t *testing.T, root string, state, workspace *session.State) *registry.Registry {
	t.Helper()
	opts := Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}
	if state != nil {
		opts.SessionState = state
	}
	if workspace != nil {
		opts.WorkspaceState = workspace
	}
	reg := registry.New()
	if err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg
}

func newState(t *testing.T, root string) *session.State {
	t.Helper()
	return session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
}

// A path that names a file must search that file. The walk used to skip the
// root entry unconditionally, so every `path` pointing at a file answered
// "found 0 matches" even when the term was on the line the caller had just
// read.
func TestRepoSearchSearchesAnExplicitFilePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sub", "a.go"), "package sub\n// needle here\n")
	writeFile(t, filepath.Join(root, "sub", "b.go"), "package sub\n// no match\n")

	reg := searchReg(t, root, nil, nil)
	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","path":"sub/a.go"}`)
	if err != nil {
		t.Fatalf("repo.search: %v", err)
	}
	if !strings.Contains(result.Content, "sub/a.go:2:") {
		t.Fatalf("search of a file path returned no hit; summary=%q content=%q", result.Summary, result.Content)
	}
	if strings.Contains(result.Content, "b.go") {
		t.Fatalf("search of a file path leaked a sibling file: %q", result.Content)
	}
}

// An explicit file path is an explicit instruction: a gitignored file named
// by the caller is searched rather than silently skipped.
func TestRepoSearchSearchesExplicitlyNamedGitignoredFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "archive/\n")
	writeFile(t, filepath.Join(root, "archive", "plan.md"), "## Task 6\n")

	reg := searchReg(t, root, nil, nil)
	result, err := invokeTool(t, reg, "repo.search", `{"query":"## Task 6","path":"archive/plan.md"}`)
	if err != nil {
		t.Fatalf("repo.search: %v", err)
	}
	if !strings.Contains(result.Content, "archive/plan.md:1:") {
		t.Fatalf("explicit gitignored file was not searched; summary=%q content=%q", result.Summary, result.Content)
	}
}

// Inside a worktree the search follows the session's active root: uncommitted
// worktree content is found and the project root's copy is not.
func TestRepoSearchFollowsActiveRootIntoWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package root\n// needle in project root\n")
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	writeFile(t, filepath.Join(wt, "main.go"), "package root\n// needle in worktree\n")
	writeFile(t, filepath.Join(wt, "internal", "new.go"), "package internal\n// needle only in worktree\n")

	state := newState(t, root)
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	reg := searchReg(t, root, state, nil)

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search: %v", err)
	}
	if !strings.Contains(result.Content, "internal/new.go:2:") {
		t.Fatalf("worktree-only file not searched; summary=%q content=%q", result.Summary, result.Content)
	}
	if strings.Contains(result.Content, "in project root") {
		t.Fatalf("search escaped the active root into the project root: %q", result.Content)
	}

	result, err = invokeTool(t, reg, "repo.search", `{"query":"needle","path":"internal"}`)
	if err != nil {
		t.Fatalf("repo.search path: %v", err)
	}
	if !strings.Contains(result.Content, "internal/new.go:2:") {
		t.Fatalf("subdirectory path in a worktree found nothing; summary=%q content=%q", result.Summary, result.Content)
	}
}

// A child agent shares the parent's workspace state through WorkspaceState,
// so its searches see the same worktree the parent is working in.
func TestRepoSearchAsSubagentUsesParentWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package root\n// needle in project root\n")
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	writeFile(t, filepath.Join(wt, "child.go"), "package wt\n// needle only in worktree\n")

	parent := newState(t, root)
	parent.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	child := newState(t, root)
	reg := searchReg(t, root, child, parent)

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search: %v", err)
	}
	if !strings.Contains(result.Content, "child.go:2:") {
		t.Fatalf("child search missed the parent's worktree; summary=%q content=%q", result.Summary, result.Content)
	}
	if strings.Contains(result.Content, "in project root") {
		t.Fatalf("child search escaped to the project root: %q", result.Content)
	}
}
