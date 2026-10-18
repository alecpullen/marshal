package schema

import "encoding/json"

// Reasoning is one step's reasoning: plain text and/or opaque provider items.
type Reasoning struct {
	Text  string
	Items []ReasoningItem
}

// ReasoningItem is an opaque, provider-bound reasoning payload. Only the
// adapter that produced Kind parses Data, and it is replayed only when
// Provider and Model match the request.
type ReasoningItem struct {
	Provider string
	Model    string
	Kind     string
	Data     json.RawMessage
}

// ReasoningKindOpenAI is a Responses API reasoning output item.
const ReasoningKindOpenAI = "openai.reasoning"

// Empty reports whether r carries nothing to replay.
func (r *Reasoning) Empty() bool {
	return r == nil || (r.Text == "" && len(r.Items) == 0)
}

// StripReasoning returns a copy of msgs with every Reasoning removed.
func StripReasoning(msgs []ChatMessage) []ChatMessage {
	out := make([]ChatMessage, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].Reasoning = nil
	}
	return out
}

// ReasoningItemsFor returns a copy of msgs keeping only the reasoning items
// produced by provider and model. Text is kept. A Reasoning left empty
// becomes nil.
func ReasoningItemsFor(msgs []ChatMessage, provider, model string) []ChatMessage {
	return filterItems(msgs, func(it ReasoningItem) bool { return it.Provider == provider && it.Model == model })
}

// WithoutReasoningItems returns a copy of msgs with every opaque item
// removed and text kept. A Reasoning left empty becomes nil.
func WithoutReasoningItems(msgs []ChatMessage) []ChatMessage {
	return filterItems(msgs, func(ReasoningItem) bool { return false })
}

func filterItems(msgs []ChatMessage, keep func(ReasoningItem) bool) []ChatMessage {
	out := make([]ChatMessage, len(msgs))
	copy(out, msgs)
	for i := range out {
		r := out[i].Reasoning
		if r == nil {
			continue
		}
		next := &Reasoning{Text: r.Text}
		for _, it := range r.Items {
			if keep(it) {
				next.Items = append(next.Items, it)
			}
		}
		if next.Empty() {
			next = nil
		}
		out[i].Reasoning = next
	}
	return out
}
