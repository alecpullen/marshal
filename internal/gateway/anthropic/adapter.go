package anthropic

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

const (
	defaultEndpoint = "https://api.anthropic.com/v1/messages"
	apiVersion      = "2023-06-01" // Messages API v1
	betaThinking    = "thinking-2024-12-19" // Beta feature for extended thinking
)

// Adapter implements the gateway.Provider interface for Anthropic's Messages API.
// It supports the beta extended thinking feature.
type Adapter struct {
	apiKey     string
	model      string
	endpoint   string
	httpClient *http.Client

	// Enable extended thinking (beta feature)
	enableThinking   bool
	thinkingBudget   int
	redactedThinking bool
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

// WithThinking enables extended thinking.
func WithThinking(budget int, redacted bool) AdapterOption {
	return func(a *Adapter) {
		a.enableThinking = true
		a.thinkingBudget = budget
		a.redactedThinking = redacted
	}
}

// NewAdapter creates a new Anthropic adapter.
func NewAdapter(apiKey, model string, opts ...AdapterOption) *Adapter {
	a := &Adapter{
		apiKey:         apiKey,
		model:          model,
		endpoint:       defaultEndpoint,
		httpClient:     &http.Client{Timeout: 120 * time.Second},
		enableThinking: false,
		thinkingBudget: 1024,
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// Name returns the provider name.
func (a *Adapter) Name() string {
	return "anthropic"
}

// Model returns the configured model.
func (a *Adapter) Model() string {
	return a.model
}

// SupportsTools reports if this provider supports function calling.
func (a *Adapter) SupportsTools() bool {
	return true
}

// SupportsThinking reports if this provider supports extended thinking.
func (a *Adapter) SupportsThinking() bool {
	return true
}

// SupportsJSONMode reports if this provider supports structured JSON output.
func (a *Adapter) SupportsJSONMode() bool {
	return true
}

// Complete streams a chat completion.
func (a *Adapter) Complete(ctx context.Context, req gateway.ChatRequest) (<-chan gateway.StreamEvent, error) {
	// Build the request body
	body, err := a.buildRequest(req)
	if err != nil {
		return nil, err
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", a.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	// Add beta header for thinking feature if enabled
	if req.EnableThinking || a.enableThinking {
		httpReq.Header.Set("anthropic-beta", betaThinking)
	}

	// Execute request
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API error: %s - %s", resp.Status, string(body))
	}

	// Stream events
	events := make(chan gateway.StreamEvent, 16)
	go a.streamEvents(ctx, resp.Body, events)

	return events, nil
}

// TokenCount estimates tokens for a request.
func (a *Adapter) TokenCount(req gateway.ChatRequest) (int, error) {
	// Anthropic has a token counting endpoint, but we'll estimate for now
	// TODO: Implement actual token counting via Anthropic's count_tokens endpoint
	total := 0
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			switch block.Type {
			case gateway.ContentBlockTypeText:
				total += len(block.Text) / 4 // Rough estimate
			case gateway.ContentBlockTypeToolUse:
				total += len(block.ToolUse.InputJSON) / 4
			case gateway.ContentBlockTypeToolResult:
				total += len(block.ToolResult.Content) / 4
			}
		}
	}

	// Add overhead for tools
	for _, tool := range req.Tools {
		total += len(tool.Description) / 4
		total += 100 // Schema overhead
	}

	return total, nil
}

// buildRequest creates the JSON request body.
func (a *Adapter) buildRequest(req gateway.ChatRequest) ([]byte, error) {
	// Convert messages to Anthropic format
	messages, system := gateway.NormalizeMessagesToAnthropic(req.Messages, req.System)

	// Build request
	anthropicReq := map[string]interface{}{
		"model":       a.model,
		"messages":    messages,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"stream":      true,
	}

	if system != "" {
		anthropicReq["system"] = system
	}

	// Add thinking if enabled (beta feature)
	if req.EnableThinking || a.enableThinking {
		anthropicReq["thinking"] = map[string]interface{}{
			"type":   "enabled",
			"budget_tokens": a.thinkingBudget,
		}
		// Remove temperature when thinking is enabled
		delete(anthropicReq, "temperature")
	}

	// Add tools if present
	if len(req.Tools) > 0 {
		anthropicReq["tools"] = gateway.NormalizeToolsToAnthropic(req.Tools)
	}

	// Add tool choice if specified
	if req.ToolChoice != nil {
		switch choice := req.ToolChoice.(type) {
		case string:
			anthropicReq["tool_choice"] = map[string]string{"type": choice}
		case gateway.SpecificToolChoice:
			anthropicReq["tool_choice"] = map[string]interface{}{
				"type": "tool",
				"name": choice.Name,
			}
		}
	}

	return json.Marshal(anthropicReq)
}

// streamEvents reads SSE events from the response body and emits StreamEvents.
func (a *Adapter) streamEvents(ctx context.Context, body io.ReadCloser, events chan<- gateway.StreamEvent) {
	defer body.Close()
	defer close(events)

	reader := bufio.NewReader(body)
	var event gateway.SSEEvent

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

		line = strings.TrimRight(line, "\n\r")

		// Parse SSE
		if line == "" {
			// End of event, process it
			if event.Data != "" {
				a.processEvent(event, events)
				event = gateway.SSEEvent{}
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			event.Event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			event.Data = data
		}
	}
}

// processEvent processes a single SSE event and emits StreamEvents.
func (a *Adapter) processEvent(event gateway.SSEEvent, events chan<- gateway.StreamEvent) {
	switch event.Event {
	case "message_start":
		var msg messageStartEvent
		if err := json.Unmarshal([]byte(event.Data), &msg); err != nil {
			return
		}
		// Could emit initial metadata here

	case "content_block_start":
		var block contentBlockStartEvent
		if err := json.Unmarshal([]byte(event.Data), &block); err != nil {
			return
		}
		// Handle thinking block start
		if block.ContentBlock.Type == "thinking" {
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventThinking,
				Thinking: &gateway.ThinkingContent{
					Thinking: block.ContentBlock.Thinking,
				},
			}
		}

	case "content_block_delta":
		var delta contentBlockDeltaEvent
		if err := json.Unmarshal([]byte(event.Data), &delta); err != nil {
			return
		}

		switch delta.Delta.Type {
		case "text_delta":
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventDelta,
				Text: delta.Delta.Text,
			}
		case "thinking_delta":
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventThinking,
				Thinking: &gateway.ThinkingContent{
					Thinking: delta.Delta.Thinking,
				},
			}
		case "signature_delta":
			// Part of thinking block
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventThinking,
				Thinking: &gateway.ThinkingContent{
					Signature: delta.Delta.Signature,
				},
			}
		case "input_json_delta":
			// Tool use partial JSON
			// Accumulate and emit when complete
		}

	case "content_block_stop":
		// Content block finished

	case "message_delta":
		var delta messageDeltaEvent
		if err := json.Unmarshal([]byte(event.Data), &delta); err != nil {
			return
		}

		// Emit usage if available
		if delta.Usage.OutputTokens > 0 {
			events <- gateway.StreamEvent{
				Kind: gateway.StreamEventDelta,
				Usage: &gateway.Usage{
					OutputTokens: delta.Usage.OutputTokens,
				},
			}
		}

	case "message_stop":
		events <- gateway.StreamEvent{
			Kind: gateway.StreamEventDone,
		}

	case "error":
		events <- gateway.StreamEvent{
			Kind: gateway.StreamEventError,
			Err:  fmt.Errorf("anthropic error: %s", event.Data),
		}
	}
}

// --- Anthropic-specific event types ---

type messageStartEvent struct {
	Message struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Role  string `json:"role"`
		Model string `json:"model"`
	} `json:"message"`
}

type contentBlockStartEvent struct {
	Index         int `json:"index"`
	ContentBlock struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Thinking string `json:"thinking,omitempty"`
		ID       string `json:"id,omitempty"`
		Name     string `json:"name,omitempty"`
		Input    map[string]interface{} `json:"input,omitempty"`
	} `json:"content_block"`
}

type contentBlockDeltaEvent struct {
	Index int `json:"index"`
	Delta struct {
		Type      string `json:"type"`
		Text      string `json:"text,omitempty"`
		Thinking  string `json:"thinking,omitempty"`
		Signature string `json:"signature,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
}

type messageDeltaEvent struct {
	Delta struct {
		StopReason   string `json:"stop_reason"`
		StopSequence string `json:"stop_sequence"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
