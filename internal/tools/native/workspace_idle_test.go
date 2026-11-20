package native_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pipeline"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// idleBlockingRunner is a CommandRunner that announces the process start and
// then holds until the test releases it. It is how these tests keep a direct
// command or a managed execution in flight without a timing sleep.
type idleBlockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newIdleBlockingRunner() *idleBlockingRunner {
	return &idleBlockingRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *idleBlockingRunner) Run(ctx context.Context, _ native.CommandRequest) (native.CommandResult, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return native.CommandResult{}, nil
	case <-ctx.Done():
		return native.CommandResult{}, ctx.Err()
	}
}

// idleRunner settles immediately, for the launches that only need to be
// admitted and to release their root use.
type idleRunner struct{}

func (idleRunner) Run(context.Context, native.CommandRequest) (native.CommandResult, error) {
	return native.CommandResult{}, nil
}

// pipelineVerifierRunner adapts the blocking runner to the pipeline's own argv
// seam, so the verifier can hold the shared root use the same way it would with
// a real command runner.
type pipelineVerifierRunner struct{ blocking *idleBlockingRunner }

func (r pipelineVerifierRunner) Run(ctx context.Context, dir string, argv []string) (string, error) {
	_, err := r.blocking.Run(ctx, native.CommandRequest{Command: strings.Join(argv, " "), Dir: dir})
	return "", err
}

// idleRepo initializes a git repository and returns its canonical repository
// identity, so a root has a cross-process participant to publish use notes to.
func idleRepo(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	repository, err := (worktree.CLIGitOps{}).RepositoryID(dir)
	if err != nil {
		t.Fatalf("repository identity: %v", err)
	}
	return repository
}

// invokeNative calls one registered tool handler directly, without a policy
// engine: these tests are about the root-use protocol, not admission.
func invokeNative(t *testing.T, reg *registry.Registry, ctx context.Context, name, args string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return tool.Handler(ctx, registry.ToolCall{ID: "call-" + name, Name: name, Args: []byte(args)})
}

// workspaceUseFor wires a native registration to the shared root-use protocol of
// one repository, the way the app adapter does in production.
func workspaceUseFor(repository string) func(context.Context, string, string) (*worktree.UseLease, error) {
	return func(ctx context.Context, root, kind string) (*worktree.UseLease, error) {
		return worktree.AcquireWorkspaceUse(ctx, repository, root, worktree.UseIdentity{Kind: kind})
	}
}

// TestIdleLeaseIncludesDetachedAndQueuedCommands pins that an admitted execution
// at a root refuses lifecycle idleness for its whole life — including the
// insertion-to-start window, before the process exists — while an unrelated root
// stays usable.
func TestIdleLeaseIncludesDetachedAndQueuedCommands(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	blocking := newIdleBlockingRunner()
	ctx := context.Background()
	manager := native.NewJobManager(ctx, blocking, root, 8, time.Hour, 1<<20)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	info, err := manager.Launch(ctx, native.LaunchSpec{Command: "hold", Dir: root, Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Detached and possibly not yet started: the accepted execution already
	// exposes its cwd, so the freeze must see it.
	if release, err := manager.AcquireIdleWorkspaces(ctx, []string{root}); err == nil {
		release()
		t.Fatal("a queued detached command must not count as idle")
	} else if !errors.Is(err, native.ErrWorkspaceBusy) {
		t.Fatalf("AcquireIdleWorkspaces: got %v, want ErrWorkspaceBusy", err)
	}

	// An unrelated root remains usable, with everything released afterwards.
	releaseOther, err := manager.AcquireIdleWorkspaces(ctx, []string{other})
	if err != nil {
		t.Fatalf("unrelated root: %v", err)
	}
	releaseOther()

	close(blocking.release)
	if _, err := manager.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	release, err := manager.AcquireIdleWorkspaces(ctx, []string{root})
	if err != nil {
		t.Fatalf("after terminal settlement: %v", err)
	}
	release()
}

// TestIdleLeaseFreezesNewLaunches pins that a root reservation rejects new
// launches at that root until its release, that other roots are unaffected, and
// that a cancelled attempt leaves no reservation behind.
func TestIdleLeaseFreezesNewLaunches(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	ctx := context.Background()
	manager := native.NewJobManager(ctx, idleRunner{}, root, 8, time.Hour, 1<<20)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	release, err := manager.AcquireIdleWorkspaces(ctx, []string{root})
	if err != nil {
		t.Fatalf("AcquireIdleWorkspaces: %v", err)
	}
	if _, err := manager.Launch(ctx, native.LaunchSpec{Command: "true", Dir: root}); !errors.Is(err, native.ErrWorkspaceBusy) {
		t.Fatalf("launch at a frozen root: got %v, want ErrWorkspaceBusy", err)
	}
	if _, err := manager.Launch(ctx, native.LaunchSpec{Command: "true", Dir: other}); err != nil {
		t.Fatalf("launch at an unrelated root: %v", err)
	}
	release()
	info, err := manager.Launch(ctx, native.LaunchSpec{Command: "true", Dir: root})
	if err != nil {
		t.Fatalf("launch after release: %v", err)
	}
	if _, err := manager.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// A cancelled attempt reserves nothing: the root is launchable afterwards.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.AcquireIdleWorkspaces(cancelled, []string{root}); err == nil {
		t.Fatal("a cancelled context must not reserve a root")
	}
	info, err = manager.Launch(ctx, native.LaunchSpec{Command: "true", Dir: root})
	if err != nil {
		t.Fatalf("launch after a cancelled reservation: %v", err)
	}
	if _, err := manager.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestIdleLeaseDirectGitRead pins that a native git read — which bypasses the
// command service entirely — still prevents source cleanup until its own direct
// command settles.
func TestIdleLeaseDirectGitRead(t *testing.T) {
	root := t.TempDir()
	repository := idleRepo(t, root)
	blocking := newIdleBlockingRunner()
	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: root,
		CommandRunner: blocking,
		Config:        config.Default(),
		WorkspaceUse:  workspaceUseFor(repository),
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = invokeNative(t, reg, context.Background(), "git.status", `{}`)
	}()
	<-blocking.started

	// The direct read holds a shared root-use lease, so lifecycle exclusivity is
	// refused while it is in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := worktree.AcquireTargetLock(ctx, repository, root); !errors.Is(err, worktree.ErrTargetBusy) {
		t.Fatalf("while git.status holds the root: got %v, want ErrTargetBusy", err)
	}

	close(blocking.release)
	<-finished

	release, err := worktree.AcquireTargetLock(ctx, repository, root)
	if err != nil {
		t.Fatalf("after the direct command settled: %v", err)
	}
	release()
}

