package acp

import (
	"bytes"
	"context"
	"testing"
)

func TestRunRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if err != context.Canceled {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}
