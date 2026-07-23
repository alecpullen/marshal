package agent

import (
	"context"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

// Rollover manages context-window archival and cross-turn generation rollover
// for the agent runner. A nil Rollover or nil Controller is a no-op for all
// methods, so existing runners without rollover support work unchanged.
type Rollover struct {
	Controller *rollover.Controller
	State      *session.State
	Cursor     int
}

// flushArchive writes only the new tail of wire (messages after Cursor) to
// the controller's archive store and advances the cursor. It is a no-op when
// Rollover or its Controller is nil, or when the cursor is already at the
// end of wire.
func (r *Rollover) flushArchive(ctx context.Context, wire []schema.ChatMessage) (int, error) {
	if r == nil {
		return 0, nil
	}
	if r.Controller == nil {
		return r.Cursor, nil
	}
	if r.Cursor >= len(wire) {
		return r.Cursor, nil
	}
	if err := r.Controller.Archive(ctx, wire[r.Cursor:]); err != nil {
		return r.Cursor, fmt.Errorf("flush archive: %w", err)
	}
	r.Cursor = len(wire)
	return r.Cursor, nil
}

// maybeRollover checks whether a generation rollover is due via
// Controller.Due. When due, it calls Controller.Rollover and updates
// session state with the new generation info. It is a no-op when Rollover
// or its Controller is nil, or when the rollover is not due.
func (r *Rollover) maybeRollover(ctx context.Context, wire []schema.ChatMessage, contextWindow int) (string, error) {
	if r == nil || r.Controller == nil {
		return "", nil
	}
	if !r.Controller.Due(ctx, wire, contextWindow) {
		return "", nil
	}
	h := rollover.GenerationHandle{
		SessionID: r.Controller.SessionID,
		Wire:      wire,
	}
	seedDigest, err := r.Controller.Rollover(ctx, h)
	if err != nil {
		return "", fmt.Errorf("maybe rollover: %w", err)
	}
	// Update session state with the new generation info (AC #3, Constraints).
	if r.State != nil {
		genID, genSeq, genSeed := r.Controller.Current()
		r.State.BeginGeneration(genID, genSeq, genSeed)
	}
	return seedDigest, nil
}

// compactContext archives the current wire messages and performs a rollover
// if due. When rollover is disabled (nil Controller), it falls back to
// summarizeAndContinue. Returns a short fresh window with the seed digest
// when rollover occurs, or the rebuilt message list from summarizeAndContinue
// when disabled.
func (r *Rollover) compactContext(ctx context.Context, runner *Runner, wire []schema.ChatMessage, contextWindow int, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error) {
	if r == nil || r.Controller == nil {
		if runner != nil {
			return runner.summarizeAndContinue(ctx, runner.Provider, runner.Model, wire, goal, responseFormat)
		}
		return nil, nil
	}
	if _, err := r.flushArchive(ctx, wire); err != nil {
		return nil, fmt.Errorf("compact context: %w", err)
	}
	seedDigest, err := r.maybeRollover(ctx, wire, contextWindow)
	if err != nil {
		return nil, fmt.Errorf("compact context: %w", err)
	}
	return []schema.ChatMessage{
		{Role: "system", Content: seedDigest},
	}, nil
}

// rolloverAndContinue is the unified intra-turn compaction entry point. When
// rollover is enabled it archives, optionally rolls over, and returns a short
// fresh window with the seed digest. When rollover is disabled it falls back
// to summarizeAndContinue.
func rolloverAndContinue(ctx context.Context, r *Runner, wire []schema.ChatMessage, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error) {
	if r == nil || r.Rollover == nil || r.Rollover.Controller == nil {
		return r.summarizeAndContinue(ctx, r.Provider, r.Model, wire, goal, responseFormat)
	}
	return r.Rollover.compactContext(ctx, r, wire, r.MaxTurnContextTokens, goal, responseFormat)
}

// Close ends the live generation on the controller. It is a no-op when
// Rollover or its Controller is nil.
func (r *Rollover) Close(ctx context.Context) error {
	if r == nil || r.Controller == nil {
		return nil
	}
	return r.Controller.Close(ctx)
}
