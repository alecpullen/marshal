package worker

import (
	"context"
	"errors"
	"testing"
)

type stub struct{ ran bool }

func (s *stub) Name() string { return "stub" }
func (s *stub) Run(ctx context.Context) error {
	s.ran = true
	<-ctx.Done()
	return ctx.Err()
}

func TestWorkerRunsUntilCancel(t *testing.T) {
	var w Worker = &stub{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if w.Name() != "stub" {
		t.Fatalf("Name = %q", w.Name())
	}
}
