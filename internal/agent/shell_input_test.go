package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestShellInputDoesNotRecoverADispatchIsolatedRoot pins the dispatcher case:
// admission runs before the tool, so a recovery here would move a child out of
// the worktree its dispatch bound before any tool could fail in it.
func TestShellInputDoesNotRecoverADispatchIsolatedRoot(t *testing.T) {
	launch := t.TempDir()
	state := session.New(config.Default(), launch, time.Unix(100, 0), session.Persistence{})
	dispatch := filepath.Join(launch, ".marshal", "worktrees", "task-1")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatalf("mkdir dispatch worktree: %v", err)
	}
	state.BindIsolatedWorkspace(dispatch)
	if err := os.RemoveAll(dispatch); err != nil {
		t.Fatalf("remove dispatch worktree: %v", err)
	}

	r := testRunnerFromState(state, 1)
	in, ok := r.shellInputFor("shell.run", map[string]interface{}{"command": "pwd"})
	if !ok {
		t.Fatal("shellInputFor reported not a shell tool")
	}
	if in.WorkspaceRoot != dispatch {
		t.Fatalf("WorkspaceRoot = %q, want the bound dispatch root %q", in.WorkspaceRoot, dispatch)
	}
	if got := state.Workspace().ActiveRoot; got != dispatch {
		t.Fatalf("session ActiveRoot = %q, want unchanged %q", got, dispatch)
	}
}

// TestShellInputRecoversARemovedWorktree pins that admission is judged against
// the directory the command will run in: a stale workspace root would classify
// the very call that recovers the session.
func TestShellInputRecoversARemovedWorktree(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	r := testRunnerFromState(state, 1)
	in, ok := r.shellInputFor("shell.run", map[string]interface{}{"command": "pwd"})
	if !ok {
		t.Fatal("shellInputFor reported not a shell tool")
	}
	if in.WorkspaceRoot != root {
		t.Fatalf("WorkspaceRoot = %q, want the project root %q", in.WorkspaceRoot, root)
	}
	if got := state.Workspace().ActiveRoot; got != root {
		t.Fatalf("session ActiveRoot = %q, want %q", got, root)
	}
}
