// Package backend defines the Backend interface and shared types for all
// model provider implementations.
package backend

import (
	"context"
	"encoding/json"
)

// --- Message types -----------------------------------------------------------

// MessageRole is the role field of a chat message.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// Message is a single entry in a chat-completion message array.
type Message struct {
	Role       MessageRole `json:"role"`
	Content    string      `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

// ToolCall is a model's request to invoke a function tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the name and (partial) JSON arguments for a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string; may be partial during streaming
}

// --- Tool definitions --------------------------------------------------------

// Tool describes a callable function the model may invoke.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the schema for a callable tool.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolChoice controls how the model picks tools.
// Use nil for default, ToolChoiceNone / ToolChoiceAuto / ToolChoiceRequired as
// string values, or a SpecificToolChoice struct for a forced function call.
type ToolChoice = interface{}

const (
	ToolChoiceNone     = "none"
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"
)

// SpecificToolChoice forces the model to call a named function.
type SpecificToolChoice struct {
	Type     string                  `json:"type"` // "function"
	Function SpecificToolChoiceField `json:"function"`
}

type SpecificToolChoiceField struct {
	Name string `json:"name"`
}

// --- Response format ---------------------------------------------------------

// ResponseFormat controls the output format of the completion.
type ResponseFormat struct {
	Type string `json:"type"` // "text" | "json_object"
}

// --- Request / Response / Chunk ----------------------------------------------

// Request is a model-agnostic chat-completion request.
type Request struct {
	Messages       []Message
	MaxTokens      int
	Temperature    float64
	ResponseFormat *ResponseFormat
	Tools          []Tool
	ToolChoice     ToolChoice
}

// Usage reports token consumption for a request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Response is a complete (non-streaming) completion result.
type Response struct {
	Content      string
	FinishReason string
	Usage        Usage
	ToolCalls    []ToolCall
}

// Chunk is a streaming delta emitted on the channel returned by Backend.Stream.
// After a Chunk with non-nil Err the channel is closed.
type Chunk struct {
	Content      string
	ToolCalls    []ToolCall // partial; Arguments accumulate across deltas
	FinishReason string
	Err          error
}

// --- Backend interface -------------------------------------------------------

// Backend is the provider-agnostic interface every model client implements.
type Backend interface {
	// Complete performs a blocking, non-streaming completion.
	Complete(ctx context.Context, req Request) (Response, error)

	// Stream starts a streaming completion and returns a channel of token
	// deltas. The channel is closed when the stream ends or an error occurs.
	// A Chunk with non-nil Err signals a terminal error; the channel is
	// closed immediately after.
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)

	// TokenCount estimates the token cost of messages using the backend's
	// native tokeniser (or a char-based heuristic for non-OpenAI families).
	TokenCount(messages []Message) (int, error)

	// SupportsTools reports whether the model accepts tool definitions.
	SupportsTools() bool

	// SupportsJSONMode reports whether the model accepts response_format=json_object.
	SupportsJSONMode() bool

	// Model returns the model string the backend was configured with.
	Model() string
}
