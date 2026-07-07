package agent

import (
	"strings"

	"marshal/internal/llm/schema"
)

const (
	toolResultPrefix = "Tool "
	compactedNote    = "\n\n[full output compacted to fit the context budget — re-run the tool if you need it again]"
)

func estimateTokens(messages []schema.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / 4
}

func compactMessages(messages []schema.ChatMessage, budgetTokens, keepRecent int) []schema.ChatMessage {
	if budgetTokens <= 0 {
		out := make([]schema.ChatMessage, len(messages))
		copy(out, messages)
		return out
	}
	if estimateTokens(messages) <= budgetTokens {
		out := make([]schema.ChatMessage, len(messages))
		copy(out, messages)
		return out
	}

	out := make([]schema.ChatMessage, len(messages))
	copy(out, messages)

	cutoff := len(out) - keepRecent
	if cutoff < 2 {
		cutoff = 2
	}

	// Phase 1: drop the oldest non-tool assistant/system/user messages first.
	// Tool results are the facts the model needs to reason about its work, so
	// they are preserved until prose compaction alone is insufficient. The
	// first two messages (system prompt + initial user goal) are always kept
	// so the model never loses its instructions or objective. An assistant
	// message carrying ToolCalls is treated as tool-related here too: zeroing
	// its ToolCalls would orphan the following role:tool results that
	// reference those tool_call_ids, which OpenAI-compatible providers reject.
	for i := 2; i < cutoff; i++ {
		if isToolResultMessage(out[i]) || len(out[i].ToolCalls) > 0 {
			continue
		}
		if estimateTokens(out) <= budgetTokens {
			return out
		}
		// Replace the prose message with an empty placeholder so message
		// ordering (and any tool_call_id pairing for role:tool results) is
		// preserved. A zero-length message costs nothing and keeps indices
		// stable for downstream compaction of tool results.
		out[i].Content = ""
		out[i].ToolCalls = nil
	}

	// Phase 2: only when dropping all old prose was still insufficient, compact
	// the oldest tool results (keeping the most recent keepRecent, which the
	// cutoff already excludes). Never compact a result that is already
	// compacted.
	for i := 2; i < cutoff; i++ {
		if estimateTokens(out) <= budgetTokens {
			return out
		}
		if !isToolResultMessage(out[i]) {
			continue
		}
		if strings.Contains(out[i].Content, compactedNote) {
			continue
		}
		firstLine := out[i].Content
		if idx := strings.Index(out[i].Content, "\n"); idx >= 0 {
			firstLine = out[i].Content[:idx]
		}
		out[i].Content = firstLine + compactedNote
	}
	return out
}

func isToolResultMessage(msg schema.ChatMessage) bool {
	return msg.Role == schema.RoleTool || strings.HasPrefix(msg.Content, toolResultPrefix)
}
