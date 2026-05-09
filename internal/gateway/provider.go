package gateway

import (
	"context"
	"encoding/json"
)

// ProviderAdapter is the interface implemented by each model provider adapter.
// It provides a common abstraction over Anthropic, OpenAI, Ollama, etc.
type ProviderAdapter interface {
	// Complete streams a chat completion.
	// The returned channel must be fully consumed by the caller.
	Complete(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)

	// TokenCount estimates tokens for a request.
	TokenCount(req ChatRequest) (int, error)

	// SupportsTools reports if this provider supports function calling.
	SupportsTools() bool

	// SupportsThinking reports if this provider supports extended thinking.
	SupportsThinking() bool

	// SupportsJSONMode reports if this provider supports structured JSON output.
	SupportsJSONMode() bool

	// Name returns the provider name.
	Name() string

	// Model returns the configured model.
	Model() string
}

// BaseProvider provides common functionality for all providers.
type BaseProvider struct {
	name             string
	model            string
	supportsTools    bool
	supportsThinking bool
	supportsJSON     bool
}

// Name returns the provider name.
func (p *BaseProvider) Name() string {
	return p.name
}

// Model returns the configured model.
func (p *BaseProvider) Model() string {
	return p.model
}

// SupportsTools reports if this provider supports function calling.
func (p *BaseProvider) SupportsTools() bool {
	return p.supportsTools
}

// SupportsThinking reports if this provider supports extended thinking.
func (p *BaseProvider) SupportsThinking() bool {
	return p.supportsThinking
}

// SupportsJSONMode reports if this provider supports structured JSON output.
func (p *BaseProvider) SupportsJSONMode() bool {
	return p.supportsJSON
}

// --- Request/Response Normalization Helpers ---

// NormalizeMessagesToOpenAI converts canonical messages to OpenAI format.
func NormalizeMessagesToOpenAI(msgs []Message) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(msgs))

	for _, msg := range msgs {
		openAIMsg := map[string]interface{}{
			"role": string(msg.Role),
		}

		// Convert content blocks to OpenAI format
		if len(msg.Content) == 1 && msg.Content[0].Type == ContentBlockTypeText {
			// Simple text message
			openAIMsg["content"] = msg.Content[0].Text
		} else {
			// Complex content (multiple blocks or non-text)
			content := make([]map[string]interface{}, 0, len(msg.Content))
			for _, block := range msg.Content {
				switch block.Type {
				case ContentBlockTypeText:
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": block.Text,
					})
				case ContentBlockTypeToolUse:
					content = append(content, map[string]interface{}{
						"type": "tool_use",
						"id":   block.ToolUse.ID,
						"name": block.ToolUse.Name,
						"input": block.ToolUse.Input,
					})
				case ContentBlockTypeToolResult:
					content = append(content, map[string]interface{}{
						"type": "tool_result",
						"tool_use_id": block.ToolResult.ToolUseID,
						"content":     block.ToolResult.Content,
					})
				}
			}
			openAIMsg["content"] = content
		}

		result = append(result, openAIMsg)
	}

	return result
}

// NormalizeMessagesToAnthropic converts canonical messages to Anthropic format.
func NormalizeMessagesToAnthropic(msgs []Message, system string) ([]map[string]interface{}, string) {
	result := make([]map[string]interface{}, 0, len(msgs))
	var extractedSystem string

	for _, msg := range msgs {
		// Handle system messages specially
		if msg.Role == RoleSystem {
			if len(msg.Content) > 0 && msg.Content[0].Type == ContentBlockTypeText {
				extractedSystem = msg.Content[0].Text
			}
			continue
		}

		anthropicMsg := map[string]interface{}{
			"role": string(msg.Role),
		}

		// Convert content blocks to Anthropic format
		content := make([]map[string]interface{}, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch block.Type {
			case ContentBlockTypeText:
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": block.Text,
				})
			case ContentBlockTypeToolUse:
				content = append(content, map[string]interface{}{
					"type": "tool_use",
					"id":   block.ToolUse.ID,
					"name": block.ToolUse.Name,
					"input": block.ToolUse.Input,
				})
			case ContentBlockTypeToolResult:
				content = append(content, map[string]interface{}{
					"type": "tool_result",
					"tool_use_id": block.ToolResult.ToolUseID,
					"content":     block.ToolResult.Content,
				})
			}
		}
		anthropicMsg["content"] = content

		result = append(result, anthropicMsg)
	}

	// Use provided system if none extracted from messages
	if extractedSystem == "" {
		extractedSystem = system
	}

	return result, extractedSystem
}

// NormalizeToolsToOpenAI converts canonical tool definitions to OpenAI format.
func NormalizeToolsToOpenAI(tools []ToolDef) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		result = append(result, tool.ToOpenAITool())
	}
	return result
}

// NormalizeToolsToAnthropic converts canonical tool definitions to Anthropic format.
func NormalizeToolsToAnthropic(tools []ToolDef) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		result = append(result, tool.ToAnthropicTool())
	}
	return result
}

// --- Tool Call Accumulation ---

// ToolCallAccumulator helps accumulate partial tool call arguments during streaming.
type ToolCallAccumulator struct {
	calls map[string]*ToolUseContent
}

// NewToolCallAccumulator creates a new accumulator.
func NewToolCallAccumulator() *ToolCallAccumulator {
	return &ToolCallAccumulator{
		calls: make(map[string]*ToolUseContent),
	}
}

// AddPartial adds a partial tool call update.
// For OpenAI-style streaming where arguments arrive in chunks.
func (a *ToolCallAccumulator) AddPartial(id, name, argumentsChunk string) {
	if existing, ok := a.calls[id]; ok {
		// Append to existing
		existing.InputJSON += argumentsChunk
	} else {
		// New tool call
		a.calls[id] = &ToolUseContent{
			ID:        id,
			Name:      name,
			InputJSON: argumentsChunk,
		}
	}
}

// GetComplete returns completed tool calls.
func (a *ToolCallAccumulator) GetComplete() []ToolUseContent {
	result := make([]ToolUseContent, 0, len(a.calls))
	for _, call := range a.calls {
		// Try to parse JSON if complete
		if call.InputJSON != "" {
			// Validate it's complete JSON
			var input map[string]interface{}
			if err := json.Unmarshal([]byte(call.InputJSON), &input); err == nil {
				call.Input = []byte(call.InputJSON)
				result = append(result, *call)
			}
		}
	}
	return result
}

// GetAll returns all tool calls (even incomplete ones).
func (a *ToolCallAccumulator) GetAll() []ToolUseContent {
	result := make([]ToolUseContent, 0, len(a.calls))
	for _, call := range a.calls {
		result = append(result, *call)
	}
	return result
}

// Clear removes all accumulated calls.
func (a *ToolCallAccumulator) Clear() {
	a.calls = make(map[string]*ToolUseContent)
}

// --- SSE Parsing ---

// SSEEvent represents a parsed Server-Sent Events event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
	Retry int
}

// ParseSSE parses a Server-Sent Events line.
// Returns true if a complete event is ready.
func ParseSSE(line string, event *SSEEvent) bool {
	if line == "" {
		// Empty line indicates end of event
		return event.Event != "" || event.Data != ""
	}

	if !contains(line, ":") {
		return false
	}

	colonIdx := indexOf(line, ":")
	field := line[:colonIdx]
	value := line[colonIdx+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:] // Remove leading space
	}

	switch field {
	case "event":
		event.Event = value
	case "data":
		event.Data += value + "\n"
	case "id":
		event.ID = value
	case "retry":
		// Parse retry value
	}

	return false
}

func contains(s, substr string) bool {
	return indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
