package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/gateway"
)

const defaultEndpoint = "https://api.openai.com/v1/chat/completions"

// Adapter implements gateway.Provider for OpenAI-compatible APIs.
// This includes OpenAI, OpenRouter, Ollama, LM Studio, vLLM, Fireworks, etc.
type Adapter struct {
	apiKey       string
	model        string
	endpoint     string
	httpClient   *http.Client

	// Capabilities
	supportsTools   bool
	supportsJSON    bool
	supportsThinking bool // Only for reasoning models like o1

	// Provider subtype for dialect-specific handling
	subtype gateway.Provider
}

// AdapterOption configures the adapter.
type AdapterOption func(*Adapter)

// WithEndpoint sets a custom endpoint.
func WithEndpoint(endpoint string) AdapterOption {
	return func(a *Adapter) {
		a.endpoint = endpoint
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) AdapterOption {
	return func(a *Adapter) {
		a.httpClient = client
	}
}

// WithCapabilities sets capability flags.
func WithCapabilities(tools, json, thinking bool) AdapterOption {
	return func(a *Adapter) {
		a.supportsTools = tools
		a.supportsJSON = json
		a.supportsThinking = thinking
	}
}

// WithSubtype sets the provider subtype.
func WithSubtype(subtype gateway.Provider) AdapterOption {
	return func(a *Adapter) {
		a.subtype = subtype
	}
}

// NewAdapter creates a new OpenAI-compatible adapter.
func NewAdapter(apiKey, model string, opts ...AdapterOption) *Adapter {
	a := &Adapter{
		apiKey:           apiKey,
		model:            model,
		endpoint:         defaultEndpoint,
		httpClient:       &http.Client{Timeout: 120 * time.Second},
		supportsTools:    true,
		supportsJSON:     true,
		supportsThinking: false,
		subtype:          gateway.ProviderOpenAI,
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// Name returns the provider name.
func (a *Adapter) Name() string {
	return string(a.subtype)
}

// Model returns the configured model.
func (a *Adapter) Model() string {
	return a.model
}

// SupportsTools reports if this provider supports function calling.
func (a *Adapter) SupportsTools() bool {
	return a.supportsTools
}

// SupportsThinking reports if this provider supports extended thinking.
func (a *Adapter) SupportsThinking() bool {
	return a.supportsThinking
}

// SupportsJSONMode reports if this provider supports structured JSON output.
func (a *Adapter) SupportsJSONMode() bool {
	return a.supportsJSON
}

// Complete streams a chat completion.
func (a *Adapter) Complete(ctx context.Context, req gateway.ChatRequest) (<-chan gateway.StreamEvent, error) {
	body, err := a.buildRequest(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	// Provider-specific headers
	a.addProviderHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error %s: %s", resp.Status, string(body))
	}

	events := make(chan gateway.StreamEvent, 16)
	go a.streamEvents(ctx, resp.Body, events)

	return events, nil
}

// TokenCount estimates tokens for a request.
func (a *Adapter) TokenCount(req gateway.ChatRequest) (int, error) {
	// Simple estimation
	total := 0
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type == gateway.ContentBlockTypeText {
				total += len(block.Text) / 4
			}
		}
	}

	for _, tool := range req.Tools {
		total += len(tool.Description) / 4
		total += 100
	}

	return total, nil
}

// addProviderHeaders adds provider-specific headers.
func (a *Adapter) addProviderHeaders(req *http.Request) {
	switch a.subtype {
	case gateway.ProviderOpenRouter:
		// OpenRouter-specific headers
		req.Header.Set("HTTP-Referer", "https://github.com/alecpullen/marshal")
		req.Header.Set("X-Title", "Marshal")
	case gateway.ProviderOllama:
		// Ollama can use different auth or no auth
	case gateway.ProviderFireworks:
		// Fireworks may need specific headers
	}
}

// buildRequest creates the JSON request body.
func (a *Adapter) buildRequest(req gateway.ChatRequest) ([]byte, error) {
	openAIReq := map[string]interface{}{
		"model":       a.model,
		"messages":    gateway.NormalizeMessagesToOpenAI(req.Messages),
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"top_p":       req.TopP,
		"stream":      true,
	}

	if len(req.StopWhen) > 0 {
		openAIReq["stop"] = req.StopWhen
	}

	// Add tools if supported and present
	if a.supportsTools && len(req.Tools) > 0 {
		openAIReq["tools"] = gateway.NormalizeToolsToOpenAI(req.Tools)

		// Add tool choice
		if req.ToolChoice != nil {
			switch choice := req.ToolChoice.(type) {
			case string:
				openAIReq["tool_choice"] = choice
			case gateway.SpecificToolChoice:
				openAIReq["tool_choice"] = map[string]interface{}{
					"type": "function",
					"function": map[string]string{
						"name": choice.Name,
					},
				}
			}
		}
	}

	// Provider-specific modifications
	a.modifyRequestForProvider(openAIReq)

	return json.Marshal(openAIReq)
}

// modifyRequestForProvider applies provider-specific request modifications.
func (a *Adapter) modifyRequestForProvider(req map[string]interface{}) {
	switch a.subtype {
	case gateway.ProviderOllama:
		// Ollama may not support certain parameters
		delete(req, "top_p") // Some Ollama versions don't support top_p
	case gateway.ProviderLMStudio:
		// LM Studio is mostly compatible but may need adjustments
	case gateway.ProviderVLLM:
		// vLLM supports most OpenAI parameters
	}
}

// streamEvents reads SSE events and emits StreamEvents.
func (a *Adapter) streamEvents(ctx context.Context, body io.ReadCloser, events chan<- gateway.StreamEvent) {
	defer body.Close()
	defer close(events)

	reader := bufio.NewReader(body)

	for {
		select {
		case <-ctx.Done():
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventError,
				Err:  ctx.Err(),
			}
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				events <- gateway.StreamEvent{
					Kind: gateway.StreamEventError,
					Err:  fmt.Errorf("read stream: %w", err),
				}
			}
			return
		}

		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Check for stream end
		if data == "[DONE]" {
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventDone,
			}
			return
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		a.processChunk(chunk, events)
	}
}

