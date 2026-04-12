package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// retryTimeout is the total window in which we keep retrying, matching
	// Aider's RETRY_TIMEOUT=60 from aider/sendchat.py.
	retryTimeout = 60 * time.Second

	// minRetryWait / maxRetryWait bound the exponential backoff delay.
	minRetryWait = 125 * time.Millisecond
	maxRetryWait = 10 * time.Second

	defaultMaxTokens   = 8192
	streamChannelBuf   = 16
)

// OpenAICompatConfig holds provider-level settings for an OpenAI-compatible
// endpoint (Fireworks, Ollama, OpenRouter, Together, Groq, DeepSeek, …).
type OpenAICompatConfig struct {
	BaseURL      string
	APIKey       string
	ModelName    string
	SupTools     bool
	SupJSONMode  bool
	HTTPClient   *http.Client // nil → http.DefaultClient
}

// openAICompatBackend implements Backend for any OpenAI-compatible /v1 endpoint.
type openAICompatBackend struct {
	cfg        OpenAICompatConfig
	httpClient *http.Client
	tokenCount func(messages []Message) (int, error)
}

// NewOpenAICompat creates a new Backend that targets an OpenAI-compatible API.
// tokenCounter may be nil; in that case a char-heuristic is used.
func NewOpenAICompat(cfg OpenAICompatConfig, tokenCounter func([]Message) (int, error)) Backend {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if tokenCounter == nil {
		tokenCounter = charHeuristicCount
	}
	return &openAICompatBackend{
		cfg:        cfg,
		httpClient: hc,
		tokenCount: tokenCounter,
	}
}

func (b *openAICompatBackend) Model() string       { return b.cfg.ModelName }
func (b *openAICompatBackend) SupportsTools() bool { return b.cfg.SupTools }
func (b *openAICompatBackend) SupportsJSONMode() bool { return b.cfg.SupJSONMode }

func (b *openAICompatBackend) TokenCount(messages []Message) (int, error) {
	return b.tokenCount(messages)
}

// --- Complete (non-streaming) ------------------------------------------------

func (b *openAICompatBackend) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := b.buildRequestBody(req, false)
	if err != nil {
		return Response{}, err
	}

	httpResp, err := b.doWithRetry(ctx, func() (*http.Response, error) {
		return b.post(ctx, body)
	})
	if err != nil {
		return Response{}, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("reading response: %w", err)
	}

	var oaiResp oaiResponse
	if err := json.Unmarshal(raw, &oaiResp); err != nil {
		return Response{}, fmt.Errorf("decoding response: %w", err)
	}
	if oaiResp.Error != nil {
		return Response{}, &APIError{
			StatusCode: httpResp.StatusCode,
			Message:    oaiResp.Error.Message,
			Type:       oaiResp.Error.Type,
		}
	}
	if len(oaiResp.Choices) == 0 {
		return Response{}, fmt.Errorf("no choices in response")
	}
	choice := oaiResp.Choices[0]
	return Response{
		Content:      choice.Message.Content,
		FinishReason: choice.FinishReason,
		Usage: Usage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
		},
		ToolCalls: choice.Message.ToolCalls,
	}, nil
}

// --- Stream ------------------------------------------------------------------

func (b *openAICompatBackend) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	body, err := b.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}

	httpResp, err := b.doWithRetry(ctx, func() (*http.Response, error) {
		return b.post(ctx, body)
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan Chunk, streamChannelBuf)
	go func() {
		defer close(ch)
		defer httpResp.Body.Close()
		b.drainSSE(ctx, httpResp.Body, ch)
	}()
	return ch, nil
}

// drainSSE reads the SSE event stream and sends Chunks to ch.
func (b *openAICompatBackend) drainSSE(ctx context.Context, r io.Reader, ch chan<- Chunk) {
	// Accumulate tool-call argument deltas keyed by tool-call index.
	type partialToolCall struct {
		id       string
		name     string
		argsBuf  strings.Builder
	}
	partials := map[int]*partialToolCall{}

	send := func(c Chunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	scanner := bufio.NewScanner(r)
	// Increase scanner buffer for large JSON lines.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			// Flush any accumulated tool calls.
			if len(partials) > 0 {
				var calls []ToolCall
				for i := 0; i < len(partials); i++ {
					p, ok := partials[i]
					if !ok {
						break
					}
					calls = append(calls, ToolCall{
						ID:   p.id,
						Type: "function",
						Function: FunctionCall{
							Name:      p.name,
							Arguments: p.argsBuf.String(),
						},
					})
				}
				send(Chunk{ToolCalls: calls, FinishReason: "tool_calls"})
			}
			return
		}

		var event oaiStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			send(Chunk{Err: fmt.Errorf("SSE parse: %w", err)})
			return
		}
		if event.Error != nil {
			send(Chunk{Err: &APIError{Message: event.Error.Message, Type: event.Error.Type}})
			return
		}
		if len(event.Choices) == 0 {
			continue
		}

		choice := event.Choices[0]
		delta := choice.Delta

		// Text content delta.
		if delta.Content != "" {
			if !send(Chunk{Content: delta.Content}) {
				return
			}
		}

		// Tool call deltas — accumulate argument strings.
		for _, tc := range delta.ToolCalls {
			p, ok := partials[tc.Index]
			if !ok {
				p = &partialToolCall{}
				partials[tc.Index] = p
			}
			if tc.ID != "" {
				p.id = tc.ID
			}
			if tc.Function.Name != "" {
				p.name = tc.Function.Name
			}
			p.argsBuf.WriteString(tc.Function.Arguments)
		}

		// Finish reason.
		if choice.FinishReason != "" && choice.FinishReason != "null" {
			if !send(Chunk{FinishReason: choice.FinishReason}) {
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		send(Chunk{Err: fmt.Errorf("stream read: %w", err)})
	}
}

