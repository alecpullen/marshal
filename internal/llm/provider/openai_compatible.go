package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	caps := defaultCapabilities()
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

func defaultCapabilities() schema.ProviderCapabilities {
	return schema.ProviderCapabilities{
		Streaming:   true,
		Embeddings:  true,
		ToolCalling: false,
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
		messages = append(messages, chatMessageBody{Role: string(m.Role), Content: m.Content})
	}
	return json.Marshal(chatCompletionRequestBody{
		Model:       req.Model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
	})
}

func streamChatEvents(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	dec := streaming.NewDecoder(body)
	for dec.Next() {
		data := strings.TrimSpace(dec.Event().Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			events <- schema.ChatEvent{Type: schema.ChatEventDone}
			return
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
		if choice.FinishReason != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: choice.FinishReason}
			return
		}
	}
	if err := dec.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}
	// Stream ended cleanly without an explicit [DONE] or finish_reason
	// (some servers omit both) — still signal completion.
	events <- schema.ChatEvent{Type: schema.ChatEventDone}
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
	events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: choice.FinishReason}
}

// Embed requests embeddings via POST {base_url}/embeddings.
func (p *OpenAICompatible) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	body, err := json.Marshal(embedRequestBody{Model: req.Model, Input: req.Input})
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: encode embed request: %w", p.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: build embed request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: embed request failed: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return schema.EmbedResponse{}, p.httpError(resp)
	}

	var parsed embedResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: decode embed response: %w", p.name, err)
	}
	if parsed.Error != nil {
		return schema.EmbedResponse{}, errors.New(parsed.Error.Message)
	}
	embeddings := make([][]float64, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		embeddings = append(embeddings, d.Embedding)
	}
	return schema.EmbedResponse{Model: parsed.Model, Embeddings: embeddings}, nil
}
