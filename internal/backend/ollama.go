package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/alecpullen/marshal/internal/tools"
)

// OllamaBackend implements the Backend interface for Ollama's native API.
// It communicates with Ollama's /api/chat endpoint rather than the OpenAI-compatible endpoint.
//
// Ollama is typically run locally and does not require an API key.
// The native API provides features like streaming (future enhancement) and
// direct access to Ollama-specific options.
//
// Example:
//
//	be := backend.NewOllamaBackend("executor", "http://localhost:11434")
//	resp, err := be.Complete(ctx, "llama3.2", []backend.Message{
//	    {Role: "system", Content: "You are a helpful assistant."},
//	    {Role: "user", Content: "Hello!"},
//	})
type OllamaBackend struct {
	name          string
	baseURL       string
	temperature   float64
	maxTokens     int
	contextWindow int // num_ctx for Ollama, critical for tool use
	client        *http.Client
}

// NewOllamaBackend creates a new Ollama backend.
// The baseURL should be the Ollama server address (e.g., "http://localhost:11434").
// No API key is required for local Ollama instances.
func NewOllamaBackend(name, baseURL string) *OllamaBackend {
	return &OllamaBackend{
		name:    name,
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

// WithTemperature sets the temperature for generation.
// Returns the backend for method chaining.
func (b *OllamaBackend) WithTemperature(t float64) *OllamaBackend {
	b.temperature = t
	return b
}

// WithMaxTokens sets the maximum tokens to generate.
// Returns the backend for method chaining.
func (b *OllamaBackend) WithMaxTokens(n int) *OllamaBackend {
	b.maxTokens = n
	return b
}

// WithContextWindow sets the context window size (num_ctx) for the model.
// This is critical for tool use and other features requiring large context.
// Returns the backend for method chaining.
func (b *OllamaBackend) WithContextWindow(n int) *OllamaBackend {
	b.contextWindow = n
	return b
}

// Name returns the backend identifier (used for logging/debugging).
func (b *OllamaBackend) Name() string { return b.name }

// Complete sends messages to the Ollama /api/chat endpoint and returns the response.
// Ollama uses streaming by default for better UX - this method collects streaming chunks.
// Implements the Backend interface.
func (b *OllamaBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	// Ollama supports streaming - use CompleteStreaming and collect chunks
	var fullContent strings.Builder
	err := b.CompleteStreaming(ctx, model, messages, func(chunk StreamResponse) {
		fullContent.WriteString(chunk.Content)
	})
	if err != nil {
		return Response{}, err
	}

	// Ollama doesn't return token counts in the native API
	// We return 0 for tokens and the caller can estimate if needed
	return Response{
		Content:          fullContent.String(),
		PromptTokens:     0, // Ollama native API doesn't provide this
		CompletionTokens: 0, // Ollama native API doesn't provide this
	}, nil
}

// buildOptions creates the Ollama options map based on backend configuration.
func (b *OllamaBackend) buildOptions() map[string]interface{} {
	opts := make(map[string]interface{})

	if b.temperature > 0 {
		opts["temperature"] = b.temperature
	}
	if b.maxTokens > 0 {
		opts["num_predict"] = b.maxTokens
	}
	if b.contextWindow > 0 {
		opts["num_ctx"] = b.contextWindow
	}

	return opts
}

// CompleteStreaming implements the StreamingBackend interface.
// It streams responses from Ollama's /api/chat endpoint, calling onChunk for each piece of content.
func (b *OllamaBackend) CompleteStreaming(
	ctx context.Context,
	model string,
	messages []Message,
	onChunk func(StreamResponse),
) error {
	if onChunk == nil {
		return fmt.Errorf("onChunk callback is required")
	}

	// Convert generic messages to Ollama format
	ollamaMessages := make([]ollamaMessage, len(messages))
	for i, m := range messages {
		ollamaMessages[i] = ollamaMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}

	// Build request body with streaming enabled
	reqBody := ollamaChatRequest{
		Model:    model,
		Messages: ollamaMessages,
		Stream:   true,
		Options:  b.buildOptions(),
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		b.baseURL+"/api/chat",
		bytes.NewReader(bodyJSON),
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	// Parse NDJSON stream
	decoder := json.NewDecoder(resp.Body)
	for {
		var chunk ollamaChatResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode chunk: %w", err)
		}

		onChunk(StreamResponse{
			Content: chunk.Message.Content,
			Done:    chunk.Done,
		})

		if chunk.Done {
			break
		}
	}

	return nil
}

// CompleteWithTools sends messages with tool definitions to Ollama's /api/chat endpoint.
// Ollama supports OpenAI-compatible tool calling for models like qwen2.5-coder and devstral.
// Models that don't support tools will return a plain content response (no tool calls),
// causing the executor loop to terminate after the first turn.
// This implements ToolCapableBackend.
func (b *OllamaBackend) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	// Ollama uses the same OpenAI-shaped tool format
	type ollamaToolFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	type ollamaTool struct {
		Type     string             `json:"type"` // "function"
		Function ollamaToolFunction `json:"function"`
	}
	type ollamaToolCallFunction struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	type ollamaToolCall struct {
		ID       string                 `json:"id"`
		Type     string                 `json:"type"`
		Function ollamaToolCallFunction `json:"function"`
	}
	type ollamaMsg struct {
		Role      string           `json:"role"`
		Content   string           `json:"content,omitempty"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	}

	ollamaTools := make([]ollamaTool, 0, len(toolDefs))
	for _, d := range toolDefs {
		paramJSON, err := json.Marshal(d.Parameters)
		if err != nil {
			return tools.Response{}, fmt.Errorf("marshal tool params: %w", err)
		}
		ollamaTools = append(ollamaTools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  paramJSON,
			},
		})
	}

	// Convert backend messages
	ollamaMsgs := make([]ollamaMsg, 0, len(messages))
	for _, m := range messages {
		om := ollamaMsg{Role: m.Role, Content: m.Content}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: ollamaToolCallFunction{
					Name:      tc.ToolName,
					Arguments: tc.Arguments,
				},
			})
		}
		ollamaMsgs = append(ollamaMsgs, om)
	}

	reqBody, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": ollamaMsgs,
		"tools":    ollamaTools,
		"stream":   false,
		"options":  b.buildOptions(),
	})
	if err != nil {
		return tools.Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/api/chat", bytes.NewReader(reqBody))
	if err != nil {
		return tools.Response{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return tools.Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tools.Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []ollamaToolCall `json:"tool_calls"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return tools.Response{}, fmt.Errorf("decode response: %w", err)
	}

	var calls []tools.Call
	for _, tc := range result.Message.ToolCalls {
		calls = append(calls, tools.Call{
			ID:        tc.ID,
			ToolName:  tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return tools.Response{
		Content:    result.Message.Content,
		ToolCalls:  calls,
		StopReason: result.DoneReason,
	}, nil
}

// --- Ollama API types ---

// ollamaMessage represents a message in Ollama's chat format.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaChatRequest is the request body for Ollama's /api/chat endpoint.
type ollamaChatRequest struct {
	Model    string                 `json:"model"`
	Messages []ollamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// ollamaChatResponse is the response from Ollama's /api/chat endpoint.
type ollamaChatResponse struct {
	Model     string        `json:"model"`
	CreatedAt string        `json:"created_at"`
	Message   ollamaMessage `json:"message"`
	Done      bool          `json:"done"`
}
