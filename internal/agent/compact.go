package agent

import (
	"unicode/utf8"

	"marshal/internal/llm/schema"
)

func estimateTokens(messages []schema.ChatMessage) int {
	runes := 0
	for _, m := range messages {
		runes += utf8.RuneCountInString(m.Content)
	}
	return runes / 4
}
