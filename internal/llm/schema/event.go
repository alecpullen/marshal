package schema

// ChatEventType discriminates the three shapes a ChatEvent can take. Both
// streaming (SSE) and non-streaming (single JSON body) provider responses
// are normalized into the same event stream: non-streaming responses are
// synthesized as exactly one Delta (the full content) followed by one Done.
type ChatEventType string

const (
	ChatEventDelta ChatEventType = "delta"
	ChatEventDone  ChatEventType = "done"
	ChatEventError ChatEventType = "error"
)

// DeltaKind distinguishes the model's free-form reasoning/thinking narration
// (DeltaThinking) from its normal output (DeltaAnswer), when a provider
// exposes the two as separate channels — e.g. the `reasoning_content` field
// emitted by DeepSeek-R1-style reasoning models over an OpenAI-compatible
// streaming API. DeltaAnswer is the zero value, so providers/models that
// never populate a reasoning channel are unaffected: every ChatEvent they
// emit defaults to DeltaAnswer exactly as before this field existed.
type DeltaKind int

const (
	DeltaAnswer DeltaKind = iota
	DeltaThinking
)

type ChatEvent struct {
	Type ChatEventType

	// Kind discriminates Delta as answer text vs. reasoning/thinking text.
	// Populated only when Type == ChatEventDelta.
	Kind DeltaKind

	// Delta holds incremental (or, for non-streaming, complete) assistant
	// text content. Populated only when Type == ChatEventDelta.
	Delta string

	// FinishReason mirrors the upstream `finish_reason` field ("stop",
	// "length", etc.) when known. Populated only when Type == ChatEventDone.
	FinishReason string

	// Err is populated only when Type == ChatEventError. The channel is
	// always closed immediately after an error event.
	Err error

	// Usage is populated only when Type == ChatEventDone and token counts are known.
	Usage *TokenUsage

	// ToolCalls is populated only when Type == ChatEventDone and the provider
	// returned complete native tool calls.
	ToolCalls []ToolCall
}

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
