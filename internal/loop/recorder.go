package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/store"
)

// LoopRecorder wraps a loop to persist rounds to the store.
type LoopRecorder struct {
	store *store.Store
	loop  *Loop
}

// NewLoopRecorder creates a recorder that persists rounds to the store.
func NewLoopRecorder(s *store.Store, l *Loop) *LoopRecorder {
	return &LoopRecorder{
		store: s,
		loop:  l,
	}
}

// Run executes the loop and persists all data to the store.
// Session metadata should be created before calling this.
func (r *LoopRecorder) Run(ctx context.Context, task string, session *store.Session) (*Result, error) {
	// Create the session record first
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := r.store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

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
		fmt.Printf("Warning: failed to update session: %v\n", updateErr)
	}

	return result, err
}

// RunWithRecording executes the loop and records each round.
func (r *LoopRecorder) RunWithRecording(ctx context.Context, task string, session *store.Session,
	roundCallback func(*Round) error) (*Result, error) {

	// Create session
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := r.store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Run loop
	result, err := r.loop.Run(ctx, task)

	// Update session
	session.UpdatedAt = time.Now()
	session.Status = result.Status
	if len(result.Rounds) > 0 {
		for _, round := range result.Rounds {
			session.PromptTokens += round.Tokens.PromptTokens
			session.CompletionTokens += round.Tokens.CompletionTokens
		}
	}

	if result.Status == "SUCCESS" || result.Status == "EXHAUSTED" {
		now := time.Now()
		session.CompletedAt = &now
	}

	// Record rounds
	for _, round := range result.Rounds {
		rr := &store.RoundRecord{
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
			CreatedAt:        session.CreatedAt,
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
