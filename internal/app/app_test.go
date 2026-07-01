package app

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRunReturnsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil), WithNow(func() time.Time {
		return time.Unix(100, 0)
	}))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}
