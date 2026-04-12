package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/prompts"
)

// roundResult records the outcome of a single executor round for use by the
// compactor when the task has been retried enough times to warrant synthesis.
type roundResult struct {
	Round int
	Issue string
	Fix   string
	Diff  string // git diff produced in this round
}

// compactorOutputInstructions are appended to the compactor user message.
const compactorOutputInstructions = `Respond with ONLY this JSON object (no markdown, no prose):
{"issue":"one sentence describing the root cause","fix":"one sentence describing the best fix direction"}`

// compact calls the compactor model with the full history of failed attempts
// and returns a synthesized (issue, fix) pair.
// It is called after compact_after consecutive FAIL rounds.
func (e *Engine) compact(ctx context.Context, compactorB backend.Backend, prompt string, history []roundResult) (issue, fix string, err error) {
	msgs := e.buildCompactorMessages(prompt, history)
	content, _, _, err := e.callBackend(ctx, compactorB, msgs)
	if err != nil {
		return "", "", fmt.Errorf("compactor: %w", err)
	}

	// Parse the JSON response.
	var resp struct {
		Issue string `json:"issue"`
		Fix   string `json:"fix"`
	}
	// Strip any accidental think blocks before parsing.
	clean := stripThinkBlocks(content)
	if err := json.Unmarshal([]byte(strings.TrimSpace(clean)), &resp); err != nil {
		// Non-fatal: fall back to using the raw content as the issue.
		return strings.TrimSpace(clean), "address the issue described above", nil
	}
	if resp.Issue == "" {
		return "", "", fmt.Errorf("compactor returned empty issue")
	}
	return resp.Issue, resp.Fix, nil
}

// buildCompactorMessages assembles the compactor prompt from the round history.
func (e *Engine) buildCompactorMessages(prompt string, history []roundResult) []backend.Message {
	var sb strings.Builder
	sb.WriteString("Original task: ")
	sb.WriteString(prompt)
	sb.WriteString("\n\nThis task has failed ")
	sb.WriteString(fmt.Sprintf("%d", len(history)))
	sb.WriteString(" time(s). Here is the full history of attempts:\n\n")

	for _, r := range history {
		sb.WriteString(fmt.Sprintf("--- Round %d ---\n", r.Round))
		if r.Issue != "" {
			sb.WriteString("Issue identified: ")
			sb.WriteString(r.Issue)
			sb.WriteString("\n")
		}
		if r.Fix != "" {
			sb.WriteString("Fix attempted: ")
			sb.WriteString(r.Fix)
			sb.WriteString("\n")
		}
		if r.Diff != "" {
			sb.WriteString("Changes made:\n```diff\n")
			sb.WriteString(r.Diff)
			sb.WriteString("\n```\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(compactorOutputInstructions)

	sysPrompt := prompts.Assemble(prompts.Compactor, "")
	return []backend.Message{
		{Role: backend.MessageRoleSystem, Content: sysPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

// stripThinkBlocks removes <think>...</think> content before JSON parsing.
func stripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</think>")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</think>"):]
	}
	return s
}
