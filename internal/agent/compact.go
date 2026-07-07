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

	for i := 2; i < cutoff; i++ {
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
		if estimateTokens(out) <= budgetTokens {
			return out
		}
	}
	return out
}

func isToolResultMessage(msg schema.ChatMessage) bool {
	return msg.Role == schema.RoleTool || strings.HasPrefix(msg.Content, toolResultPrefix)
}
