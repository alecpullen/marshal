package agent

import (
	"unicode/utf8"

	"marshal/internal/llm/schema"
)

// estimateTokens approximates prompt size as runes/4. Tool-call payloads
// (name, ID, args) travel on the wire exactly like content, so they count
// too — ignoring them systematically underestimates native-mode turns and
// delays compaction. Args are JSON and ASCII-dominant, so byte length is a
// fair rune proxy there.
func estimateTokens(messages []schema.ChatMessage) int {
	runes := 0
	for _, m := range messages {
		runes += utf8.RuneCountInString(m.Content)
		runes += utf8.RuneCountInString(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			runes += utf8.RuneCountInString(tc.ID)
			runes += utf8.RuneCountInString(tc.Name)
			runes += len(tc.Args)
		}
	}
	return runes / 4
}
