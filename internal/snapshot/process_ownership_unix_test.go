//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeGroupProbe stands in for the signal-0 group probe. The tree it is
// installed on is real — the child is really started, killed and reaped — but
// its disappearance is reported by the test, so an uncertain tree can be modelled
// without signalling an unrelated process and without waiting out the production
// budget on every attempt.
type fakeGroupProbe struct {
	mu  sync.Mutex
	err error
}

func (p *fakeGroupProbe) probe(int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *fakeGroupProbe) set(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

// fakeProbeTree returns an instance-scoped tree constructor that builds a real
// process group whose probe is answered by p.
func fakeProbeTree(p *fakeGroupProbe) func(*exec.Cmd) (*processTree, error) {
	return func(cmd *exec.Cmd) (*processTree, error) {
		tree, err := newProcessTree(cmd)
		if err != nil {
			return nil, err
		}
		tree.probe = p.probe
		tree.timeout = 200 * time.Millisecond
		return tree, nil
	}
}

// shortBudgetTree returns the production tree with only its disappearance budget
// shortened, so a test can exercise the real group probe without spending
// processTreeKillTimeout per attempt.
func shortBudgetTree(budget time.Duration) func(*exec.Cmd) (*processTree, error) {
	return func(cmd *exec.Cmd) (*processTree, error) {
		tree, err := newProcessTree(cmd)
		if err != nil {
			return nil, err
		}
		tree.timeout = budget
		return tree, nil
	}
}

// writerTreeCmd is a child that spawns a grandchild writer and idles.
func writerTreeCmd(heartbeat string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeWriter,
		envHelperHeartbeat+"="+heartbeat,
	)
	return cmd
}

// forkWriterCmd is a child that spawns a grandchild writer and exits at once.
func forkWriterCmd(heartbeat string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeForkWriter,
		envHelperHeartbeat+"="+heartbeat,
	)
	return cmd
}

// trackedTrees snapshots the trees the manager still owns the fate of.
func trackedTrees(m *Manager) []*ownedTree {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ownedTree, 0, len(m.trees))
	for life := range m.trees {
		out = append(out, life)
	}
	return out
}

// requireUnknownTermination fails unless err reports that a started tree's exit
// could not be proved.
func requireUnknownTermination(t *testing.T, err error) {
	t.Helper()
	var unknown *treeTerminationUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want a *treeTerminationUnknownError", err)
	}
}

// requireUnchangedFile fails if path keeps growing, which is what a surviving
// writer looks like from outside.
func requireUnchangedFile(t *testing.T, path, when string) {
	t.Helper()
	before := fileSize(t, path)
	time.Sleep(300 * time.Millisecond)
	if after := fileSize(t, path); after != before {
		t.Fatalf("a writer survived %s: %s grew from %d to %d bytes", when, path, before, after)
	}
}

// A tree whose disappearance cannot be proved must keep the store lock: releasing
// on the assumption that the group is gone would hand the store to a second owner
// while a writer may still be alive. Once disappearance is confirmed the release
// succeeds, closes the handle exactly once and lets the store move on.
func TestManagerRetainsUnknownTreeAndLock(t *testing.T) {
	probe := &fakeGroupProbe{err: syscall.EPERM}
	m := newTestManager(t)
	m.newTree = fakeProbeTree(probe)
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(ctx, writerTreeCmd(heartbeat))
		done <- err
	}()
	waitForFileGrowth(t, heartbeat)
	cancel()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunManaged did not return after cancellation")
	}
	requireUnknownTermination(t, runErr)
	if !errors.Is(runErr, syscall.EPERM) {
		t.Fatalf("RunManaged error = %v, want it to wrap the probe's EPERM", runErr)
	}

	trees := trackedTrees(m)
	if len(trees) != 1 {
		t.Fatalf("tracked %d trees, want the one whose exit is unproven", len(trees))
	}
	tree := trees[0]

	// Repeated releases must fail, and none of them may free the store.
	for i := 1; i <= 2; i++ {
		if err := m.Release(); err == nil {
			t.Fatalf("Release %d reported success while a tree's exit was unproven", i)
		}
		requireStoreStillOwned(t, m.Root(), "after a release with an unproven tree")
	}

	// Now the group is genuinely gone.
	probe.set(syscall.ESRCH)
	if err := m.Release(); err != nil {
		t.Fatalf("Release after the tree was confirmed gone: %v", err)
	}
	if got := tree.closes(); got != 1 {
		t.Fatalf("platform handle closed %d times, want exactly 1", got)
	}
	if got := len(trackedTrees(m)); got != 0 {
		t.Fatalf("%d trees still tracked after a confirmed drain, want 0", got)
	}

	second := newTestManagerAt(t, m.Root())
	if _, err := second.Acquire(context.Background()); err != nil {
		t.Fatalf("second manager Acquire after the confirmed drain: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second manager Release: %v", err)
	}
}

