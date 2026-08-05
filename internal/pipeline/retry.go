package pipeline

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	"marshal/internal/llm/provider"
)

// isTransientDispatchError decides whether a failed subagent dispatch is
// worth retrying with a fresh runner. It retries only while the pipeline's
// run context is still alive — ctx cancellation/timeout means the user
// aborted and we must stop immediately.
func isTransientDispatchError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}

	// Network-shaped errors are always worth retrying.
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne *net.OpError
	if errors.As(err, &ne) {
		return true
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		syscall.ECONNABORTED,
		syscall.ETIMEDOUT,
		syscall.ENETUNREACH,
		syscall.ENETDOWN,
		syscall.EHOSTUNREACH,
		syscall.EPIPE,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}

	// A per-request chat timeout (context.DeadlineExceeded) while the
	// pipeline context is still alive is a transient overload/stall, not a
	// user abort, so retry at the dispatch level.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Provider rate limits and server errors are worth retrying; client
	// errors are not.
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		if pe.StatusCode == 429 {
			return true
		}
		if pe.StatusCode >= 500 {
			return true
		}
	}

	// Unparseable implementer/reviewer reports are retried: a fresh
	// subagent can recover from a malformed response.
	if errors.Is(err, ErrReportParse) {
		return true
	}

	return false
}

// retryBackoff returns the delay before the next dispatch retry attempt
// (0-based): 5s, 15s, 45s.
func retryBackoff(attempt int) time.Duration {
	switch {
	case attempt < 0:
		return 0
	case attempt == 0:
		return 5 * time.Second
	case attempt == 1:
		return 15 * time.Second
	default:
		return 45 * time.Second
	}
}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// shortErr returns the first line of err truncated to 80 characters for
// progress messages.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
