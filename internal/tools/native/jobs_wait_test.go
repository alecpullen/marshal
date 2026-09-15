package native

import (
	"context"
	"strings"
	"testing"
	"time"
)

// waitTestRunner finishes jobs after a configurable delay so Wait tests get
// deterministic running→terminal transitions (fakeRunner returns
// DeadlineExceeded when req.Timeout is unset, which always yields a failed
// job).
type waitTestRunner struct{ delay time.Duration }

func (r *waitTestRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	select {
	case <-time.After(r.delay):
		return CommandResult{ExitCode: 0, Stdout: "done"}, nil
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
}

func TestJobManagerWaitUnknownID(t *testing.T) {
	m := NewJobManager(context.Background(), &waitTestRunner{}, t.TempDir(), 5, time.Hour, 1<<20)
	_, err := m.Wait(context.Background(), "job-999")
	if err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("Wait unknown = %v, want unknown-job error", err)
	}
}

func TestJobManagerWaitReturnsTerminalJobImmediately(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := m.Start(ctx, "true", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if info.Status != StatusCompleted || info.ExitCode == nil || *info.ExitCode != 0 {
		t.Fatalf("Wait = %+v, want completed/exit 0", info)
	}
}

func TestJobManagerWaitBlocksUntilDone(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: 150 * time.Millisecond}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := m.Start(ctx, "slow", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	start := time.Now()
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("Wait returned after %s; it did not block", elapsed)
	}
	if info.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", info.Status)
	}
}

func TestJobManagerWaitHonorsContextCancel(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := m.Start(ctx, "hang", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := m.Wait(waitCtx, id); err == nil || err != context.DeadlineExceeded {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}
}

func TestJobManagerWaitAfterKill(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := m.Start(ctx, "killme", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if info.Status != StatusKilled {
		t.Fatalf("status = %s, want killed", info.Status)
	}
}

func TestJobManagerOutstandingListsOnlyRunning(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	idA, err := m.Start(ctx, "a", 0)
	if err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if _, err := m.Start(ctx, "b", 0); err != nil {
		t.Fatalf("Start b: %v", err)
	}
	if got := len(m.Outstanding()); got != 2 {
		t.Fatalf("Outstanding = %d, want 2", got)
	}
	if err := m.Kill(idA); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := len(m.Outstanding()); got != 1 {
		t.Fatalf("Outstanding after kill = %d, want 1", got)
	}
	out := m.Outstanding()
	if out[0].Command != "b" {
		t.Fatalf("Outstanding[0] = %s, want job b", out[0].Command)
	}
}
