package agent

import (
	"context"
	"errors"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

// handoffSummaryDirective asks the model for a self-contained mid-task
// handoff. Adapted from crush's summary prompt: the summary is the ONLY
// context that survives, so vague next steps are useless.
const handoffSummaryDirective = `Summarize this conversation so the task can continue with no other context. This summary will be the ONLY context available afterwards, so be thorough and specific. Do not call tools; respond with plain text only, covering:

## Current State
The exact original request, what has been completed, what is in progress, and what remains.

## Files & Changes
Files modified (with what changed), files read and why they matter, and important file paths / line numbers.

## Technical Context
Commands that worked and commands that failed (exact commands), key decisions made and why, gotchas discovered, assumptions made.

## Exact Next Steps
Numbered, concrete steps with file paths — "3. Update parseAction in internal/agent/protocol.go to accept the actions array", not "continue implementing".`

var errEmptyHandoffSummary = errors.New("agent: handoff summarization returned empty text")

// summarizeAndContinue is the crush-style alternative to destructive
// compaction: request a handoff summary of the oversized transcript, then
// rebuild the working message list from scratch around it so the loop can
// keep working with full instructions intact.
func (r *Runner) summarizeAndContinue(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, goal string) ([]schema.ChatMessage, error) {
	req := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: handoffSummaryDirective})
	res, err := r.chatWithRetryNoNativeTools(ctx, p, model, req)
	if err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(res.Text)
	if summary == "" {
		return nil, errEmptyHandoffSummary
	}

	fresh := []schema.ChatMessage{
		BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools),
	}
	fresh = appendContextPackMessage(fresh, r.State.ContextPack())
	fresh = append(fresh,
		schema.ChatMessage{Role: schema.RoleUser, Content: goal},
		schema.ChatMessage{Role: schema.RoleAssistant, Content: "Progress summary (earlier transcript was compacted to fit the context budget):\n\n" + summary},
		schema.ChatMessage{Role: schema.RoleUser, Content: "Continue the task from that summary. Do not repeat work the summary marks as completed."},
	)

	r.State.AddMessage(session.RoleSystem, "Context compacted mid-turn; continuing from a progress summary.", session.ContentTypePlain)
	return fresh, nil
}
