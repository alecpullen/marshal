package agent

import "marshal/internal/llm/schema"

func estimateTokens(messages []schema.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / 4
}
