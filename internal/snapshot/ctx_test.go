package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		if got := ReasonOf(err); got != ReasonCommandTimeout {
			t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonCommandTimeout, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Track blocked on a busy service instead of honoring its deadline")
	}
}

// A deadline that ends the capture before admission — here, while waiting for
// the service's operation semaphore — is classified as a timeout and backed
// off. Those failures never reach AdmitAndCaptureWithCleanup, which records the
// ones admission sees.
func TestTrackBacksOffADeadlineBeforeAdmission(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	nestedTree(t, dir, 2)
	svc := New(t.TempDir(), dir, 1, nil, testLogger())

	if err := svc.lock(context.Background()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := svc.Track(ctx)
	if err == nil {
		t.Fatal("Track succeeded while the service was held")
	}
	if got := ReasonOf(err); got != ReasonCommandTimeout {
		t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonCommandTimeout, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the classified timeout lost its cause: %v", err)
	}

	m, _, storeErr := svc.store()
	if storeErr != nil {
		t.Fatalf("store: %v", storeErr)
	}
	if reason, ok := m.suppressedCapture(dir); !ok || reason != ReasonCommandTimeout {
		t.Fatalf("suppressedCapture = (%q, %v), want (%q, true)", reason, ok, ReasonCommandTimeout)
	}

	// Release the semaphore: the next call is refused by the suppression before
	// any capture work starts.
	svc.unlock()
	start := time.Now()
	_, err = svc.Track(context.Background())
	if err == nil {
		t.Fatal("a suppressed workspace captured successfully")
	}
	if !strings.Contains(err.Error(), "suppressed") {
		t.Fatalf("second Track error = %v, want a suppression", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a suppressed capture took %s; it should be refused before capture", elapsed)
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