// A release that races an in-flight start must wait for the start to resolve. A
// tree that has no group id yet would otherwise read as gone, the lock would be
// handed on, and the child would begin writing after the unlock.
func TestManagerReleaseWaitsForInFlightTreeStart(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	buildStarted := make(chan struct{})
	allowBuild := make(chan struct{})
	m.newTree = func(cmd *exec.Cmd) (*processTree, error) {
		close(buildStarted)
		<-allowBuild
		return newProcessTree(cmd)
	}

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	runDone := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(context.Background(), writerTreeCmd(heartbeat))
		runDone <- err
	}()
	<-buildStarted // registered, but the child has not started yet

	releaseDone := make(chan error, 1)
	go func() { releaseDone <- m.Release() }()

	// The lock stays held while the start is still in flight.
	requireStoreStillOwned(t, m.Root(), "while a tree start was in flight")
	select {
	case err := <-releaseDone:
		t.Fatalf("Release returned (%v) while a tree start was still in flight", err)
	default:
	}

	// Let the child start. The drain must now kill and reap it before unlocking.
	close(allowBuild)
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatalf("Release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Release did not return after the in-flight start resolved")
	}

	requireUnchangedFile(t, heartbeat, "the release that raced the start")

	select {
	case <-runDone:
	case <-time.After(15 * time.Second):
		t.Fatal("RunManaged did not return after its tree was drained")
	}
}

// A leader that exits normally does not prove its descendants exited. The tree
// must stay owned until the writer is confirmed gone, and the lock must not be
// released underneath it.
func TestManagerRetainsTreeWhenLeaderExitsBeforeWriter(t *testing.T) {
	m := newTestManager(t)
	// The group probe is the real one; only the disappearance budget is
	// shortened so a live descendant does not cost the production budget.
	m.newTree = shortBudgetTree(250 * time.Millisecond)
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	runDone := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(context.Background(), forkWriterCmd(heartbeat))
		runDone <- err
	}()

	var runErr error
	select {
	case runErr = <-runDone:
	case <-time.After(30 * time.Second):
		t.Fatal("RunManaged did not return after the leader exited")
	}
	requireUnknownTermination(t, runErr)
	if !strings.Contains(runErr.Error(), "still has live members") {
		// A live descendant is exactly what the probe should have reported; a nil
		// error, which would have claimed the tree was gone, is what must not
		// happen.
		t.Fatalf("RunManaged error = %v, want the live-member evidence from the group probe", runErr)
	}
	if got := len(trackedTrees(m)); got != 1 {
		t.Fatalf("tracked %d trees after a leader exited with a live writer, want 1", got)
	}
	// The grandchild is still writing; it must not have been discarded.
	if fileSize(t, heartbeat) == 0 {
		// The writer may still be warming up; wait for it to start.
		waitForFileGrowth(t, heartbeat)
	}

	// Releasing must kill the surviving writer before handing the store on.
	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	requireUnchangedFile(t, heartbeat, "ownership release")
	if got := len(trackedTrees(m)); got != 0 {
		t.Fatalf("%d trees still tracked after a confirmed drain, want 0", got)
	}
}

