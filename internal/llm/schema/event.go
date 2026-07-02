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

type ChatEvent struct {
	Type ChatEventType

	// Delta holds incremental (or, for non-streaming, complete) assistant
	// text content. Populated only when Type == ChatEventDelta.
	Delta string

	// FinishReason mirrors the upstream `finish_reason` field ("stop",
	// "length", etc.) when known. Populated only when Type == ChatEventDone.
	FinishReason string

	// Err is populated only when Type == ChatEventError. The channel is
	// always closed immediately after an error event.
	Err error
}
