package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"marshal/internal/llm/schema"
	"marshal/internal/llm/streaming"
)

type Options struct {
	Name       string
	BaseURL    string
	APIKey     string // already resolved — see factory.go (Task 5)
	HTTPClient *http.Client
	// Capabilities overrides the default capability set. Leave nil to use
	// defaultCapabilities().
	Capabilities *schema.ProviderCapabilities
}

type OpenAICompatible struct {
	name         string
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	capabilities schema.ProviderCapabilities
}

func NewOpenAICompatible(opts Options) (*OpenAICompatible, error) {
	if opts.Name == "" {
		return nil, errors.New("provider: name is required")
	}
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("provider %q: base_url is required", opts.Name)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	caps := DefaultCapabilities()
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	return &OpenAICompatible{
		name:         opts.Name,
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:       opts.APIKey,
		httpClient:   client,
		capabilities: caps,
	}, nil
}

// DefaultCapabilities returns the baseline capability set for an
// OpenAI-compatible provider. Callers may override individual fields before
// passing the struct to NewOpenAICompatible via Options.Capabilities.
func DefaultCapabilities() schema.ProviderCapabilities {
	return schema.ProviderCapabilities{
		ToolCalling:      false,
		JSONMode:         true,
		StructuredOutput: true,
	}
}

func (p *OpenAICompatible) Name() string { return p.name }

func (p *OpenAICompatible) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

func (p *OpenAICompatible) setHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OpenAICompatible) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: string(body)}
}

// Models lists available models via GET {base_url}/models.
func (p *OpenAICompatible) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("provider %q: build models request: %w", p.name, err)
	}
	p.setHeaders(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: models request failed: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, p.httpError(resp)
	}

	var parsed modelsResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("provider %q: decode models response: %w", p.name, err)
	}
	models := make([]schema.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		models = append(models, schema.ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	return models, nil
}

// Chat sends a chat completion request. req.Stream controls the wire format:
// callers explicitly opt into SSE streaming; Chat mechanically supports both
// and normalizes the result into the same <-chan schema.ChatEvent shape
// either way.
//
// HTTP-level failures (connection errors, non-2xx status) are returned
// synchronously as the second return value. Once the channel is handed
// back, all further failures (malformed SSE payload, an embedded
// {"error":...} object, a dropped connection, or context cancellation) are
// delivered as a single ChatEventError event, after which the channel is
// closed.
func (p *OpenAICompatible) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	body, err := buildChatRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider %q: chat request failed: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}

	events := make(chan schema.ChatEvent)
	if req.Stream {
		go streamChatEvents(resp.Body, events)
	} else {
		go readChatResponse(resp.Body, events)
	}
	return events, nil
}

func buildChatRequestBody(req schema.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("chat request: model is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("chat request: at least one message is required")
	}
	messages := make([]chatMessageBody, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, chatMessageToBody(m))
	}
	tools := make([]toolDefinitionBody, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, toolDefinitionBody{
			Type: "function",
			Function: toolFunctionBody{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	var streamOpts *streamOptions
	if req.Stream {
		streamOpts = &streamOptions{IncludeUsage: true}
	}
	return json.Marshal(chatCompletionRequestBody{
		Model:          req.Model,
		Messages:       messages,
		Stream:         req.Stream,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxTokens,
		Stop:           req.Stop,
		ResponseFormat: req.ResponseFormat,
		Tools:          tools,
		ToolChoice:     req.ToolChoice,
		StreamOptions:  streamOpts,
	})
}

func chatMessageToBody(m schema.ChatMessage) chatMessageBody {
	body := chatMessageBody{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		body.ToolCalls = make([]toolCallBody, 0, len(m.ToolCalls))
		for _, call := range m.ToolCalls {
			body.ToolCalls = append(body.ToolCalls, toolCallBody{
				ID:   call.ID,
				Type: "function",
				Function: toolFunctionBody{
					Name:      call.Name,
					Arguments: string(call.Args),
				},
			})
		}
	}
	return body
}

type streamingToolCallBuffer struct {
	id       string
	name     string
	argument strings.Builder
}

func (b *streamingToolCallBuffer) append(call toolCallBody) {
	if call.ID != "" {
		b.id = call.ID
	}
	if call.Function.Name != "" {
		b.name = call.Function.Name
	}
	if call.Function.Arguments != "" {
		b.argument.WriteString(call.Function.Arguments)
	}
}

func toolCallsFromWire(calls []toolCallBody) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, schema.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: json.RawMessage(call.Function.Arguments),
		})
	}
	out, _ = repairToolCalls(out)
	return out
}

func toolCallsFromStreamBuffers(buffers map[int]*streamingToolCallBuffer) []schema.ToolCall {
	if len(buffers) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(buffers))
	for index := range buffers {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	out := make([]schema.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		buf := buffers[index]
		out = append(out, schema.ToolCall{
			ID:   buf.id,
			Name: buf.name,
			Args: json.RawMessage(buf.argument.String()),
		})
	}
	out, _ = repairToolCalls(out)
	return out
}

// tokenUsageFrom maps the wire usage body to the schema type.
func tokenUsageFrom(u *usageBody) *schema.TokenUsage {
	if u == nil {
		return nil
	}
	out := &schema.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	// OpenAI detail objects.
	if u.PromptTokensDetails != nil {
		out.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	// DeepSeek top-level cache fields override only when the OpenAI
	// detail objects didn't populate them (DeepSeek doesn't use _details).
	if out.CacheReadTokens == 0 && u.PromptCacheHitTokens != nil {
		out.CacheReadTokens = *u.PromptCacheHitTokens
	}
	if u.PromptCacheMissTokens != nil {
		out.CacheWriteTokens = *u.PromptCacheMissTokens
	}
	return out
}

func streamChatEvents(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	dec := streaming.NewDecoder(body)
	var finishReason string
	var usage *schema.TokenUsage
	toolBuffers := make(map[int]*streamingToolCallBuffer)

	for dec.Next() {
		data := strings.TrimSpace(dec.Event().Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode stream chunk: %w", err)}
			return
		}
		if chunk.Error != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(chunk.Error.Message)}
			return
		}
		if chunk.Usage != nil {
			usage = tokenUsageFrom(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.ReasoningContent != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: choice.Delta.ReasoningContent}
		}
		if choice.Delta.Content != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: choice.Delta.Content}
		}
		for _, call := range choice.Delta.ToolCalls {
			buf := toolBuffers[call.Index]
			if buf == nil {
				buf = &streamingToolCallBuffer{}
				toolBuffers[call.Index] = buf
			}
			buf.append(call)
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
	}
	if err := dec.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: finishReason,
		Usage:        usage,
		ToolCalls:    toolCallsFromStreamBuffers(toolBuffers),
	}
}

func readChatResponse(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	var parsed chatCompletionResponse
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode chat response: %w", err)}
		return
	}
	if parsed.Error != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(parsed.Error.Message)}
		return
	}
	if len(parsed.Choices) == 0 {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New("chat response contained no choices")}
		return
	}
	choice := parsed.Choices[0]
	if choice.Message.Content != "" {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: choice.Message.Content}
	}
	var usage *schema.TokenUsage
	if parsed.Usage != nil {
		usage = tokenUsageFrom(parsed.Usage)
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: choice.FinishReason,
		Usage:        usage,
		ToolCalls:    toolCallsFromWire(choice.Message.ToolCalls),
	}
}
