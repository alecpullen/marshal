// Package loop provides backward compatibility during the migration to marshal-centric architecture.
// The Loop type is now a thin wrapper around the Marshal orchestrator.
//
// DEPRECATED: New code should use internal/marshal directly.
package loop

import (
	"context"

	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/marshal"
	"github.com/alecpullen/marshal/internal/store"
)

// Loop is a backward-compatible wrapper around Marshal.
// It delegates all operations to the Marshal orchestrator.
//
// DEPRECATED: Use Marshal directly for new code.
type Loop struct {
	marshal *marshal.Marshal
}

// Round represents a single iteration of the loop.
// Kept for backward compatibility - maps to marshal.Round.
type Round struct {
	Number       int
	ExecutorReq  ExecutorRequest
	ExecutorResp string
	Diff         string
	CriticResp   string
	Verdict      Verdict
	Tokens       TokenUsage
	ThinkBlock   string // Extracted R1 think-block content (if any)
}

// Result is the final outcome of the loop.
type Result struct {
	Status       string // "SUCCESS" | "FAILED" | "EXHAUSTED"
	FinalVerdict *Verdict
	Rounds       []Round
	TotalTokens  TokenUsage
}

// TokenUsage tracks prompt and completion tokens for a round.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// RoundCallback is called after each round completes.
type RoundCallback func(round Round)

// New creates a new Loop instance that delegates to Marshal.
func New(cfg *config.Config, gitLayer git.Layer, s *store.Store, skills []Skill) *Loop {
	return &Loop{
		marshal: marshal.New(cfg, gitLayer, s, skills),
	}
}

// Run executes the full loop until completion or exhaustion.
// Delegates to Marshal.ExecuteTask().
func (l *Loop) Run(ctx context.Context, task string) (*Result, error) {
	result, err := l.marshal.ExecuteTask(ctx, task)
	if err != nil {
		return nil, err
	}

	// Convert marshal.Result to loop.Result
	return convertResult(result), nil
}

// RunWithCallback executes the loop with a callback after each round.
// Note: Callback is not called during marshal execution in this simplified version.
func (l *Loop) RunWithCallback(ctx context.Context, task string, callback RoundCallback) (*Result, error) {
	// For backward compatibility, we just run without callback support
	// Full callback support would require extending the marshal package
	return l.Run(ctx, task)
}

// convertResult converts a marshal.Result to a loop.Result.
func convertResult(mr *marshal.Result) *Result {
	if mr == nil {
		return nil
	}

	var finalVerdict *Verdict
	if mr.FinalVerdict != nil {
		finalVerdict = &Verdict{
			Verdict:  mr.FinalVerdict.Verdict,
			Summary:  mr.FinalVerdict.Summary,
			Issue:    mr.FinalVerdict.Issue,
			Fix:      mr.FinalVerdict.Fix,
			Concerns: mr.FinalVerdict.Concerns,
		}
	}

	rounds := make([]Round, len(mr.Rounds))
	for i, mr := range mr.Rounds {
		rounds[i] = Round{
			Number:       mr.Number,
			ExecutorReq:  ExecutorRequest{Task: mr.ExecutorReq.Task, PriorFeedback: mr.ExecutorReq.PriorFeedback},
			ExecutorResp: mr.ExecutorResp,
			Diff:         mr.Diff,
			CriticResp:   mr.CriticResp,
			Verdict: Verdict{
				Verdict:  mr.Verdict.Verdict,
				Summary:  mr.Verdict.Summary,
				Issue:    mr.Verdict.Issue,
				Fix:      mr.Verdict.Fix,
				Concerns: mr.Verdict.Concerns,
			},
			Tokens: TokenUsage{
				PromptTokens:     mr.Tokens.PromptTokens,
				CompletionTokens: mr.Tokens.CompletionTokens,
			},
			ThinkBlock: mr.ThinkBlock,
		}
	}

	return &Result{
		Status:       mr.Status,
		FinalVerdict: finalVerdict,
		Rounds:       rounds,
		TotalTokens: TokenUsage{
			PromptTokens:     mr.TotalTokens.PromptTokens,
			CompletionTokens: mr.TotalTokens.CompletionTokens,
		},
	}
}

// ExtractThinkBlock provides backward compatibility for think-block extraction.
// Delegates to marshal.ExtractThinkBlock().
func ExtractThinkBlock(content string) (think string, cleaned string) {
	return marshal.ExtractThinkBlock(content)
}

// IsPass provides backward compatibility for verdict checking.
func IsPass(v *Verdict) bool {
	if v == nil {
		return false
	}
	return v.Verdict == "PASS"
}
