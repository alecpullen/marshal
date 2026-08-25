package session

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestWaitSubagentBlocksUntilFinish(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)

	type outcome struct {
		view SubagentView
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := s.WaitSubagent(context.Background(), v.ID)
		done <- outcome{got, err}
	}()

	select {
	case <-done:
		t.Fatal("WaitSubagent returned while the subagent was still running")
	case <-time.After(20 * time.Millisecond):
	}

	s.FinishSubagent(v.ID, "report", nil)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("WaitSubagent: %v", out.err)
		}
		if out.view.Status != SubagentDone {
			t.Fatalf("status = %v, want SubagentDone", out.view.Status)
		}
		if out.view.Summary != "report" {
			t.Fatalf("summary = %q, want %q", out.view.Summary, "report")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitSubagent did not return after FinishSubagent")
	}
}

func TestWaitSubagentReturnsFinishedViewImmediately(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)
	s.FinishSubagent(v.ID, "report", errors.New("boom"))

	got, err := s.WaitSubagent(context.Background(), v.ID)
	if err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}
	if got.Status != SubagentFailed {
		t.Fatalf("status = %v, want SubagentFailed", got.Status)
	}
	if got.Error != "boom" {
		t.Fatalf("Error = %q, want %q", got.Error, "boom")
	}
}

func TestWaitSubagentUnknownID(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	_, err := s.WaitSubagent(context.Background(), 999)
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

func TestWaitSubagentContextCancel(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.WaitSubagent(ctx, v.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestContextCancelledByShutdown(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	ctx := s.Context()
	if ctx == nil {
		t.Fatal("Context() returned nil")
	}
	select {
	case <-ctx.Done():
		t.Fatal("session context cancelled before Shutdown")
	default:
	}
	s.Shutdown()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session context not cancelled by Shutdown")
	}
}

func TestWaitAnyReturnsFirstFinisher(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	c := s.RegisterSubagent("c", nil)

	type outcome struct {
		view SubagentView
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID, c.ID})
		done <- outcome{got, err}
	}()

	select {
	case <-done:
		t.Fatal("WaitAnySubagent returned while all three were still running")
	case <-time.After(20 * time.Millisecond):
	}

	s.FinishSubagent(b.ID, "b report", nil)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("WaitAnySubagent: %v", out.err)
		}
		if out.view.ID != b.ID {
			t.Fatalf("returned #%d, want the first finisher #%d", out.view.ID, b.ID)
		}
		if out.view.Summary != "b report" {
			t.Fatalf("summary = %q, want %q", out.view.Summary, "b report")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAnySubagent did not return after a child finished")
	}
}

func TestWaitAnyReturnsAlreadyFinishedImmediately(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	s.FinishSubagent(a.ID, "done", nil)

	got, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID})
	if err != nil {
		t.Fatalf("WaitAnySubagent: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("returned #%d, want the already-finished #%d", got.ID, a.ID)
	}
}

func TestWaitAnyRespectsContext(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := s.WaitAnySubagent(ctx, []int64{a.ID}); err == nil {
		t.Fatal("want a context error, got nil")
	}
}

func TestWaitAnyRejectsEmptyIDs(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	if _, err := s.WaitAnySubagent(context.Background(), nil); err == nil {
		t.Fatal("want an error for an empty id set, not an infinite block")
	}
}

// The fan-in spawns one goroutine per pending child; the losers must all be
// released when the first winner is chosen.
func TestWaitAnyLeaksNoGoroutines(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	c := s.RegisterSubagent("c", nil)

	before := runtime.NumGoroutine()
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.FinishSubagent(c.ID, "done", nil)
	}()
	if _, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID, c.ID}); err != nil {
		t.Fatalf("WaitAnySubagent: %v", err)
	}
	// Give the released goroutines a moment to unwind.
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Errorf("goroutines: before %d, after %d — fan-in leaked", before, got)
	}
}
