package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// preparingRunner is a command runner that also reports the environment it
// would launch with, which is what a preparation requires of a production
// backend. Its captured values are deliberate fixtures, never the ambient
// environment.
type preparingRunner struct {
	result CommandResult
	env    []string
	known  map[string]string

	mu       sync.Mutex
	requests []CommandRequest
}

func (r *preparingRunner) Run(_ context.Context, req CommandRequest) (CommandResult, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	return r.result, nil
}

func (r *preparingRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	env := []string{"PATH=/captured/bin", "PWD=" + dir}
	known := map[string]string{"PATH": "/captured/bin", "PWD": dir}
	if r.env != nil {
		env = append([]string(nil), r.env...)
	}
	if r.known != nil {
		known = make(map[string]string, len(r.known))
		for key, value := range r.known {
			known[key] = value
		}
	}
	return env, known, nil
}

func (r *preparingRunner) lastRequest(t *testing.T) CommandRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("runner was never called")
	}
	return r.requests[len(r.requests)-1]
}

// TestPrepareCommandCapturesActiveRoot pins that preparation captures the
// directory the command will really run in — the session's active root, a
// bound dispatch's own worktree, or the project root recovered to after an
// interactive worktree disappears — and that the same binding is the workspace
// the command belongs to.
func TestPrepareCommandCapturesActiveRoot(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".marshal", "worktrees", "feat-y")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: worktree, Branch: "feat/y"})
	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &preparingRunner{}, SessionState: state})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}

	call := registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"go test ./..."}`)}
	facts, err := tools.prepareCommand(context.Background(), call)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}
	if facts.Command != "go test ./..." {
		t.Fatalf("Command = %q", facts.Command)
	}
	if facts.Dir != worktree || facts.WorkspaceRoot != worktree {
		t.Fatalf("Dir = %q, WorkspaceRoot = %q, want the active worktree %q", facts.Dir, facts.WorkspaceRoot, worktree)
	}
	// A local backend runs the command in Dir, so the analyzer's PWD fact is
	// that directory — not whichever directory this process was started from.
	if facts.Env["PWD"] != worktree {
		t.Fatalf("PWD fact = %q, want the captured cwd %q", facts.Env["PWD"], worktree)
	}

	// A bound dispatch: the command root is the dispatch's own worktree, and
	// that is the workspace the command belongs to.
	dispatch := filepath.Join(root, "dispatch")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatalf("mkdir dispatch: %v", err)
	}
	dispatchTools, err := newToolSet(Options{WorkspaceRoot: dispatch, CommandDir: dispatch, CommandRunner: &preparingRunner{}})
	if err != nil {
		t.Fatalf("newToolSet(dispatch): %v", err)
	}
	bound, err := dispatchTools.prepareCommand(context.Background(), call)
	if err != nil {
		t.Fatalf("prepareCommand(dispatch): %v", err)
	}
	if bound.Dir != dispatch || bound.WorkspaceRoot != dispatch {
		t.Fatalf("Dir = %q, WorkspaceRoot = %q, want the bound dispatch %q", bound.Dir, bound.WorkspaceRoot, dispatch)
	}

	// Removing the interactive worktree underneath the session recovers to the
	// project root; preparation must capture the recovered root, never the dead
	// path.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	recovered, err := tools.prepareCommand(context.Background(), call)
	if err != nil {
		t.Fatalf("prepareCommand(recovered): %v", err)
	}
	if recovered.Dir != root || recovered.WorkspaceRoot != root {
		t.Fatalf("Dir = %q, WorkspaceRoot = %q, want the recovered project root %q", recovered.Dir, recovered.WorkspaceRoot, root)
	}
}

// TestPrepareConfiguredTestCommand pins that an absent override prepares the
// owner's current configured test command (not the value captured at
// construction), while a caller override is honoured by an ordinary test.run.
func TestPrepareConfiguredTestCommand(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Commands.Test = "owner-current-test"
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	tools, err := newToolSet(Options{
		WorkspaceRoot: root,
		CommandRunner: &preparingRunner{},
		SessionState:  state,
		TestCommand:   "construction-time-test",
	})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}

	facts, err := tools.prepareCommand(context.Background(), registry.ToolCall{Name: "test.run", Args: []byte(`{}`)})
	if err != nil {
		t.Fatalf("prepareCommand(no override): %v", err)
	}
	if facts.Command != "owner-current-test" {
		t.Fatalf("Command = %q, want the owner's configured command", facts.Command)
	}

	facts, err = tools.prepareCommand(context.Background(), registry.ToolCall{Name: "test.run", Args: []byte(`{"command":"caller-test"}`)})
	if err != nil {
		t.Fatalf("prepareCommand(override): %v", err)
	}
	if facts.Command != "caller-test" {
		t.Fatalf("Command = %q, want the caller override", facts.Command)
	}

	// A toolset with no session only has its construction value to offer.
	standalone, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &preparingRunner{}, TestCommand: "construction-time-test"})
	if err != nil {
		t.Fatalf("newToolSet(standalone): %v", err)
	}
	facts, err = standalone.prepareCommand(context.Background(), registry.ToolCall{Name: "test.run", Args: []byte(`{}`)})
	if err != nil {
		t.Fatalf("prepareCommand(standalone): %v", err)
	}
	if facts.Command != "construction-time-test" {
		t.Fatalf("Command = %q, want the construction value", facts.Command)
	}
}

// TestPrepareCommandUnsupportedRunnerFailsClosed pins that a runner which
// cannot say what environment it would launch with is an explicit preparation
// failure, never a command approved against a guessed environment.
func TestPrepareCommandUnsupportedRunnerFailsClosed(t *testing.T) {
	root := t.TempDir()
	// fakeRunner implements CommandRunner only.
	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	_, err = tools.prepareCommand(context.Background(), registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"go test ./..."}`)})
	if err == nil {
		t.Fatal("preparation succeeded for a runner with no environment capture")
	}
	if !strings.Contains(err.Error(), "cannot capture its launch environment") {
		t.Fatalf("error = %v, want an explicit unsupported-capture error", err)
	}
}

// TestTesterPreparationIgnoresOverride pins that the tester view strips a
// supplied command override from preparation exactly as it strips it from
// execution: the evaluated bytes and the launched bytes are the same
// configured command.
func TestTesterPreparationIgnoresOverride(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Commands.Test = "configured-test"
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	runner := &preparingRunner{result: CommandResult{ExitCode: 0, Meta: registry.SandboxMeta{Backend: "prepared", Enabled: true}}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:    root,
		CommandRunner:    runner,
		SessionState:     state,
		JobManager:       newTestCommandService(t, runner, root, state, nil),
		CommandAdmission: allowAllAdmission(t),
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := registry.TesterView(reg).Lookup("test.run")
	if !ok {
		t.Fatal("test.run missing from the tester view")
	}
	if tool.PrepareCommand == nil {
		t.Fatal("tester tool lost PrepareCommand")
	}
	call := registry.ToolCall{Name: "test.run", Args: []byte(`{"command":"override"}`)}
	facts, err := tool.PrepareCommand(context.Background(), call)
	if err != nil {
		t.Fatalf("PrepareCommand: %v", err)
	}
	if facts.Command != "configured-test" {
		t.Fatalf("prepared %q, want the configured command", facts.Command)
	}

	if _, err := tool.Handler(context.Background(), call); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := runner.lastRequest(t).Command; got != "configured-test" {
		t.Fatalf("handler launched %q, want the configured command", got)
	}
	if got := runner.lastRequest(t).Command; got != facts.Command {
		t.Fatalf("handler launched %q but preparation analyzed %q", got, facts.Command)
	}
}
