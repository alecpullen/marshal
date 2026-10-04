package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitUnheldPassesWhenNotHeld(t *testing.T) {
	s := newTestState()
	if err := s.WaitUnheld(context.Background()); err != nil {
		t.Fatalf("WaitUnheld = %v", err)
	}
}

func TestWaitUnheldBlocksUntilRelease(t *testing.T) {
	s := newTestState()
	s.SetHold(true)
	if !s.Held() {
		t.Fatal("Held = false after SetHold(true)")
	}
	done := make(chan error, 1)
	go func() { done <- s.WaitUnheld(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("WaitUnheld returned while held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	s.SetHold(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitUnheld = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUnheld did not return after release")
	}
	if s.Held() {
		t.Fatal("Held = true after release")
	}
}

func TestWaitUnheldReturnsOnCancel(t *testing.T) {
	s := newTestState()
	s.SetHold(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.WaitUnheld(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitUnheld = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUnheld did not return on cancel")
	}
}

func TestSetHoldIsIdempotent(t *testing.T) {
	s := newTestState()
	s.SetHold(false)
	s.SetHold(true)
	s.SetHold(true)
	s.SetHold(false)
	s.SetHold(false)
	if s.Held() {
		t.Fatal("still held")
	}
}
