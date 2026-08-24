package session

import (
	"context"
	"errors"
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