// TestIdleLeasePipelineVerifierAndExistingFinish pins both halves of the
// existing lifecycle: a pipeline verifier running in a checkout holds a shared
// use that refuses workspace.finish, and once it settles the same finish
// acquires the exclusive lease and completes.
func TestIdleLeasePipelineVerifierAndExistingFinish(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	idleRepo(t, root)
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{SessionID: "sess-idle"})
	repository, err := (worktree.CLIGitOps{}).RepositoryID(root)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: root,
		CommandRunner: idleRunner{},
		SessionState:  state,
		Config:        config.Default(),
		WorkspaceUse:  workspaceUseFor(repository),
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if _, err := invokeNative(t, reg, context.Background(), "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	ws := state.Workspace()
	if ws.ActiveRoot == root || ws.BaseSha == "" {
		t.Fatalf("isolation did not record a worktree and base: %+v", ws)
	}
	// Commit real work inside the worktree so the finish has something to merge.
	if err := os.WriteFile(filepath.Join(ws.ActiveRoot, "feat.txt"), []byte("from agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "agent work"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws.ActiveRoot
		if out, gerr := cmd.CombinedOutput(); gerr != nil {
			t.Fatalf("git %v: %v\n%s", args, gerr, out)
		}
	}

	// A verifier at the worktree root holds the shared use for its actual cwd.
	blocking := newIdleBlockingRunner()
	verifier := pipeline.Verifier{Build: "hold", Runner: pipelineVerifierRunner{blocking: blocking}}
	verified := make(chan error, 1)
	go func() {
		res, verr := verifier.Run(context.Background(), ws.ActiveRoot)
		if verr == nil && !res.OK {
			verr = errors.New("verify did not pass")
		}
		verified <- verr
	}()
	<-blocking.started

	res, err := invokeNative(t, reg, context.Background(), "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish while a verifier runs: %v", err)
	}
	if !strings.Contains(res.Content, "Refused to finish") {
		t.Fatalf("content = %q, want a refusal while the verifier holds the checkout", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "feat.txt")); !os.IsNotExist(err) {
		t.Fatalf("a refused finish must not merge anything: %v", err)
	}

	close(blocking.release)
	if err := <-verified; err != nil {
		t.Fatalf("verifier: %v", err)
	}

	// The verifier's lease is released with terminal evidence, so the same
	// finish now takes the exclusive lease and completes.
	res, err = invokeNative(t, reg, context.Background(), "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish after the verifier settled: %v", err)
	}
	if !strings.Contains(res.Content, "squash commit") {
		t.Fatalf("content = %q, want a completed squash merge", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "feat.txt")); err != nil {
		t.Fatalf("merged file missing: %v", err)
	}
}