// processChunk processes a single stream chunk.
func (a *Adapter) processChunk(chunk openAIStreamChunk, events chan<- gateway.StreamEvent) {
	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// Handle content (including reasoning/thinking content from models like Qwen)
	content := delta.Content
	if content == "" && delta.ReasoningContent != "" {
		content = delta.ReasoningContent
	}
	if content != "" {
		events <- gateway.StreamEvent{
			Kind: gateway.StreamEventDelta,
			Text: content,
		}
	}

	// Handle tool calls
	if len(delta.ToolCalls) > 0 {
		for _, tc := range delta.ToolCalls {
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventToolCall,
				ToolCall: &gateway.ToolUseContent{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					InputJSON: tc.Function.Arguments,
				},
			}
		}
	}

	// Handle finish
	if choice.FinishReason != "" {
		if choice.FinishReason == "tool_calls" {
			// Tool calls completed
		}

		// Emit usage if available
		if chunk.Usage != nil {
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventDelta,
				Usage: &gateway.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				},
			}
		}
	}
}

// --- OpenAI-specific types ---

type openAIStreamChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []openAIChoice    `json:"choices"`
	Usage   *openAIUsage      `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Delta        openAIDelta   `json:"delta"`
	FinishReason string        `json:"finish_reason"`
}

type openAIDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"` // Qwen/DeepSeek thinking content
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// --- Factory for different providers ---

// NewForProvider creates an adapter for a specific provider.
func NewForProvider(provider gateway.Provider, apiKey, model string) *Adapter {
	opts := []AdapterOption{
		WithSubtype(provider),
	}

	switch provider {
	case gateway.ProviderOllama:
		opts = append(opts,
			WithEndpoint("http://localhost:11434/v1/chat/completions"),
			WithCapabilities(false, false, false), // Tools vary by model
		)
	case gateway.ProviderLMStudio:
		opts = append(opts,
			WithEndpoint("http://localhost:1234/v1/chat/completions"),
			WithCapabilities(true, true, false),
		)
	case gateway.ProviderVLLM:
		opts = append(opts,
			WithEndpoint("http://localhost:8000/v1/chat/completions"),
			WithCapabilities(true, true, false),
		)
	case gateway.ProviderOpenRouter:
		opts = append(opts,
			WithEndpoint("https://openrouter.ai/api/v1/chat/completions"),
			WithCapabilities(true, true, false),
		)
	case gateway.ProviderFireworks:
		opts = append(opts,
			WithEndpoint("https://api.fireworks.ai/inference/v1/chat/completions"),
			WithCapabilities(true, true, false),
		)
	}

	return NewAdapter(apiKey, model, opts...)
}
