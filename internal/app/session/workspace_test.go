package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/pubsub"
	"marshal/internal/worktree"
)

func TestWorkspaceDefaultsToProjectRoot(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	ws := s.Workspace()
	if ws.ProjectRoot != root || ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("Workspace() = %+v, want project root %q with empty branch", ws, root)
	}
}

func TestSetWorkspaceRebindsAndPublishes(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	broker := pubsub.NewBroker[WorkspaceEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetWorkspaceBroker(broker)

	wt := Workspace{ProjectRoot: root, ActiveRoot: root + "/.marshal/worktrees/feat-x", Branch: "feat/x"}
	s.SetWorkspace(wt)
	if got := s.Workspace(); got != wt {
		t.Fatalf("Workspace() = %+v, want %+v", got, wt)
	}
	// The §1 trap: rebinding must never move WorkingDir (the project root).
	if s.WorkingDir != root {
		t.Fatalf("WorkingDir = %q, want unchanged %q", s.WorkingDir, root)
	}
	select {
	case ev := <-ch:
		if ev.Payload.Workspace != wt {
			t.Fatalf("event payload = %+v, want %+v", ev.Payload.Workspace, wt)
		}
	case <-time.After(time.Second):
		t.Fatal("no WorkspaceEvent published")
	}

	// Re-setting the same value must not republish.
	s.SetWorkspace(wt)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected republish: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSetWorkspaceReturnToRootClearsBranch(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: root + "/wt", Branch: "feat/x"})
	s.SetWorkspace(Workspace{ActiveRoot: root})
	ws := s.Workspace()
	if ws.ProjectRoot != root || ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("Workspace() = %+v, want return to %q with cleared branch", ws, root)
	}
}

// TestProjectStorageStaysAtProjectRoot is the spec §8 "§1 trap" test: after
// rebinding, everything that hangs off .marshal/ must still resolve under
// the project root. Getting this wrong relocates the session's own storage
// into the worktree it just created.
func TestProjectStorageStaysAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: root + "/.marshal/worktrees/feat-x", Branch: "feat/x"})
	if got := db.Path(s.WorkingDir); !strings.HasPrefix(got, root) {
		t.Fatalf("db.Path = %q, escapes project root %q", got, root)
	}
	if got := config.ProjectConfigPath(s.WorkingDir); !strings.HasPrefix(got, root) {
		t.Fatalf("ProjectConfigPath = %q, escapes project root %q", got, root)
	}
	if dir, err := worktree.AgentDir(s.WorkingDir); err != nil || !strings.HasPrefix(dir, root) {
		t.Fatalf("AgentDir = %q, %v, escapes project root %q", dir, err, root)
	}
}
