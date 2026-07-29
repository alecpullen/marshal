package native

import (
	"context"
	"path/filepath"
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
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
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
	if _, err := m.Start(context.Background(), "true", time.Second); err != nil {
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
