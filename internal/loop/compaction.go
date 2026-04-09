package loop

import (
	"context"
	"fmt"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
)

// Compactor summarizes conversation history to manage token budget.
type Compactor struct {
	backend backend.Backend
	cfg     config.AgentConfig
}

// CompactionResult contains the summary and metadata.
type CompactionResult struct {
	Summary       string
	KeptRounds    []int
	DroppedRounds []int
	TokensSaved   int // Estimated token savings
}

// NewCompactor creates a new compactor.
func NewCompactor(backend backend.Backend, cfg config.AgentConfig) *Compactor {
	return &Compactor{
		backend: backend,
		cfg:     cfg,
	}
}

// Compact generates a summary of the conversation history.
func (c *Compactor) Compact(ctx context.Context, history []Round) (*CompactionResult, error) {
	if len(history) == 0 {
		return &CompactionResult{
			Summary:       "",
			KeptRounds:    []int{},
			DroppedRounds: []int{},
		}, nil
	}

	// Build the history summary for the prompt
	historyText := ""
	for _, round := range history {
		historyText += fmt.Sprintf("\nRound %d:\n", round.Number)
		historyText += fmt.Sprintf("- Task: %s\n", truncate(round.ExecutorReq.Task, 100))
		historyText += fmt.Sprintf("- Verdict: %s\n", round.Verdict.Verdict)
		if round.Verdict.Issue != "" {
			historyText += fmt.Sprintf("- Issue: %s\n", truncate(round.Verdict.Issue, 100))
		}
	}

	prompt := fmt.Sprintf(`Summarize the following conversation history, preserving essential context:

%s

Provide a concise summary that captures:
1. The original task
2. Key decisions made so far
3. Current state of the code
4. Outstanding issues from the last critic review

This summary will be used as context for continuing the conversation.`, historyText)

	messages := []backend.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := c.backend.Complete(ctx, c.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	// Determine which rounds to keep/drop
	// For now, keep all rounds but note that compaction occurred
	kept := make([]int, len(history))
	dropped := []int{}
	for i, round := range history {
		kept[i] = round.Number
	}

	return &CompactionResult{
		Summary:       resp.Content,
		KeptRounds:    kept,
		DroppedRounds: dropped,
	}, nil
}

// ShouldCompact returns true if compaction should be triggered.
func (c *Compactor) ShouldCompact(round int, compactAfter int) bool {
	return round >= compactAfter
}

// CompactAndDrop generates a summary and actually drops old rounds.
// It keeps the last `keepRecent` rounds and summarizes the rest.
func (c *Compactor) CompactAndDrop(ctx context.Context, history []Round, keepRecent int) (*CompactionResult, error) {
	if len(history) <= keepRecent {
		// Nothing to drop
		kept := make([]int, len(history))
		for i, round := range history {
			kept[i] = round.Number
		}
		return &CompactionResult{
			Summary:       "",
			KeptRounds:    kept,
			DroppedRounds: []int{},
			TokensSaved:   0,
		}, nil
	}

	// Calculate what to drop vs keep
	dropCount := len(history) - keepRecent
	toDrop := history[:dropCount]
	toKeep := history[dropCount:]

	// Generate summary of what we're dropping
	result, err := c.Compact(ctx, toDrop)
	if err != nil {
		return nil, err
	}

	// Estimate token savings (rough approximation)
	tokensSaved := 0
	for _, round := range toDrop {
		tokensSaved += round.Tokens.PromptTokens + round.Tokens.CompletionTokens
	}
	result.TokensSaved = tokensSaved

	// Update kept/dropped lists based on actual split
	result.DroppedRounds = make([]int, len(toDrop))
	for i, round := range toDrop {
		result.DroppedRounds[i] = round.Number
	}
	result.KeptRounds = make([]int, len(toKeep))
	for i, round := range toKeep {
		result.KeptRounds[i] = round.Number
	}

	return result, nil
}
