package sandbox

import (
	"context"

	"marshal/internal/tools/native"
)

// runWithTimeout builds a context deadline from req.Timeout and returns it
// along with its cancel func. Callers should `defer cancel()`. A zero timeout
// inherits the parent context's deadline (no new deadline added).
func runWithTimeout(ctx context.Context, req native.CommandRequest) (context.Context, context.CancelFunc) {
	if req.Timeout > 0 {
		return context.WithTimeout(ctx, req.Timeout)
	}
	return ctx, func() {}
}

// killedReason returns a short string explaining why a command terminated
// early based on the error and context state.
func killedReason(err error, ctx context.Context) string {
	if err == nil {
		return ""
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout"
	}
	if ctx.Err() == context.Canceled {
		return "cancelled"
	}
	return err.Error()
}
