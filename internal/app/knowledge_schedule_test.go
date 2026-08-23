package app

import (
	"context"
	"sync"
	"testing"

	"marshal/internal/knowledge"
)

func TestKnowledgeSchedulerFiresEveryFiveTurns(t *testing.T) {
	var mu sync.Mutex
	var runs int
	done := make(chan struct{}, 10)
	s := &knowledgeScheduler{
		input: func() knowledge.ExtractInput { return knowledge.ExtractInput{} },
		run: func(ctx context.Context, in knowledge.ExtractInput) {
			mu.Lock()
			runs++
			mu.Unlock()
			done <- struct{}{}
		},
	}
	for i := 0; i < 4; i++ {
		s.OnTurn()
	}
	mu.Lock()
	if runs != 0 {
		mu.Unlock()
		t.Fatalf("runs = %d after 4 turns, want 0", runs)
	}
	mu.Unlock()
	s.OnTurn()
	<-done
	mu.Lock()
	if runs != 1 {
		mu.Unlock()
		t.Fatalf("runs = %d after 5 turns, want 1", runs)
	}
	mu.Unlock()
}

func TestKnowledgeSchedulerSkipsWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var runs int
	s := &knowledgeScheduler{
		input: func() knowledge.ExtractInput { return knowledge.ExtractInput{} },
		run: func(ctx context.Context, in knowledge.ExtractInput) {
			mu.Lock()
			runs++
			mu.Unlock()
			<-release // hold the run open
		},
	}
	for i := 0; i < 5; i++ {
		s.OnTurn()
	}
	// Wait until the first run is in flight.
	for {
		mu.Lock()
		n := runs
		mu.Unlock()
		if n == 1 {
			break
		}
	}
	// These triggers must be dropped while the first run holds the gate.
	s.OnCompaction()
	for i := 0; i < 5; i++ {
		s.OnTurn()
	}
	close(release)
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (concurrent triggers dropped)", runs)
	}
}

func TestKnowledgeSchedulerCompactionFiresImmediately(t *testing.T) {
	done := make(chan struct{}, 1)
	s := &knowledgeScheduler{
		input: func() knowledge.ExtractInput { return knowledge.ExtractInput{} },
		run:   func(ctx context.Context, in knowledge.ExtractInput) { done <- struct{}{} },
	}
	s.OnCompaction() // no turns counted — still fires
	<-done
}
