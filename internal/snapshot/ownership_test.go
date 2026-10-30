package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Env sentinels for the helper-process pattern. A test that needs a second OS
// process re-executes this test binary with one of these set, so the helper
// runs the same real code path as production rather than a stand-in.
const (
	envHelperMode      = "MARSHAL_SNAPSHOT_TEST_HELPER"
	envHelperLockRoot  = "MARSHAL_SNAPSHOT_TEST_LOCK_ROOT"
	envHelperReadyFile = "MARSHAL_SNAPSHOT_TEST_READY_FILE"
	envHelperHeartbeat = "MARSHAL_SNAPSHOT_TEST_HEARTBEAT"

	helperModeLock      = "hold-lock"
	helperModeWriter    = "writer-tree"
	helperModeHeartbeat = "heartbeat"
)

// TestSnapshotHelperProcess is the re-executed helper. It is inert unless a
// mode sentinel is set, so an ordinary `go test` run skips it.
//
// Every mode has a self-imposed deadline: if the parent test fails before it
// can kill the helper, the helper still exits instead of leaking a process.
func TestSnapshotHelperProcess(t *testing.T) {
	mode := os.Getenv(envHelperMode)
	if mode == "" {
		t.Skip("helper process; only runs when re-executed with a mode sentinel")
	}

	switch mode {
	case helperModeLock:
		root := os.Getenv(envHelperLockRoot)
		lock, err := AcquireStoreLock(context.Background(), root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper acquire: %v\n", err)
			os.Exit(2)
		}
		// Signal readiness only after the lock is genuinely held, so the
		// parent never measures a race.
		if err := os.WriteFile(os.Getenv(envHelperReadyFile), []byte("acquired"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "helper ready file: %v\n", err)
			os.Exit(2)
		}
		// Hold the lock. The parent kills this process to test crash release.
		waitOrTimeout(t, 30*time.Second)
		_ = lock.Release()

	case helperModeWriter:
		// The direct child spawns a grandchild that writes, then idles. The
		// grandchild is what proves whole-tree termination: killing only the
		// direct child would leave the writer running.
		heartbeat := os.Getenv(envHelperHeartbeat)
		grandchild := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
		grandchild.Env = append(os.Environ(),
			envHelperMode+"="+helperModeHeartbeat,
			envHelperHeartbeat+"="+heartbeat,
		)
		if err := grandchild.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "helper grandchild: %v\n", err)
			os.Exit(2)
		}
		waitOrTimeout(t, 30*time.Second)

	case helperModeHeartbeat:
		path := os.Getenv(envHelperHeartbeat)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			os.Exit(2)
		}
		defer f.Close()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := f.Write([]byte("x")); err != nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}
}

// waitOrTimeout blocks until timeout, which keeps a leaked helper from
// surviving forever.
func waitOrTimeout(_ *testing.T, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}

// startLockHolder starts a second OS process that acquires the store lock and
// holds it until it is killed. It returns the running command; the caller must
// kill it.
func startLockHolder(t *testing.T, root string) *exec.Cmd {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeLock,
		envHelperLockRoot+"="+root,
		envHelperReadyFile+"="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lock-holder helper never reported that it acquired the lock")
	return nil
}