// --- HTTP + retry ------------------------------------------------------------

func (b *openAICompatBackend) post(ctx context.Context, body []byte) (*http.Response, error) {
	url := strings.TrimRight(b.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	return b.httpClient.Do(req)
}

// doWithRetry executes fn, retrying on 429 and 5xx up to retryTimeout.
// It follows Aider's sendchat.py RETRY_TIMEOUT=60 pattern.
func (b *openAICompatBackend) doWithRetry(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	deadline := time.Now().Add(retryTimeout)
	wait := minRetryWait
	attempt := 0

	for {
		attempt++
		resp, err := fn()

		if err == nil && resp.StatusCode < 400 {
			return resp, nil
		}

		// Non-retriable client errors (401, 403, 400, 404, …).
		if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			return nil, parseAPIError(resp.StatusCode, raw)
		}

		// Determine wait duration.
		delay := wait
		if err == nil && resp.StatusCode == 429 {
			if ra := resp.Header.Get("retry-after"); ra != "" {
				if secs, parseErr := strconv.ParseFloat(ra, 64); parseErr == nil {
					delay = time.Duration(secs * float64(time.Second))
				}
			}
			if resp.Body != nil {
				resp.Body.Close()
			}
		} else if err == nil && resp.Body != nil {
			resp.Body.Close()
		}

		if time.Now().Add(delay).After(deadline) {
			if err != nil {
				return nil, fmt.Errorf("retry timeout after %d attempt(s): %w", attempt, err)
			}
			return nil, fmt.Errorf("retry timeout after %d attempt(s): HTTP %d", attempt, resp.StatusCode)
		}

		slog.Debug("retrying request",
			"attempt", attempt,
			"wait", delay.String(),
			"err", err,
		)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff, capped at maxRetryWait.
		wait *= 2
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
	}
}

// --- Request body builder ----------------------------------------------------

// oaiRequestBody is the JSON shape sent to /v1/chat/completions.
type oaiRequestBody struct {
	Model          string          `json:"model"`
	Messages       []oaiMessage    `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *oaiStreamOpts  `json:"stream_options,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     ToolChoice      `json:"tool_choice,omitempty"`
}

type oaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaiMessage mirrors Message but uses json tags the API expects.
type oaiMessage struct {
	Role       MessageRole `json:"role"`
	Content    string      `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

func (b *openAICompatBackend) buildRequestBody(req Request, stream bool) ([]byte, error) {
	msgs := make([]oaiMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = oaiMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
		}
	}

	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = defaultMaxTokens
	}

	body := oaiRequestBody{
		Model:          b.cfg.ModelName,
		Messages:       msgs,
		MaxTokens:      maxTok,
		Temperature:    req.Temperature,
		Stream:         stream,
		ResponseFormat: req.ResponseFormat,
	}
	if stream {
		body.StreamOptions = &oaiStreamOpts{IncludeUsage: true}
	}
	if len(req.Tools) > 0 {
		body.Tools = req.Tools
		body.ToolChoice = req.ToolChoice
	}

	return json.Marshal(body)
}

// --- API response types ------------------------------------------------------

type oaiResponse struct {
	Choices []oaiChoice `json:"choices"`
	Usage   oaiUsage    `json:"usage"`
	Error   *oaiError   `json:"error"`
}

type oaiChoice struct {
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type oaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// SSE event types.
type oaiStreamEvent struct {
	Choices []oaiStreamChoice `json:"choices"`
	Usage   *oaiUsage         `json:"usage"`
	Error   *oaiError         `json:"error"`
}

type oaiStreamChoice struct {
	Delta        oaiStreamDelta `json:"delta"`
	FinishReason string         `json:"finish_reason"`
}

type oaiStreamDelta struct {
	Role      MessageRole          `json:"role"`
	Content   string               `json:"content"`
	ToolCalls []oaiStreamToolCall  `json:"tool_calls"`
}

// oaiStreamToolCall carries partial tool-call deltas.
type oaiStreamToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// --- Errors ------------------------------------------------------------------

// APIError is returned for non-retriable HTTP errors from the API.
type APIError struct {
	StatusCode int
	Message    string
	Type       string
}

func (e *APIError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("API error: %s", e.Message)
}

func parseAPIError(statusCode int, body []byte) error {
	var wrapper struct {
		Error oaiError `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Error.Message != "" {
		return &APIError{StatusCode: statusCode, Message: wrapper.Error.Message, Type: wrapper.Error.Type}
	}
	return &APIError{StatusCode: statusCode, Message: string(body)}
}

// --- Fallback token counter --------------------------------------------------

// charHeuristicCount estimates token count using ~4 chars/token, matching
// Aider's fallback for non-tiktoken-supported providers.
func charHeuristicCount(messages []Message) (int, error) {
	total := 0
	for _, m := range messages {
		// 4 chars/token + ~4 tokens per-message overhead (role, delimiters).
		total += len(m.Content)/4 + 4
	}
	return total, nil
}
