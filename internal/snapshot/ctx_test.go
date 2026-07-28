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

// nestedTree writes depth levels of directories, each holding one file, so the
// walk in largeFileExcludes has something to iterate over.
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
// returns the context error instead of shelling out to git.
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

// The tree walk is the unbounded phase of Track. Cancelling mid-flight must
// abort it rather than run to completion.
func TestLargeFileExcludesHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	nestedTree(t, dir, 5)
	svc := New(t.TempDir(), dir, 1, []string{"*.test"}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.largeFileExcludes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("largeFileExcludes error = %v, want context.Canceled", err)
	}
}

// A caller that cannot acquire the lock must observe its deadline rather than
// block forever behind a wedged Track.
func TestTrackDoesNotBlockForeverOnBusyService(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	nestedTree(t, dir, 2)
	svc := New(t.TempDir(), dir, 1, nil, testLogger())

	// Hold the service the way an in-flight Track would.
	svc.lock(context.Background())
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

// Even with a live context, a walk that will not finish must surface as an
// error instead of stalling the turn forever.
func TestTrackFailsWhenWalkExceedsBudget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	nestedTree(t, dir, 4)
	svc := New(t.TempDir(), dir, 1, nil, testLogger())
	svc.walkTimeout = time.Nanosecond

	done := make(chan error, 1)
	go func() {
		_, err := svc.Track(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Track error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Track ignored its walk budget")
	}
}