// Two managers in the SAME process must never hold the store at once. The OS
// lock alone is not enough (flock is per-open-file-description), so this proves
// the process-local semaphore closes that gap.
func TestSameProcessManagersCannotBothOwnTheStore(t *testing.T) {
	dataDir := t.TempDir()
	first, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := first.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	second, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = second.Acquire(ctx)
	if err == nil {
		t.Fatal("second manager acquired the store while the first held it")
	}
	if got := ReasonOf(err); got != ReasonLockTimeout {
		t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonLockTimeout, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("second Acquire took %s; it should stop at its deadline", elapsed)
	}

	// After the first releases, the second must succeed. This is what rules
	// out a permanently wedged store, and proves the block was ownership and
	// not a broken lock file.
	if err := first.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if _, err := second.Acquire(context.Background()); err != nil {
		t.Fatalf("second Acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// Many goroutines competing for the same store must never both hold it. This is
// the goroutine half of the ownership requirement: the OS lock alone is not
// sufficient on every platform, so a process-local semaphore must serialize
// them.
func TestConcurrentGoroutinesNeverBothOwnTheStore(t *testing.T) {
	dataDir := t.TempDir()
	const workers = 8

	var (
		mu        sync.Mutex
		held      int
		maxHeld   int
		acquired  int
		releaseMu sync.Mutex
	)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := NewManager(dataDir)
			if err != nil {
				return
			}
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := m.Acquire(ctx); err != nil {
				return
			}
			mu.Lock()
			held++
			acquired++
			if held > maxHeld {
				maxHeld = held
			}
			mu.Unlock()

			// Hold briefly so overlap is actually observable.
			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			held--
			mu.Unlock()

			releaseMu.Lock()
			err = m.Release()
			releaseMu.Unlock()
			_ = err
		}()
	}
	close(start)
	wg.Wait()

	if acquired == 0 {
		t.Fatal("no goroutine ever acquired the store")
	}
	if maxHeld != 1 {
		t.Fatalf("up to %d goroutines held the store at once, want exactly 1", maxHeld)
	}
}

// The key cross-process proof: a second OS process cannot hold the same store.
func TestSeparateProcessesCannotBothOwnTheStore(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	holder := startLockHolder(t, m.Root())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := m.Acquire(ctx)
	if err == nil {
		t.Fatal("acquired the store while another OS process held it")
	}
	if got := ReasonOf(err); got != ReasonLockTimeout {
		t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonLockTimeout, err)
	}

	// Kill the holder; the OS must release the advisory lock with it.
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_, _ = holder.Process.Wait()

	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after holder death: %v", err)
	}
	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// Crash release: a process killed with SIGKILL must not leave the store locked
// forever. On Unix SIGKILL cannot be trapped, so this is the real crash case.
func TestCrashReleaseDoesNotLeaveStoreLocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL crash semantics are Unix-specific; Windows TerminateProcess is covered by the kill above")
	}
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	holder := startLockHolder(t, m.Root())

	// Prove the lock really is held before crashing the holder.
	busyCtx, busyCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer busyCancel()
	if _, err := m.Acquire(busyCtx); err == nil {
		t.Fatal("store was not actually locked by the helper")
	}

	if err := holder.Process.Signal(os.Kill); err != nil {
		t.Fatalf("SIGKILL holder: %v", err)
	}
	_, _ = holder.Process.Wait()

	// A crash must not require any cleanup step: the next acquire just works.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, err := m.Acquire(ctx)
		cancel()
		if err == nil {
			if relErr := m.Release(); relErr != nil {
				t.Fatalf("Release: %v", relErr)
			}
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("store stayed locked after the holder was SIGKILLed: %v", lastErr)
}

// A crashed holder must leave the permanent lock file behind, never delete it.
func TestCrashLeavesLockFilePresent(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	holder := startLockHolder(t, m.Root())
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_, _ = holder.Process.Wait()

	if _, err := os.Lstat(filepath.Join(m.Root(), LockFileName)); err != nil {
		t.Fatalf("lock file missing after holder crashed: %v", err)
	}
}

// A subprocess tree that is killed must leave no writer alive, including
// grandchildren, BEFORE ownership is released. The heartbeat file is the
// portable evidence: it stops changing only when every writer is dead.
func TestReleaseKillsSubprocessTreeBeforeUnlock(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeWriter,
		envHelperHeartbeat+"="+heartbeat,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(ctx, cmd)
		done <- err
	}()

	// Wait until the grandchild is demonstrably writing.
	waitForFileGrowth(t, heartbeat)

	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Release returns only after the tree is confirmed gone, so the size at
	// this instant is final. It must not change afterwards.
	sizeAfterRelease := fileSize(t, heartbeat)
	time.Sleep(400 * time.Millisecond)
	if grew := fileSize(t, heartbeat); grew != sizeAfterRelease {
		t.Fatalf("a writer survived ownership release: heartbeat grew from %d to %d bytes", sizeAfterRelease, grew)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunManaged did not return after its tree was drained")
	}
}

