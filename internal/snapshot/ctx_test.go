package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// nestedTree writes depth levels of directories, each holding one file, so a
// capture's eligibility listing has something to iterate over.
func nestedTree(t *testing.T, root string, depth int) {
	t.Helper()
	dir := root
	for i := 0; i < depth; i++ {
		dir = filepath.Join(dir, "d")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// Track must not start work it cannot finish: an already-cancelled context
// returns the context error rather than starting a capture.
func TestTrackReturnsOnCancelledContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	nestedTree(t, dir, 3)
	svc := New(t.TempDir(), dir, 1, []string{"*.test"}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.Track(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Track error = %v, want context.Canceled", err)
	}
}

// The eligibility listing is the unbounded phase of a capture: it enumerates
// the whole workspace. Cancelling mid-flight must abort it rather than run to
// completion.
func TestEligibilityDiscoveryHonorsCancellation(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	nestedTree(t, dir, 5)
	svc := New(t.TempDir(), dir, 1, []string{"*.test"}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m, cat, err := svc.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	_, planErr := m.planCapture(ctx, cat, CaptureRequest{
		WorkspaceRoot: dir,
		Ignore:        []string{"*.test"},
		MaxFileBytes:  1,
		Bounds:        defaultCaptureBounds(),
	})
	if !errors.Is(planErr, context.Canceled) {
		t.Fatalf("planCapture error = %v, want context.Canceled", planErr)
	}
	if !errors.Is(planErr, ErrInterruptedCapture) {
		t.Fatalf("planCapture error = %v, want ErrInterruptedCapture", planErr)
	}
}

// A caller that cannot acquire the service's own operation lock must observe
// its deadline rather than block forever behind a wedged capture.
func TestTrackDoesNotBlockForeverOnBusyService(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	nestedTree(t, dir, 2)
	svc := New(t.TempDir(), dir, 1, nil, testLogger())

	// Hold the service the way an in-flight Track would.
	if err := svc.lock(context.Background()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer svc.unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := svc.Track(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Track error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Track blocked on a busy service instead of honoring its deadline")
	}
}

// A capture whose context ends while it is running must abandon it: the store
// lock is released and the partial work is not reported as a snapshot.
//
// This replaces the old work-tree-walk budget test. The walk that test bounded
// no longer exists: eligibility is now decided by a Git listing with an output
// bound, and the resource that has to be bounded during a capture is the
// reservation, not a timer around a filesystem traversal. What the old test
// was really asserting — "a capture that cannot finish must surface an error
// instead of stalling the turn forever" — is asserted here against the
// mechanism that replaced it.
func TestTrackAbandonsACaptureWhenTheContextEnds(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	nestedTree(t, dir, 4)
	svc := New(t.TempDir(), dir, 1, nil, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := svc.Track(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Track error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Track ignored its cancelled context")
	}
}

// The store lock must not be held after an operation returns: a service that
// leaked ownership would block every other Marshal process for the life of the
// session.
func TestTrackReleasesStoreOwnership(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := New(t.TempDir(), dir, 2_000_000, nil, testLogger())
	if _, err := svc.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	m, _, err := svc.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if m.Owned() {
		t.Fatal("Track released its result but not the store lock")
	}
}
