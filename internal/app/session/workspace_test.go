package session

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
	// Safe without s.mu: WorkingDir is set in New and never mutated
	// (spec §1: it stays the project root for the session's lifetime).
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

func newPersistedState(t *testing.T) (*State, *db.DB, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := db.Open(db.Path(root))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject(root, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sid := "sess-workspace-test"
	if err := d.CreateSession(sid, pid, "test", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
	return s, d, sid, root
}

func TestSetWorkspacePersists(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	row, err := d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != wt || row.WorktreeBranch != "feat/x" {
		t.Fatalf("persisted = %q/%q, want %q/feat/x", row.ActiveRoot, row.WorktreeBranch, wt)
	}
	// Returning to the project root stores empty values, not the root path.
	s.SetWorkspace(Workspace{ActiveRoot: root})
	row, err = d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" {
		t.Fatalf("persisted after return = %q/%q, want empty", row.ActiveRoot, row.WorktreeBranch)
	}
}

func TestResumeRestoresWorkspace(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})

	// A second State over the same row simulates resume.
	resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
	ws := resumed.Workspace()
	if ws.ActiveRoot != wt || ws.Branch != "feat/x" || ws.ProjectRoot != root {
		t.Fatalf("resumed Workspace() = %+v, want worktree %q on feat/x", ws, wt)
	}
}

func TestResumeFallsBackWithNoticeWhenWorktreeGone(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	gone := filepath.Join(root, ".marshal", "worktrees", "deleted")
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: gone, Branch: "deleted"})

	resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
	ws := resumed.Workspace()
	if ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("resumed Workspace() = %+v, want fallback to project root", ws)
	}
	msgs := resumed.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages; want a system notice")
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleSystem || !strings.Contains(last.Content, "no longer exists") {
		t.Fatalf("last message = %v %q, want system notice about missing worktree", last.Role, last.Content)
	}
	// The stale record is cleared so the next resume does not warn again.
	row, err := d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" {
		t.Fatalf("stale ActiveRoot %q was not cleared", row.ActiveRoot)
	}
}

func TestResumeRestoresTodos(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	todos := []db.TodoItem{
		{Content: "step one", Status: "completed"},
		{Content: "step two", Status: "in_progress"},
	}
	if err := s.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}

	resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
	got := resumed.Todos()
	if len(got) != len(todos) {
		t.Fatalf("resumed todos = %d, want %d", len(got), len(todos))
	}
	for i, want := range todos {
		if got[i].Content != want.Content || got[i].Status != want.Status {
			t.Fatalf("todo[%d] = %+v, want %+v", i, got[i], want)
		}
	}
}
