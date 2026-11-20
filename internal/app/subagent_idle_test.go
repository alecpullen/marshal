package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/worktree"
)

// idleGateRunner settles every managed command immediately: these tests are
// about the gate's ordering and refusals, not about what a command prints.
type idleGateRunner struct{}

func (idleGateRunner) Run(context.Context, native.CommandRequest) (native.CommandResult, error) {
	return native.CommandResult{}, nil
}

// idleGateRepo initializes a git repository and returns its canonical
// repository identity, so the roots below have a cross-process participant.
func idleGateRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	repo = resolved
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	repository, err := (worktree.CLIGitOps{}).RepositoryID(repo)
	if err != nil {
		t.Fatalf("repository identity: %v", err)
	}
	return repo, repository
}

// idleGateFor builds one runtime's gate: its own session, write gate and
// command service, exactly as two conversations in one process would have.
func idleGateFor(t *testing.T, sessionID, workingDir string) *subagentIdleGate {
	t.Helper()
	state := session.New(config.Default(), workingDir, time.Unix(100, 0), session.Persistence{SessionID: sessionID})
	jobs := native.NewJobManager(context.Background(), idleGateRunner{}, workingDir, 8, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jobs.Shutdown(context.Background()) })
	return newSubagentIdleGate(state, &swarm.WriteLock{}, jobs, worktree.CLIGitOps{})
}

// TestIdleLeaseIncludesLiveSharedChildren pins that a shared child still running
// at the integration target makes the target busy even between its tool calls,
// and that a settled child no longer does. Only the durable record can say so: a
// child between tool calls holds no command of its own.
func TestIdleLeaseIncludesLiveSharedChildren(t *testing.T) {
	repo, repository := idleGateRepo(t)
	target := filepath.Join(repo, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	gate := idleGateFor(t, "owner-1", repo)

	gate.state.ApplySubagentWorkspace(subagentwork.Record{
		Version:        1,
		OwnerSessionID: "owner-1",
		TaskKey:        "task-1",
		Revision:       1,
		Outcome:        subagentwork.OutcomeRunning,
		Binding:        subagentwork.Binding{Mode: subagentwork.ModeShared, Root: target, Repository: repository},
		Target:         subagentwork.Target{Root: target, Repository: repository},
	})
	ctx := context.Background()
	if _, err := gate.AcquireIdle(ctx, "owner-1", []string{target}); !errors.Is(err, native.ErrWorkspaceBusy) {
		t.Fatalf("with a live shared child: got %v, want ErrWorkspaceBusy", err)
	}

	// The child settled: its record is no longer a live user, and nothing else
	// holds the target, so the mutation may proceed.
	gate.state.ApplySubagentWorkspace(subagentwork.Record{
		Version:        1,
		OwnerSessionID: "owner-1",
		TaskKey:        "task-1",
		Revision:       2,
		Outcome:        subagentwork.OutcomeDone,
		Binding:        subagentwork.Binding{Mode: subagentwork.ModeShared, Root: target, Repository: repository},
		Target:         subagentwork.Target{Root: target, Repository: repository},
	})
	release, err := gate.AcquireIdle(ctx, "owner-1", []string{target})
	if err != nil {
		t.Fatalf("after the child settled: %v", err)
	}
	release()
}

// TestIdleLeaseOtherRuntimeWriter pins that another runtime's ordinary
// file-write claim at the target refuses this runtime's lifecycle exclusivity,
// so an integration or discard cannot mutate underneath it.
func TestIdleLeaseOtherRuntimeWriter(t *testing.T) {
	repo, _ := idleGateRepo(t)
	target := filepath.Join(repo, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writer := idleGateFor(t, "owner-a", repo)
	mutator := idleGateFor(t, "owner-b", repo)
	ctx := context.Background()

	// Another runtime holds a normal shared root use — the claim every direct
	// file or search operation publishes.
	lease, err := writer.workspaceUse(ctx, target, worktree.UseKindTool)
	if err != nil {
		t.Fatalf("writer lease: %v", err)
	}
	if lease == nil {
		t.Fatal("a git checkout must publish a root-use lease")
	}
	if _, err := mutator.AcquireIdle(ctx, "owner-b", []string{target}); !errors.Is(err, native.ErrWorkspaceBusy) {
		t.Fatalf("with another runtime writing: got %v, want ErrWorkspaceBusy", err)
	}
	if err := lease.Release(true); err != nil {
		t.Fatalf("writer release: %v", err)
	}
	release, err := mutator.AcquireIdle(ctx, "owner-b", []string{target})
	if err != nil {
		t.Fatalf("after the other runtime settled: %v", err)
	}
	release()
}

// TestIdleLeaseLockOrderingAndRelease pins that roots are acquired in canonical
// sorted order, and that a waiter refused part-way through a multi-root
// acquisition releases every reservation it had already taken.
func TestIdleLeaseLockOrderingAndRelease(t *testing.T) {
	repo, _ := idleGateRepo(t)
	a := filepath.Join(repo, "a")
	b := filepath.Join(repo, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The ordering is canonical: aliases collapse and the result is sorted, so
	// two callers naming the same roots in opposite orders acquire in the same
	// order.
	got := canonicalRoots([]string{b, a, b + string(filepath.Separator), ""})
	want := []string{a, b}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalRoots = %v, want %v", got, want)
	}

	holder := idleGateFor(t, "owner-1", repo)
	waiter := idleGateFor(t, "owner-2", repo)
	ctx := context.Background()

	releaseB, err := holder.AcquireIdle(ctx, "owner-1", []string{b})
	if err != nil {
		t.Fatalf("holder acquisition: %v", err)
	}
	// The waiter sorts [b, a] to [a, b]: it takes a first, then is refused by b,
	// and must give a back rather than holding half a mutation.
	if _, err := waiter.AcquireIdle(ctx, "owner-2", []string{b, a}); !errors.Is(err, native.ErrWorkspaceBusy) {
		t.Fatalf("partially blocked waiter: got %v, want ErrWorkspaceBusy", err)
	}
	third := idleGateFor(t, "owner-3", repo)
	releaseA, err := third.AcquireIdle(ctx, "owner-3", []string{a})
	if err != nil {
		t.Fatalf("the stopped waiter leaked a root: %v", err)
	}
	releaseA()

	// Once the holder releases, the same waiter takes both roots: its write
	// lock, command reservation and file locks were all given back.
	releaseB()
	releaseAll, err := waiter.AcquireIdle(ctx, "owner-2", []string{b, a})
	if err != nil {
		t.Fatalf("waiter after release: %v", err)
	}
	releaseAll()

	// Opposing root lists acquire concurrently without deadlocking: the retry
	// loop is bounded by a deadline, not by a fixed sleep.
	errorsOut := make(chan error, 2)
	acquire := func(g *subagentIdleGate, roots []string) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			release, err := g.AcquireIdle(context.Background(), "owner", roots)
			if err == nil {
				release()
				errorsOut <- nil
				return
			}
			if !errors.Is(err, native.ErrWorkspaceBusy) {
				errorsOut <- err
				return
			}
			if time.Now().After(deadline) {
				errorsOut <- err
				return
			}
			runtime.Gosched()
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); acquire(holder, []string{a, b}) }()
	go func() { defer wg.Done(); acquire(waiter, []string{b, a}) }()
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("opposing root lists deadlocked or leaked: %v", err)
		}
	}
}
