// Package store provides SQLite persistence for Marshal sessions.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/loop"
)

// LoopRecorder wraps a loop to persist rounds to the store.
type LoopRecorder struct {
	store *Store
	loop  *loop.Loop
}

// NewLoopRecorder creates a recorder that persists rounds to the store.
func NewLoopRecorder(s *Store, l *loop.Loop) *LoopRecorder {
	return &LoopRecorder{
		store: s,
		loop:  l,
	}
}

// Run executes the loop and persists all data to the store.
// Session metadata should be created before calling this.
func (r *LoopRecorder) Run(ctx context.Context, task string, session *Session) (*loop.Result, error) {
	// Create the session record first
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := r.store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// We need to access the loop's internal state to record rounds
	// Since we can't easily hook into the loop mid-execution,
	// we'll need to modify the approach or have the loop call us back

	// For now, run the loop and record the result at the end
	// Full round-by-round recording requires loop modifications
	result, err := r.loop.Run(ctx, task)

	// Update session with final status
	session.UpdatedAt = time.Now()
	session.Status = result.Status
	if result.TotalTokens.PromptTokens > 0 {
		session.PromptTokens = result.TotalTokens.PromptTokens
	}
	if result.TotalTokens.CompletionTokens > 0 {
		session.CompletionTokens = result.TotalTokens.CompletionTokens
	}

	// Set completed_at for terminal states
	if result.Status == "SUCCESS" || result.Status == "EXHAUSTED" {
		now := time.Now()
		session.CompletedAt = &now
	}

	if updateErr := r.store.UpdateSession(session); updateErr != nil {
		// Log but don't fail the original error
		fmt.Printf("Warning: failed to update session: %v\n", updateErr)
	}

	return result, err
}

// RunWithRecording executes the loop and records each round.
// This is a placeholder for full round-by-round recording.
// To implement this fully, the loop would need to accept callbacks.
func (r *LoopRecorder) RunWithRecording(ctx context.Context, task string, session *Session,
	roundCallback func(*loop.Round) error) (*loop.Result, error) {

	// Create session
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := r.store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Run loop (without per-round recording for now)
	result, err := r.loop.Run(ctx, task)

	// Update session
	session.UpdatedAt = time.Now()
	session.Status = result.Status
	if len(result.Rounds) > 0 {
		// Aggregate tokens from rounds
		for _, round := range result.Rounds {
			session.PromptTokens += round.Tokens.PromptTokens
			session.CompletionTokens += round.Tokens.CompletionTokens
		}
	}

	if result.Status == "SUCCESS" || result.Status == "EXHAUSTED" {
		now := time.Now()
		session.CompletedAt = &now
	}

	// Record rounds if we have them
	for _, round := range result.Rounds {
		rr := &RoundRecord{
			SessionID:        session.ID,
			RoundNumber:      round.Number,
			ExecutorRequest:  round.ExecutorReq.Task,
			ExecutorResponse: round.ExecutorResp,
			Diff:             round.Diff,
			Verdict:          round.Verdict.Verdict,
			Summary:          round.Verdict.Summary,
			Issue:            round.Verdict.Issue,
			Fix:              round.Verdict.Fix,
			Concerns:         round.Verdict.Concerns,
			PromptTokens:     round.Tokens.PromptTokens,
			CompletionTokens: round.Tokens.CompletionTokens,
			CreatedAt:        session.CreatedAt, // Approximate
		}
		if createErr := r.store.CreateRound(rr); createErr != nil {
			fmt.Printf("Warning: failed to record round %d: %v\n", round.Number, createErr)
		}
	}

	if updateErr := r.store.UpdateSession(session); updateErr != nil {
		fmt.Printf("Warning: failed to update session: %v\n", updateErr)
	}

	return result, err
}
