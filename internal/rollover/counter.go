// Package rollover provides context-window rollover logic for long
// conversations. This file defines the token counter abstractions used by the
// rollover controller.
package rollover

import (
	"context"
	"sync/atomic"
	"unicode/utf8"

	"marshal/internal/llm/schema"
)

// TokenCounter counts tokens in a slice of chat messages.
type TokenCounter interface {
	// Name returns a human-readable name for this counter (e.g. "estimator", "usage").
	Name() string

	// CountTokens returns the estimated or observed token count for the given
	// messages. The implementation determines whether the count is a heuristic
	// estimate or a provider-reported value.
	CountTokens(ctx context.Context, wire []schema.ChatMessage) (int, error)
}

// EstimatorCounter implements TokenCounter using Marshal's existing
// 4-chars-per-token heuristic. It sums the UTF-8 rune count of all message
// content and divides by 4 (ceiling).
type EstimatorCounter struct{}

// Name returns "estimator".
func (EstimatorCounter) Name() string { return "estimator" }

// CountTokens returns the estimated token count for the given messages using
// the 4-chars-per-token heuristic. It sums the Content field of every message
// and applies the same ceiling division as EstimateTokens in the contextpack
// package.
func (EstimatorCounter) CountTokens(_ context.Context, wire []schema.ChatMessage) (int, error) {
	var total int
	for _, msg := range wire {
		total += utf8.RuneCountInString(msg.Content)
	}
	if total == 0 {
		return 0, nil
	}
	return (total + 3) / 4, nil
}

// UsageCounter implements TokenCounter by preferring the larger of the
// provider-observed prompt-token count and the estimator's heuristic count.
// This ensures the rollover controller does not underestimate the true token
// usage when the provider reports a higher count than the estimator.
type UsageCounter struct {
	estimator EstimatorCounter
	observed  atomic.Int64
}

// NewUsageCounter returns a UsageCounter initialised with zero observed
// tokens, so it falls back to the estimator until the first Observe call.
func NewUsageCounter() *UsageCounter {
	return &UsageCounter{}
}

// Name returns "usage".
func (c *UsageCounter) Name() string { return "usage" }

// Observe records a provider-reported prompt-token count. The next call to
// CountTokens will consider this value alongside the estimator.
func (c *UsageCounter) Observe(promptTokens int) {
	c.observed.Store(int64(promptTokens))
}

// CountTokens returns the larger of the observed provider token count and the
// estimator's heuristic count. If no observation has been made (observed is
// zero), the estimator value is returned directly.
func (c *UsageCounter) CountTokens(ctx context.Context, wire []schema.ChatMessage) (int, error) {
	est, err := c.estimator.CountTokens(ctx, wire)
	if err != nil {
		return 0, err
	}
	obs := int(c.observed.Load())
	if obs > est {
		return obs, nil
	}
	return est, nil
}

// ResolveCounter returns a TokenCounter for the given name. When name is
// "usage" and usage is non-nil, it returns the usage counter (which prefers
// provider-observed counts). For any other name (or a nil usage), it returns
// a plain EstimatorCounter.
func ResolveCounter(name string, usage *UsageCounter) TokenCounter {
	if name == "usage" && usage != nil {
		return usage
	}
	return EstimatorCounter{}
}
