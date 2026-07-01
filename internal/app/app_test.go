package app

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunSkipsProgramWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithProgramRunner(func(model tea.Model, output io.Writer) error {
			called = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("program runner was called after context cancellation")
	}
}

func TestRunStartsProgram(t *testing.T) {
	stdout := bytes.NewBuffer(nil)

	called := false
	err := Run(context.Background(), stdout, bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithProgramRunner(func(model tea.Model, output io.Writer) error {
			called = true
			if output != stdout {
				t.Fatal("runner did not receive stdout buffer")
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}
