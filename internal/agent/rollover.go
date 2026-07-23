package agent

import (
	"context"
	"fmt"

	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

// Rollover manages context-window archival and cross-turn generation rollover
// for the agent runner. A nil Rollover or nil Controller is a no-op for all
// methods, so existing runners without rollover support work unchanged.
type Rollover struct {
	Controller *rollover.Controller
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
// Controller.Due. When due, it calls Controller.Rollover and returns the
// seed digest. The caller is responsible for updating session state with
// the new generation info. It is a no-op when Rollover or its Controller is
// nil, or when the rollover is not due.
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
	return seedDigest, nil
}

// compactContext archives the current wire messages and performs a rollover
// if due. It is called from the overflow branch in runner.go. Returns the
// seed digest if a rollover occurred, or "" if not.
func (r *Rollover) compactContext(ctx context.Context, wire []schema.ChatMessage, contextWindow int) (string, error) {
	if _, err := r.flushArchive(ctx, wire); err != nil {
		return "", err
	}
	return r.maybeRollover(ctx, wire, contextWindow)
}

// Close ends the live generation on the controller. It is a no-op when
// Rollover or its Controller is nil.
func (r *Rollover) Close(ctx context.Context) error {
	if r == nil || r.Controller == nil {
		return nil
	}
	return r.Controller.Close(ctx)
}
