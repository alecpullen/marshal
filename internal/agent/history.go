package agent

import (
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

// defaultHistoryBudgetTokens caps how much prior-turn conversation is
// replayed into a new turn (4-chars-per-token heuristic, same as
// estimateTokens in compact.go).
const defaultHistoryBudgetTokens = 8000

// buildHistoryMessages converts prior session transcript entries into chat
// messages so the model remembers earlier turns. Only user turns and final
// (non-salvaged) assistant answers are replayed: intermediate reasoning,
// plans, system notices, and salvaged fallbacks are noise or unreliable.
// When the total exceeds maxTokens, the oldest turns are dropped first.
func buildHistoryMessages(prior []session.Message, maxTokens int) []schema.ChatMessage {
	if maxTokens <= 0 {
		maxTokens = defaultHistoryBudgetTokens
	}

	var candidates []schema.ChatMessage
	for _, m := range prior {
		switch m.Role {
		case session.RoleUser:
			candidates = append(candidates, schema.ChatMessage{Role: schema.RoleUser, Content: m.Content})
		case session.RoleAssistant:
			if m.Final && !m.Salvaged {
				candidates = append(candidates, schema.ChatMessage{Role: schema.RoleAssistant, Content: m.Content})
			}
		}
	}

	// Walk backwards accumulating until the budget is spent, then restore order.
	budget := maxTokens * 4 // chars
	total := 0
	start := len(candidates)
	for i := len(candidates) - 1; i >= 0; i-- {
		total += len(candidates[i].Content)
		if total > budget {
			break
		}
		start = i
	}
	return candidates[start:]
}
