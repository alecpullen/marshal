package bridge

import (
	"context"
	"testing"
	"time"
)

func TestSlotsBlockPastLimit(t *testing.T) {
	s := newSlots(2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.acquire(ctx); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}

	running, waiting := s.stats()
	if running != 2 || waiting != 0 {
		t.Fatalf("stats = (%d, %d), want (2, 0)", running, waiting)
	}

	third := make(chan error, 1)
	go func() { third <- s.acquire(ctx) }()

	// The third acquire must block until a slot frees.
	select {
	case err := <-third:
		t.Fatalf("third acquire returned %v immediately, want blocking", err)
	case <-time.After(100 * time.Millisecond):
	}

	s.release()
	select {
	case err := <-third:
		if err != nil {
			t.Fatalf("third acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("third acquire did not proceed after release")
	}
}

func TestSlotsRespectContextCancellation(t *testing.T) {
	s := newSlots(1)
	if err := s.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.acquire(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled acquire returned nil, want ctx error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled acquire did not return")
	}

	// A cancelled waiter must not leave a phantom in the waiting count.
	if _, waiting := s.stats(); waiting != 0 {
		t.Fatalf("waiting = %d after cancellation, want 0", waiting)
	}
}

func TestSlotsZeroLimitMeansUnbounded(t *testing.T) {
	s := newSlots(0)
	for i := 0; i < 50; i++ {
		if err := s.acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d under unbounded limit: %v", i, err)
		}
	}
}
