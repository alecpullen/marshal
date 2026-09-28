// Package oauthworker keeps OAuth-backed provider tokens fresh in the
// background.
//
// The provider backends already refresh inline when a token is expired
// (Engine.TokenSource), so this worker is not required for correctness. Its
// job is to refresh *before* the token expires, so a long-running session
// never pays the refresh latency mid-turn and never races a request against
// an expiry boundary. The engine mutex serializes the two paths, so a
// proactive refresh and an inline one cannot both run.
package oauthworker

import (
	"context"
	"log/slog"
	"time"

	"marshal/internal/oauth"
)

// noTokenRetryDelay bounds how often the worker re-checks for a token when
// none is stored yet. A user who has not logged in should not cause a busy
// loop, but should also not wait long after logging in.
const noTokenRetryDelay = 30 * time.Second

// refreshFraction is the fallback schedule when the server sends no
// earliest_refresh_at hint: refresh at this fraction of the token's
// lifetime. 0.8 leaves a fifth of the lifetime as headroom for clock skew
// and a slow refresh round-trip.
const refreshFraction = 0.8

// minRefreshDelay floors the computed wait so a token that is already past
// its refresh point does not spin the loop.
const minRefreshDelay = time.Second

// failureRetryDelay is how long the worker waits after a failed refresh
// before trying again.
//
// Without it, a transient failure (network blip, 5xx) would retry at
// minRefreshDelay: the stored token is still expired, so the schedule
// recomputes to the 1s floor and the worker hammers the token endpoint once
// a second for the whole outage. That is both wasteful and, against a
// subscription endpoint, the kind of traffic that draws attention.
const failureRetryDelay = 5 * time.Minute

// Worker proactively refreshes one OAuth provider's token.
type Worker struct {
	// Engine is the provider's OAuth engine. Required.
	Engine *oauth.Engine
	// Provider is the provider entry name, used in logs.
	Provider string
	// WorkerName is the worker name reported to the supervisor. It is not
	// called Name because Name is the interface method.
	WorkerName string
	// Logger is optional; slog.Default() is used when nil.
	Logger *slog.Logger
	// Now is an injectable clock for tests. Nil means time.Now.
	Now func() time.Time
	// Sleep is an injectable timer for tests. Nil means a real timer.
	// It must return false when ctx is cancelled before d elapses.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// Name implements worker.Worker.
func (w *Worker) Name() string {
	if w.WorkerName != "" {
		return w.WorkerName
	}
	return "oauth-refresh-" + w.Provider
}

func (w *Worker) log() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}

func (w *Worker) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

// sleep waits for d, returning false when ctx was cancelled first.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run implements worker.Worker. It blocks until ctx is cancelled, refreshing
// the token each time the schedule comes due. It returns nil on clean
// shutdown; a failed refresh is logged and retried on the next cycle rather
// than terminating the worker, because a transient network failure must not
// take down a session that can still refresh inline.
func (w *Worker) Run(ctx context.Context) error {
	if w.Engine == nil {
		return nil
	}
	for {
		if ctx.Err() != nil {
			return nil
		}

		entry, err := w.Engine.LoadToken(ctx)
		if err != nil {
			w.log().Warn("oauth worker: cannot read stored token", "provider", w.Provider, "err", err)
			if !w.sleep(ctx, noTokenRetryDelay) {
				return nil
			}
			continue
		}
		if entry == nil {
			// No token yet: the user has not logged in. Wait and re-check.
			if !w.sleep(ctx, noTokenRetryDelay) {
				return nil
			}
			continue
		}

		if !w.sleep(ctx, w.nextRefreshDelay(entry)) {
			return nil
		}

		// The engine mutex serializes this against any concurrent
		// chat-time refresh, so the loser re-reads rather than
		// double-refreshing.
		if _, err := w.Engine.ForceRefresh(ctx); err != nil {
			w.log().Warn("oauth worker: refresh failed", "provider", w.Provider, "err", err)
			// Back off rather than recomputing the schedule: the stored
			// token is still expired, so the schedule would return the 1s
			// floor and retry immediately.
			if !w.sleep(ctx, failureRetryDelay) {
				return nil
			}
		}
	}
}

// nextRefreshDelay computes how long to wait before refreshing.
//
// The server's earliest_refresh_at hint wins when present and still in the
// future. A hint already in the past means "refresh now" — floored to
// minRefreshDelay so the loop cannot spin. With no hint, the delay is
// refreshFraction of the token's lifetime.
func (w *Worker) nextRefreshDelay(entry *oauth.StoredTokens) time.Duration {
	now := w.now()

	if hint := oauth.ParseEarliestRefresh(entry.EarliestRefreshAt); !hint.IsZero() {
		if d := hint.Sub(now); d > minRefreshDelay {
			return d
		}
		return minRefreshDelay
	}

	// No hint: schedule from the token's own lifetime. A zero IssuedAt
	// means the lifetime is unknown — computing ExpiresAt.Sub(zeroTime)
	// would yield a span of centuries and schedule the refresh in the
	// distant past, hot-looping against the token endpoint.
	if entry.IssuedAt.IsZero() {
		if d := entry.ExpiresAt.Sub(now); d > minRefreshDelay {
			return d
		}
		return minRefreshDelay
	}
	lifetime := entry.ExpiresAt.Sub(entry.IssuedAt)
	if lifetime <= 0 {
		// A nonsensical lifetime (expiry before issue). Fall back to the
		// remaining time, or refresh now if that is also unknown.
		if d := entry.ExpiresAt.Sub(now); d > minRefreshDelay {
			return d
		}
		return minRefreshDelay
	}
	target := entry.IssuedAt.Add(time.Duration(float64(lifetime) * refreshFraction))
	if d := target.Sub(now); d > minRefreshDelay {
		return d
	}
	return minRefreshDelay
}
