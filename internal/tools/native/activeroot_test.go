package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestActiveRootFollowsSessionWorkspace(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	if got := tools.activeRoot(); got != root {
		t.Fatalf("activeRoot() = %q, want construction root %q", got, root)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	// The fixture models a worktree that exists: activeRoot() stats it.
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if got := tools.activeRoot(); got != wt {
		t.Fatalf("activeRoot() = %q, want worktree %q", got, wt)
	}
}

func TestActiveRootFallsBackWithoutSession(t *testing.T) {
	root := t.TempDir()
	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	if got := tools.activeRoot(); got != root {
		t.Fatalf("activeRoot() = %q, want fallback root %q", got, root)
	}
}

func TestShellRunUsesActiveRoot(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state, JobManager: newTestCommandService(t, runner, root, state, nil), CommandAdmission: allowAllAdmission(t)}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	// The fixture models a worktree that exists: activeRoot() stats it.
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"pwd"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	if len(runner.requests) == 0 {
		t.Fatal("no command recorded")
	}
	if got := runner.requests[len(runner.requests)-1].Dir; got != wt {
		t.Fatalf("shell dir = %q, want worktree %q", got, wt)
	}
}

func TestActiveRootReturnsToTheProjectRootWhenTheWorktreeIsGone(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	tools := newToolSetForTest(t, Options{WorkspaceRoot: root, SessionState: state})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if got := tools.activeRoot(); got != root {
		t.Fatalf("activeRoot() = %q, want the project root %q", got, root)
	}
	if got := state.Workspace().ActiveRoot; got != root {
		t.Fatalf("session ActiveRoot = %q, want %q", got, root)
	}
}

// TestAPipelineToolsetDoesNotWriteOutsideItsDispatchWorktree is the regression
// the dispatch binding exists to prevent: a pipeline child's project root is
// the directory the run was launched from, not its dispatch worktree, so
// recovering a vanished dispatch worktree would point its file tools at the
// user's checkout while its commands keep failing in the bound root.
func TestAPipelineToolsetDoesNotWriteOutsideItsDispatchWorktree(t *testing.T) {
	launch := t.TempDir()
	dispatch := filepath.Join(launch, ".marshal", "worktrees", "task-1")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatalf("mkdir dispatch worktree: %v", err)
	}
	// The shape pipeline.Dispatcher.runExec builds: a child session whose
	// project root is the launch directory, bound to the dispatch worktree.
	state := session.New(config.Default(), launch, time.Unix(100, 0), session.Persistence{})
	state.BindIsolatedWorkspace(dispatch)
	if err := os.RemoveAll(dispatch); err != nil {
		t.Fatalf("remove dispatch worktree: %v", err)
	}
	tools := newToolSetForTest(t, Options{WorkspaceRoot: dispatch, CommandDir: dispatch, SessionState: state})

	if got := tools.activeRoot(); got != dispatch {
		t.Fatalf("activeRoot() = %q, want the (gone) dispatch worktree %q", got, dispatch)
	}
	if got := state.Workspace().ActiveRoot; got != dispatch {
		t.Fatalf("session ActiveRoot = %q, want unchanged %q", got, dispatch)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: dispatch, CommandDir: dispatch, SessionState: state, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	// The call may fail — that is the intended loud failure. What it must not
	// do is write into the launch directory.
	if _, err := invokeTool(t, reg, "file.write", `{"path":"leaked.go","content":"package leaked\n"}`); err == nil {
		t.Fatal("file.write outside the dispatch worktree succeeded; want a loud failure")
	}
	leaked := filepath.Join(launch, "leaked.go")
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Fatalf("file.write created %q in the launch directory (stat error: %v)", leaked, err)
	}
}

// TestShellRunRecoversAfterItsWorktreeIsRemoved is the incident end to end: a
// worktree removed from outside the session, then a shell command. The test
// injects fakeRunner, which records the request and never chdirs, so before the
// fix the call itself still succeeded and this test failed on the recorded Dir
// (the incident's real "working directory ... does not exist" is exercised by
// internal/sandbox/restricted_test.go).
func TestShellRunRecoversAfterItsWorktreeIsRemoved(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state, JobManager: newTestCommandService(t, runner, root, state, nil), CommandAdmission: allowAllAdmission(t)}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"pwd"}`); err != nil {
		t.Fatalf("shell.run after the worktree was removed: %v", err)
	}
	if len(runner.requests) == 0 {
		t.Fatal("no command recorded")
	}
	if got := runner.requests[len(runner.requests)-1].Dir; got != root {
		t.Fatalf("shell dir = %q, want the project root %q", got, root)
	}
}

// TestActiveRootRecoveryReachesTheSharedWorkspaceState pins the wiring: a
// subagent's toolset carries the parent as WorkspaceState (app.go:2413), so the
// recovery must move the parent and notice the parent's conversation — a notice
// in the child's transcript would be read by nobody.
func TestActiveRootRecoveryReachesTheSharedWorkspaceState(t *testing.T) {
	root := t.TempDir()
	parent := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	child := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	childTools := newToolSetForTest(t, Options{WorkspaceRoot: root, SessionState: child, WorkspaceState: parent})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	parent.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if got := childTools.activeRoot(); got != root {
		t.Fatalf("child activeRoot() = %q, want %q", got, root)
	}
	if got := parent.Workspace().ActiveRoot; got != root {
		t.Fatalf("parent ActiveRoot = %q, want %q", got, root)
	}
	parentNotices := 0
	for _, m := range parent.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "no longer exists") {
			parentNotices++
		}
	}
	if parentNotices != 1 {
		t.Fatalf("parent notices = %d, want 1", parentNotices)
	}
	for _, m := range child.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "no longer exists") {
			t.Fatal("the notice landed in the child's transcript")
		}
	}
}

// dirRecordingRunner records each request's Dir on a channel so the test
// can synchronize with the job goroutine without a data race.
type dirRecordingRunner struct{ dirs chan string }

func (r *dirRecordingRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	r.dirs <- req.Dir
	return CommandResult{}, nil
}

func TestJobManagerStartResolvesDirFunc(t *testing.T) {
	runner := &dirRecordingRunner{dirs: make(chan string, 1)}
	m := NewJobManager(context.Background(), runner, "/orig", 1, time.Hour, 1000)
	dir := "/orig"
	m.SetDirFunc(func() string { return dir })
	dir = "/worktree"
	if _, err := launchBackground(context.Background(), m, "true"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case got := <-runner.dirs:
		if got != "/worktree" {
			t.Fatalf("job dir = %q, want /worktree", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job never ran")
	}
}
