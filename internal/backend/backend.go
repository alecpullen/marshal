package backend

import (
	"context"

	"github.com/alecpullen/marshal/internal/tools"
)

// Message is a single turn in a conversation sent to a backend.
// ToolCallID and ToolCalls are optional; zero values are omitted from JSON.
type Message struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"` // role="tool": links result to a call
	ToolCalls  []tools.Call `json:"tool_calls,omitempty"`   // role="assistant": model-initiated calls
}

type Response struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	CacheHit         bool
	CachedTokens     int
}

type Backend interface {
	Complete(ctx context.Context, model string, messages []Message) (Response, error)
	Name() string
}

// StreamResponse is called for each chunk of a streaming response
type StreamResponse struct {
	Content string
	Done    bool
}

// AsStreaming returns the backend as StreamingBackend if it implements it.
// Returns nil if the backend does not support streaming.
func AsStreaming(be Backend) StreamingBackend {
	if sb, ok := be.(StreamingBackend); ok {
		return sb
	}
	return nil
}

// StreamingBackend extends Backend with streaming capabilities.
// Not all backends support streaming - this is an optional extension.
type StreamingBackend interface {
	Backend
	// CompleteStreaming calls onChunk for each content chunk as it arrives.
	// The onChunk callback may be called many times during a single request.
	CompleteStreaming(ctx context.Context, model string, messages []Message, onChunk func(StreamResponse)) error
}

// ToolCapableBackend extends Backend with tool-call support.
// Use AsToolCapable to safely probe for this capability at runtime.
type ToolCapableBackend interface {
	Backend
	// CompleteWithTools sends messages along with tool definitions.
	// The returned Response may contain ToolCalls (model wants to invoke tools)
	// or Content (model produced a final answer).
	CompleteWithTools(ctx context.Context, model string, messages []Message, toolDefs []tools.Definition) (tools.Response, error)
}

// AsToolCapable returns the backend as ToolCapableBackend if supported, nil otherwise.
// Mirrors the AsStreaming pattern.
func AsToolCapable(be Backend) ToolCapableBackend {
	if tb, ok := be.(ToolCapableBackend); ok {
		return tb
	}
	return nil
}
