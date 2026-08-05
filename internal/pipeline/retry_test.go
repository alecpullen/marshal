package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	"marshal/internal/llm/provider"
)

func TestIsTransientDispatchError_NetworkShapes(t *testing.T) {
	ctx := context.Background()
	cases := []error{
		&url.Error{Op: "Post", URL: "http://localhost", Err: errors.New("refused")},
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
		&net.DNSError{Name: "api.example.com"},
		io.EOF,
		io.ErrUnexpectedEOF,
		fmt.Errorf("wrapped: %w", syscall.ECONNRESET),
		fmt.Errorf("wrapped: %w", syscall.ETIMEDOUT),
	}
	for _, err := range cases {
		if !isTransientDispatchError(ctx, err) {
			t.Errorf("isTransientDispatchError(ctx, %T) = false, want true", err)
		}
	}
}

func TestIsTransientDispatchError_DeadlineExceededWithLiveCtx(t *testing.T) {
	ctx := context.Background()
	if !isTransientDispatchError(ctx, context.DeadlineExceeded) {
		t.Error("DeadlineExceeded with live ctx should be transient")
	}
}

func TestIsTransientDispatchError_NotTransient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"ctx canceled", context.Canceled},
		{"ctx done", func() error { cancel(); return errors.New("network") }()},
		{"provider 400", &provider.ProviderError{StatusCode: 400}},
		{"provider 404", &provider.ProviderError{StatusCode: 404}},
		{"provider 499", &provider.ProviderError{StatusCode: 499}},
		{"plain error", errors.New("something bad")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isTransientDispatchError(ctx, tc.err) {
				t.Errorf("isTransientDispatchError(ctx, %v) = true, want false", tc.err)
			}
		})
	}
}

func TestIsTransientDispatchError_ProviderRetryable(t *testing.T) {
	ctx := context.Background()
	for _, code := range []int{429, 500, 502, 503, 504} {
		err := &provider.ProviderError{StatusCode: code}
		if !isTransientDispatchError(ctx, err) {
			t.Errorf("status %d should be transient", code)
		}
	}
}

func TestRetryBackoff(t *testing.T) {
	want := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}
	for i, d := range want {
		if got := retryBackoff(i); got != d {
			t.Errorf("retryBackoff(%d) = %v, want %v", i, got, d)
		}
	}
	if got := retryBackoff(-1); got != 0 {
		t.Errorf("retryBackoff(-1) = %v, want 0", got)
	}
	if got := retryBackoff(10); got != 45*time.Second {
		t.Errorf("retryBackoff(10) = %v, want 45s", got)
	}
}

func TestShortErr(t *testing.T) {
	if got := shortErr(nil); got != "" {
		t.Errorf("shortErr(nil) = %q, want empty", got)
	}
	if got := shortErr(errors.New("one line")); got != "one line" {
		t.Errorf("shortErr = %q", got)
	}
	multi := errors.New("first line\nsecond line\nthird")
	if got := shortErr(multi); got != "first line" {
		t.Errorf("shortErr multi-line = %q", got)
	}
	long := errors.New(string(make([]byte, 200)))
	if got := shortErr(long); len(got) > 83 {
		t.Errorf("shortErr long = %d chars, want <= 83", len(got))
	}
}