// Every managed launch path must keep a tree whose exit is unproven, and a start
// that fails before a process exists must leave nothing behind.
func TestManagerRetainsUnknownTreeForEveryLaunchPath(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(*Manager, string) error
	}{
		{
			name: "gitRun",
			run: func(m *Manager, dir string) error {
				_, _, _, err := m.gitRun(ctx, dir, nil, "version")
				return err
			},
		},
		{
			name: "gitRunEnv",
			run: func(m *Manager, dir string) error {
				_, _, _, err := m.gitRunEnv(ctx, dir, dir, nil, nil, 0, "version")
				return err
			},
		},
		{
			name: "gitRunEnvWriter",
			run: func(m *Manager, dir string) error {
				_, _, err := m.gitRunEnvWriter(ctx, dir, io.Discard, "version")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeGroupProbe{err: syscall.EPERM}
			m := newTestManager(t)
			m.newTree = fakeProbeTree(probe)
			if _, err := m.Acquire(ctx); err != nil {
				t.Fatalf("Acquire: %v", err)
			}

			dir := t.TempDir()
			requireUnknownTermination(t, tc.run(m, dir))
			if got := len(trackedTrees(m)); got != 1 {
				t.Fatalf("%s tracked %d trees with an unproven exit, want 1", tc.name, got)
			}
			if err := m.Release(); err == nil {
				t.Fatalf("%s: Release reported success while a tree's exit was unproven", tc.name)
			}

			probe.set(syscall.ESRCH)
			if err := m.Release(); err != nil {
				t.Fatalf("%s: Release after confirmation: %v", tc.name, err)
			}
			if got := len(trackedTrees(m)); got != 0 {
				t.Fatalf("%s left %d trees tracked after a confirmed drain", tc.name, got)
			}
		})
	}

	t.Run("start failure", func(t *testing.T) {
		m := newTestManager(t)
		if _, err := m.Acquire(ctx); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		boom := errors.New("no tree for you")
		m.newTree = func(*exec.Cmd) (*processTree, error) { return nil, boom }

		_, err := m.RunManaged(ctx, writerTreeCmd(filepath.Join(t.TempDir(), "heartbeat")))
		if !errors.Is(err, boom) {
			t.Fatalf("RunManaged = %v, want the constructor's error", err)
		}
		if got := len(trackedTrees(m)); got != 0 {
			t.Fatalf("a start that failed before any process existed left %d trees tracked, want 0", got)
		}
		if err := m.Release(); err != nil {
			t.Fatalf("Release after a failed start: %v", err)
		}
	})
}

// Two release callers racing an uncertain tree must both fail, serialise, and
// leave the store owned until the tree is confirmed gone.
func TestManagerConcurrentReleaseKeepsUncertainTree(t *testing.T) {
	probe := &fakeGroupProbe{err: syscall.EPERM}
	m := newTestManager(t)
	m.newTree = fakeProbeTree(probe)
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(ctx, writerTreeCmd(heartbeat))
		done <- err
	}()
	waitForFileGrowth(t, heartbeat)
	cancel()
	select {
	case err := <-done:
		requireUnknownTermination(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("RunManaged did not return after cancellation")
	}

	results := make(chan error, 2)
	var armed sync.WaitGroup
	armed.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			armed.Done()
			results <- m.Release()
		}()
	}
	armed.Wait()

	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatalf("concurrent Release %d reported success while a tree's exit was unproven", i+1)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a concurrent Release did not return")
		}
	}
	requireStoreStillOwned(t, m.Root(), "after two concurrent failed releases")

	probe.set(syscall.ESRCH)
	if err := m.Release(); err != nil {
		t.Fatalf("Release after confirmation: %v", err)
	}
	second := newTestManagerAt(t, m.Root())
	if _, err := second.Acquire(context.Background()); err != nil {
		t.Fatalf("second manager Acquire after the confirmed drain: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second manager Release: %v", err)
	}
}