// Cancelling the caller's context must kill the tree and return the context
// error, rather than leaving a writer behind or hanging.
func TestRunManagedCancellationKillsTree(t *testing.T) {
	m := newTestManager(t)
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeWriter,
		envHelperHeartbeat+"="+heartbeat,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.RunManaged(ctx, cmd)
		done <- err
	}()

	waitForFileGrowth(t, heartbeat)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			// A tree whose direct child was killed can also surface the
			// child's exit error; what must never happen is a nil error that
			// implies the command "succeeded".
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("RunManaged error = %v, want context.Canceled or the child's exit error", err)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunManaged ignored cancellation")
	}

	sizeAtReturn := fileSize(t, heartbeat)
	time.Sleep(300 * time.Millisecond)
	if grew := fileSize(t, heartbeat); grew != sizeAtReturn {
		t.Fatalf("a writer survived cancellation: heartbeat grew from %d to %d bytes", sizeAtReturn, grew)
	}
}

// A child that floods its output must not exhaust the parent's memory: the
// retained output is bounded.
func TestRunManagedBoundsOutput(t *testing.T) {
	m := newTestManager(t, WithMaxCommandOutput(64))
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotOutputFloodHelper")
	cmd.Env = append(os.Environ(), "MARSHAL_SNAPSHOT_TEST_FLOOD=1")
	out, err := m.RunManaged(context.Background(), cmd)
	if err != nil {
		t.Fatalf("RunManaged: %v", err)
	}
	if len(out) > 256 {
		t.Fatalf("retained %d bytes of output, want it bounded near the 64-byte cap", len(out))
	}
}

// TestSnapshotOutputFloodHelper is inert unless its sentinel is set.
func TestSnapshotOutputFloodHelper(t *testing.T) {
	if os.Getenv("MARSHAL_SNAPSHOT_TEST_FLOOD") == "" {
		t.Skip("output-flood helper")
	}
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = 'z'
	}
	for i := 0; i < 256; i++ {
		if _, err := os.Stdout.Write(chunk); err != nil {
			break
		}
	}
	os.Exit(0)
}

func waitForFileGrowth(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fileSize(t, path) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper never began writing %s", path)
}

// requireStoreStillOwned fails unless a second owner is refused the store
// within a short deadline. Refusal is the evidence that the lock is still held;
// the timeout is what keeps a wedged lock from hanging the test.
func requireStoreStillOwned(t *testing.T, root, when string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	other, err := AcquireStoreLock(ctx, root)
	if err == nil {
		_ = other.Release()
		t.Fatalf("%s: a second owner acquired the store while the first still held it", when)
	}
	if got := ReasonOf(err); got != ReasonLockTimeout {
		t.Fatalf("%s: ReasonOf = %q, want %q (err = %v)", when, got, ReasonLockTimeout, err)
	}
}

// A drain that fails must not release the store, and a later Release must rerun
// exactly the callback that failed — never the ones that already succeeded.
func TestStoreLockRetriesFailedDrain(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireStoreLock(context.Background(), root)
	if err != nil {
		t.Fatalf("AcquireStoreLock: %v", err)
	}

	var (
		mu          sync.Mutex
		failedCalls int
		okCalls     int
	)
	injected := errors.New("writer still alive")
	lock.OnRelease(func() error {
		mu.Lock()
		defer mu.Unlock()
		failedCalls++
		if failedCalls == 1 {
			return injected
		}
		return nil
	})
	lock.OnRelease(func() error {
		mu.Lock()
		defer mu.Unlock()
		okCalls++
		return nil
	})

	err = lock.Release()
	if !errors.Is(err, injected) {
		t.Fatalf("first Release = %v, want it to report the failed drain %v", err, injected)
	}
	mu.Lock()
	gotFailed, gotOK := failedCalls, okCalls
	mu.Unlock()
	if gotFailed != 1 {
		t.Fatalf("failed drain ran %d times on the first Release, want 1", gotFailed)
	}
	if gotOK != 1 {
		t.Fatalf("successful drain ran %d times on the first Release, want 1", gotOK)
	}
	requireStoreStillOwned(t, root, "after a failed drain")

	// The retry reruns only the callback that failed.
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	mu.Lock()
	gotFailed, gotOK = failedCalls, okCalls
	mu.Unlock()
	if gotFailed != 2 {
		t.Fatalf("failed drain ran %d times after the retry, want 2", gotFailed)
	}
	if gotOK != 1 {
		t.Fatalf("successful drain ran %d times after the retry, want 1 (a successful drain must not run twice)", gotOK)
	}

	// Only now may another owner take the store.
	other, err := AcquireStoreLock(context.Background(), root)
	if err != nil {
		t.Fatalf("AcquireStoreLock after the retry succeeded: %v", err)
	}
	if err := other.Release(); err != nil {
		t.Fatalf("second owner Release: %v", err)
	}
}

