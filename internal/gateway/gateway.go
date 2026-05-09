// Package gateway provides a unified interface for multiple model providers
// (Anthropic, OpenAI, Ollama, etc.) with normalization at the adapter boundary.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
)

// --- Content Block Types (Canonical Internal Format) ---

// ContentBlockType identifies the type of content block.
type ContentBlockType string

const (
	ContentBlockTypeText       ContentBlockType = "text"
	ContentBlockTypeToolUse    ContentBlockType = "tool_use"
	ContentBlockTypeToolResult ContentBlockType = "tool_result"
	ContentBlockTypeImage      ContentBlockType = "image"
	ContentBlockTypeThinking   ContentBlockType = "thinking" // Extended thinking for reasoning models
)

// ContentBlock is a unified content representation across all providers.
type ContentBlock struct {
	Type ContentBlockType `json:"type"`

	// For text blocks
	Text string `json:"text,omitempty"`

	// For tool_use blocks
	ToolUse *ToolUseContent `json:"tool_use,omitempty"`

	// For tool_result blocks
	ToolResult *ToolResultContent `json:"tool_result,omitempty"`

	// For image blocks
	Image *ImageContent `json:"image,omitempty"`

	// For thinking blocks (reasoning models like Claude with extended thinking)
	Thinking *ThinkingContent `json:"thinking,omitempty"`
}

// ToolUseContent represents a tool call.
type ToolUseContent struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`      // Arguments as JSON
	InputJSON string          `json:"input_json"` // Raw string for partial streaming
}

// ToolResultContent represents the result of a tool execution.
type ToolResultContent struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ImageContent represents an image (base64 or URL).
type ImageContent struct {
	Source    string `json:"source"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64 data or URL
}

// ThinkingContent represents extended thinking/reasoning from models.
type ThinkingContent struct {
	Thinking      string `json:"thinking"`
	Signature     string `json:"signature,omitempty"` // For Claude's redacted thinking
	Redacted      bool   `json:"redacted,omitempty"`
}

// --- Message Types ---

// Role is the message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a normalized chat message.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// --- Tool Definitions ---

// ToolDef defines a callable tool.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToOpenAITool converts to OpenAI tool format.
func (t ToolDef) ToOpenAITool() map[string]interface{} {
	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.InputSchema,
		},
	}
}

// ToAnthropicTool converts to Anthropic tool format.
func (t ToolDef) ToAnthropicTool() map[string]interface{} {
	return map[string]interface{}{
		"name":        t.Name,
		"description": t.Description,
		"input_schema": t.InputSchema,
	}
}

// --- Request/Response Types ---

// ChatRequest is the unified chat completion request.
type ChatRequest struct {
	Messages    []Message
	Tools       []ToolDef
	ToolChoice  ToolChoice
	MaxTokens   int
	Temperature float64
	TopP        float64
	StopWhen    []string
	System      string // System prompt (separate for Anthropic compatibility)

	// Extended thinking for reasoning models (Anthropic beta feature)
	EnableThinking bool   // Enable extended thinking
	ThinkingBudget int    // Budget in tokens for thinking (default 1024)
	RedactedThinking bool // Whether to request redacted thinking
}

// ToolChoice controls tool selection.
type ToolChoice = interface{}

const (
	ToolChoiceNone     = "none"
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"
)

// SpecificToolChoice forces a specific tool.
type SpecificToolChoice struct {
	Type string `json:"type"` // "tool"
	Name string `json:"name"` // tool name
}

// --- Stream Events ---

// StreamEventKind categorizes stream events.
type StreamEventKind string

const (
	StreamEventDelta     StreamEventKind = "delta"
	StreamEventToolCall  StreamEventKind = "tool_call"
	StreamEventThinking  StreamEventKind = "thinking" // Extended thinking content
	StreamEventDone      StreamEventKind = "done"
	StreamEventError     StreamEventKind = "error"
)

// StreamEvent is a normalized streaming event.
type StreamEvent struct {
	Kind     StreamEventKind
	Text     string      // For delta events
	ToolCall *ToolUseContent
	Thinking *ThinkingContent // Extended thinking content
	Usage    *Usage
	Err      error
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens        int
	OutputTokens       int
	TotalTokens        int
	ThinkingTokens     int // Tokens used for thinking (if available)
	CacheReadTokens    int // Anthropic prompt cache
	CacheWriteTokens   int // Anthropic prompt cache
}

// --- Gateway Interface ---

// Gateway is the unified interface for all model providers.
type Gateway interface {
	// Complete streams a chat completion with the given binding.
	// Returns a channel of StreamEvent that must be fully consumed.
	Complete(ctx context.Context, binding Binding, req ChatRequest) (<-chan StreamEvent, error)

	// TokenCount estimates tokens for a request (provider-specific counting).
	TokenCount(binding Binding, req ChatRequest) (int, error)

	// SupportsTools reports if the provider supports function calling.
	SupportsTools(binding Binding) bool

	// SupportsThinking reports if the provider supports extended thinking.
	SupportsThinking(binding Binding) bool
}

// --- Errors ---

// ErrGateway is the base error type for gateway errors.
type ErrGateway string

func (e ErrGateway) Error() string {
	return "gateway: " + string(e)
}

// Specific error constants.
const (
	ErrBudgetExceeded ErrGateway = "budget exceeded"
	ErrRateLimited    ErrGateway = "rate limited"
	ErrProviderUnavailable ErrGateway = "provider unavailable"
	ErrInvalidBinding ErrGateway = "invalid binding"
	ErrUnsupportedProvider ErrGateway = "unsupported provider"
	ErrUnrecoverable ErrGateway = "unrecoverable error"
)

// IsUnrecoverable reports if an error indicates an unrecoverable failure
// that should trigger fallback to a secondary provider.
func IsUnrecoverable(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific error types
	switch err {
	case ErrProviderUnavailable, ErrRateLimited:
		return true
	}

	// HTTP status codes that indicate unrecoverable errors
	// These would be wrapped in the actual implementation
	return false
}

// --- Provider Registry ---

// ProviderRegistry manages available providers.
type ProviderRegistry struct {
	providers map[string]Provider
}

// NewProviderRegistry creates an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry.
func (r *ProviderRegistry) Register(name string, p Provider) {
	r.providers[name] = p
}

// Get retrieves a provider by name.
func (r *ProviderRegistry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Available returns all registered provider names.
func (r *ProviderRegistry) Available() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// --- Helper Functions ---

// NormalizeMessages converts various provider message formats to canonical Message format.
// This is used by adapters to normalize their native formats.
func NormalizeMessages(msgs []Message) []Message {
	// Currently just passes through - adapters handle their own normalization
	return msgs
}

// EstimateTokens provides a rough token estimate for budgeting purposes.
// This uses a simple heuristic (~4 chars/token) and is not precise.
func EstimateTokens(text string) int {
	return len(text) / 4
}

// FormatBinding creates a human-readable binding string.
func FormatBinding(provider, model string) string {
	return fmt.Sprintf("%s/%s", provider, model)
}
