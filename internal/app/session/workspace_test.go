package session

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x", TargetBranch: "main", BaseSha: "abc123"})
	row, err := d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != wt || row.WorktreeBranch != "feat/x" || row.WorktreeTargetBranch != "main" || row.WorktreeBaseSha != "abc123" {
		t.Fatalf("persisted = %q/%q/%q/%q, want %q/feat/x/main/abc123", row.ActiveRoot, row.WorktreeBranch, row.WorktreeTargetBranch, row.WorktreeBaseSha, wt)
	}
	// Returning to the project root stores empty values, not the root path.
	s.SetWorkspace(Workspace{ActiveRoot: root})
	row, err = d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" || row.WorktreeTargetBranch != "" || row.WorktreeBaseSha != "" {
		t.Fatalf("persisted after return = %q/%q/%q/%q, want empty", row.ActiveRoot, row.WorktreeBranch, row.WorktreeTargetBranch, row.WorktreeBaseSha)
	}
}

func TestRestoreTitlePersistsManualFlag(t *testing.T) {
	t.Run("manual", func(t *testing.T) {
		s, d, sid, root := newPersistedState(t)
		s.SetTitleManual("my name")
		if err := d.UpdateSessionTitle(sid, "my name", true); err != nil {
			t.Fatalf("UpdateSessionTitle: %v", err)
		}

		// A second State over the same row simulates resume.
		resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
		if got := resumed.Title(); got != "my name" {
			t.Fatalf("resumed Title() = %q, want %q", got, "my name")
		}
		if !resumed.TitleManuallySet() {
			t.Fatal("resumed TitleManuallySet() = false, want true")
		}
	})

	t.Run("auto", func(t *testing.T) {
		s, d, sid, root := newPersistedState(t)
		s.SetTitle("auto")
		if err := d.UpdateSessionTitle(sid, "auto", false); err != nil {
			t.Fatalf("UpdateSessionTitle: %v", err)
		}

		resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
		if got := resumed.Title(); got != "auto" {
			t.Fatalf("resumed Title() = %q, want %q", got, "auto")
		}
		if resumed.TitleManuallySet() {
			t.Fatal("resumed TitleManuallySet() = true, want false")
		}
	})
}

func TestResumeRestoresWorkspace(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x", TargetBranch: "main", BaseSha: "abc123"})

	// A second State over the same row simulates resume.
	resumed := New(config.Default(), root, time.Unix(100, 0), Persistence{DB: d, SessionID: sid, Logger: slog.Default()})
	ws := resumed.Workspace()
	if ws.ActiveRoot != wt || ws.Branch != "feat/x" || ws.ProjectRoot != root || ws.TargetBranch != "main" || ws.BaseSha != "abc123" {
		t.Fatalf("resumed Workspace() = %+v, want worktree %q on feat/x targeting main from abc123", ws, wt)
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

func TestEnsureWorkspaceRootLeavesALiveWorktreeAlone(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	before := len(s.Messages())

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != wt || ws.Branch != "feat/x" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the worktree unchanged", ws)
	}
	if got := len(s.Messages()); got != before {
		t.Fatalf("messages = %d, want %d (no notice for a live worktree)", got, before)
	}
}

func TestEnsureWorkspaceRootRecoversFromARemovedWorktree(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x", TargetBranch: "main", BaseSha: "abc123"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != root || ws.Branch != "" || ws.BaseSha != "" || ws.TargetBranch != "" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the project root with an empty branch", ws)
	}
	msgs := s.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != RoleSystem || !strings.Contains(last.Content, wt) {
		t.Fatalf("last message = %v %q, want a system notice naming %q", last.Role, last.Content, wt)
	}
	// The cleared row is what stops the next resume warning a second time.
	row, err := d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" {
		t.Fatalf("row still records ActiveRoot %q / branch %q", row.ActiveRoot, row.WorktreeBranch)
	}
}

func TestEnsureWorkspaceRootWritesOneNoticeForConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ws := s.EnsureWorkspaceRoot(); ws.ActiveRoot != root {
				t.Errorf("EnsureWorkspaceRoot() = %+v, want %q", ws, root)
			}
		}()
	}
	wg.Wait()

	notices := 0
	for _, m := range s.Messages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "no longer exists") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("system notices = %d, want 1", notices)
	}
}

func TestEnsureWorkspaceRootStaysPutWhenTheProjectRootIsAlsoGone(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove project: %v", err)
	}

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != wt {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the root left alone with nowhere to land", ws)
	}
	for _, m := range s.Messages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "no longer exists") {
			t.Fatal("a notice was written although the session could not move")
		}
	}
}

// TestEnsureWorkspaceRootLeavesADispatchIsolatedRootAlone pins the dispatch
// case: a pipeline child is bound to the dispatcher's worktree while its
// project root is the directory the run was launched from, so recovering the
// child to that project root would write outside the isolation it was given.
func TestEnsureWorkspaceRootLeavesADispatchIsolatedRootAlone(t *testing.T) {
	launch := t.TempDir()
	s := New(config.Default(), launch, time.Unix(100, 0), Persistence{})
	dispatch := filepath.Join(launch, ".marshal", "worktrees", "task-1")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatalf("mkdir dispatch worktree: %v", err)
	}
	s.BindIsolatedWorkspace(dispatch)
	if err := os.RemoveAll(dispatch); err != nil {
		t.Fatalf("remove dispatch worktree: %v", err)
	}
	before := len(s.Messages())

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != dispatch {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the bound dispatch root %q", ws, dispatch)
	}
	if got := s.Workspace().ActiveRoot; got != dispatch {
		t.Fatalf("session ActiveRoot = %q, want unchanged %q", got, dispatch)
	}
	if got := len(s.Messages()); got != before {
		t.Fatalf("messages = %d, want %d (a dispatch-isolated root earns no notice)", got, before)
	}
}

func TestASetWorkspaceClearsDispatchIsolation(t *testing.T) {
	launch := t.TempDir()
	s := New(config.Default(), launch, time.Unix(100, 0), Persistence{})
	dispatch := filepath.Join(launch, ".marshal", "worktrees", "task-1")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatalf("mkdir dispatch worktree: %v", err)
	}
	s.BindIsolatedWorkspace(dispatch)
	if err := os.RemoveAll(dispatch); err != nil {
		t.Fatalf("remove dispatch worktree: %v", err)
	}
	// An ordinary rebinding is the session's own choice again, so the same
	// session recovers from that gone root.
	s.SetWorkspace(Workspace{ActiveRoot: dispatch, Branch: "dispatch/task-1"})

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != launch || ws.Branch != "" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the project root %q with an empty branch", ws, launch)
	}
	noticed := false
	for _, m := range s.Messages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "no longer exists") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("no system notice; a session that chose its root still recovers")
	}
}

// TestEnsureWorkspaceRootRecoversWhenTheRootIsAFile covers the other arm of
// unusedWorktreeRoot: a path that still exists but is not a directory is as
// unusable as one that was removed.
func TestEnsureWorkspaceRootRecoversWhenTheRootIsAFile(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	if err := os.WriteFile(wt, []byte("not a worktree\n"), 0o644); err != nil {
		t.Fatalf("write file at worktree path: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	before := len(s.Messages())

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the project root %q with an empty branch", ws, root)
	}
	if got := len(s.Messages()); got != before+1 {
		t.Fatalf("messages = %d, want %d (one notice)", got, before+1)
	}
	last := s.Messages()[before]
	if last.Role != RoleSystem || !strings.Contains(last.Content, wt) {
		t.Fatalf("last message = %v %q, want a system notice naming %q", last.Role, last.Content, wt)
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
