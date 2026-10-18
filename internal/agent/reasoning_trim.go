package agent

import "marshal/internal/llm/schema"

// trimReplayedReasoning clears Reasoning on whole assistant messages,
// oldest first, until over reports false. The most recent assistant
// message keeps its reasoning. It returns the new slice and how many
// messages it trimmed; the input slice is not modified.
func trimReplayedReasoning(msgs []schema.ChatMessage, over func([]schema.ChatMessage) bool) ([]schema.ChatMessage, int) {
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == schema.RoleAssistant {
			last = i
			break
		}
	}
	out := make([]schema.ChatMessage, len(msgs))
	copy(out, msgs)
	trimmed := 0
	for i := range out {
		if !over(out) {
			break
		}
		if i == last || out[i].Role != schema.RoleAssistant || out[i].Reasoning == nil {
			continue
		}
		out[i].Reasoning = nil
		trimmed++
	}
	return out, trimmed
}