// Two release callers must serialise: the second may not report success while
// the first is still draining, and the drain must run exactly once.
func TestStoreLockConcurrentReleaseWaitsForDrain(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireStoreLock(context.Background(), root)
	if err != nil {
		t.Fatalf("AcquireStoreLock: %v", err)
	}

	drainStarted := make(chan struct{})
	unblockDrain := make(chan struct{})
	var (
		mu      sync.Mutex
		drains  int
		overlap bool
	)
	lock.OnRelease(func() error {
		mu.Lock()
		drains++
		if drains > 1 {
			overlap = true
		}
		mu.Unlock()
		close(drainStarted)
		<-unblockDrain
		return nil
	})

	results := make(chan error, 2)
	var armed sync.WaitGroup
	armed.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			armed.Done()
			results <- lock.Release()
		}()
	}
	armed.Wait()
	<-drainStarted

	// Give the second caller every chance to reach Release, then assert it has
	// not reported success: the drain is still blocked, so the store is not
	// released and no caller may claim it was.
	for i := 0; i < 1000; i++ {
		if len(results) != 0 {
			break
		}
		runtime.Gosched()
	}
	if n := len(results); n != 0 {
		t.Fatalf("%d release call(s) reported completion while the drain was still blocked", n)
	}

	close(unblockDrain)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Release: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a concurrent Release did not return after the drain resolved")
		}
	}

	mu.Lock()
	gotDrains, gotOverlap := drains, overlap
	mu.Unlock()
	if gotOverlap {
		t.Fatal("drain callbacks ran concurrently; release attempts must serialise")
	}
	if gotDrains != 1 {
		t.Fatalf("drain ran %d times across two release callers, want 1", gotDrains)
	}
}

// A callback registered while a release is already draining must run before the
// lock is handed on, not be dropped and not be deferred past the unlock.
func TestStoreLockDrainsCallbackRegisteredDuringRelease(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireStoreLock(context.Background(), root)
	if err != nil {
		t.Fatalf("AcquireStoreLock: %v", err)
	}

	var (
		mu            sync.Mutex
		lateRan       int
		stillOwnedErr error
	)
	late := func() error {
		// Runs from inside the first release: the store must still be held.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		other, err := AcquireStoreLock(ctx, root)
		if err == nil {
			_ = other.Release()
		} else if ReasonOf(err) != ReasonLockTimeout {
			stillOwnedErr = err
		}
		mu.Lock()
		lateRan++
		mu.Unlock()
		return nil
	}
	lock.OnRelease(func() error {
		lock.OnRelease(late)
		return nil
	})

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	mu.Lock()
	gotLate, gotErr := lateRan, stillOwnedErr
	mu.Unlock()
	if gotErr != nil {
		t.Fatalf("late drain probing ownership: %v", gotErr)
	}
	if gotLate != 1 {
		t.Fatalf("callback registered during release ran %d times, want 1", gotLate)
	}

	// It ran inside the release, so the store is free now.
	other, err := AcquireStoreLock(context.Background(), root)
	if err != nil {
		t.Fatalf("AcquireStoreLock after release: %v", err)
	}
	if err := other.Release(); err != nil {
		t.Fatalf("second owner Release: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
