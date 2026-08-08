package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/schema"
)

// OllamaNative talks to Ollama's native /api/* endpoints instead of the
// OpenAI-compatible shim. The native API is required for keep_alive (model
// warm-ness control) and per-model capability probing via /api/show.
type OllamaNative struct {
	name         string
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	capabilities schema.ProviderCapabilities
	keepAlive    string
	limitsTable  *limits.Table

	capsMu    sync.Mutex
	capsCache map[string]ollamaModelCaps
}

// NewOllamaNative builds a native Ollama provider. opts.KeepAlive should
// already carry the resolved value (factory applies the "30m" default).
func NewOllamaNative(opts Options) (*OllamaNative, error) {
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
	return &OllamaNative{
		name:         opts.Name,
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:       opts.APIKey,
		httpClient:   client,
		capabilities: caps,
		keepAlive:    opts.KeepAlive,
		limitsTable:  opts.LimitsTable,
		capsCache:    make(map[string]ollamaModelCaps),
	}, nil
}

func (p *OllamaNative) Name() string { return p.name }

func (p *OllamaNative) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

func (p *OllamaNative) setHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OllamaNative) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: string(body)}
}

// ollamaConnHint appends a startup hint when the server is unreachable —
// the common case for a local Ollama provider.
func (p *OllamaNative) connHint(err error) error {
	if strings.Contains(err.Error(), "connection refused") {
		return fmt.Errorf("%w (is Ollama running? expected at %s)", err, p.baseURL)
	}
	return err
}

// --- per-model capability probing (/api/show) ---

// ollamaModelCaps caches one model's probed capabilities. known=false means
// the server did not report capabilities (old Ollama) or the probe failed;
// callers then fall back to the provider-level config value.
type ollamaModelCaps struct {
	tools         bool
	contextWindow int
	known         bool
}

type ollamaShowResponse struct {
	Capabilities []string                   `json:"capabilities"`
	ModelInfo    map[string]json.RawMessage `json:"model_info"`
}

// --- wire types ---

type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	KeepAlive string          `json:"keep_alive,omitempty"`
	Options   *ollamaOptions  `json:"options,omitempty"`
	Tools     []ollamaTool    `json:"tools,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
	NumCtx      *int     `json:"num_ctx,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFunction `json:"function"`
}

type ollamaToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ollamaChatChunk is both the streaming NDJSON line shape and the
// non-streaming response body.
type ollamaChatChunk struct {
	Message         ollamaChatChunkMessage `json:"message"`
	Done            bool                   `json:"done"`
	DoneReason      string                 `json:"done_reason"`
	PromptEvalCount int                    `json:"prompt_eval_count"`
	EvalCount       int                    `json:"eval_count"`
	Error           string                 `json:"error"`
}

type ollamaChatChunkMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking"`
	ToolCalls []ollamaToolCall `json:"tool_calls"`
}

// --- request building ---

func (p *OllamaNative) buildChatRequestBody(req schema.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("chat request: model is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("chat request: at least one message is required")
	}
	messages := make([]ollamaMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := ollamaMessage{Role: string(m.Role), Content: m.Content}
		for _, call := range m.ToolCalls {
			args := call.Args
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
				Function: ollamaToolCallFunction{Name: call.Name, Arguments: args},
			})
		}
		messages = append(messages, om)
	}
	var tools []ollamaTool
	for _, tool := range req.Tools {
		tools = append(tools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	var opts *ollamaOptions
	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil || req.ContextWindow != nil || len(req.Stop) > 0 {
		opts = &ollamaOptions{
			Temperature: req.Temperature,
			TopP:        req.TopP,
			NumPredict:  req.MaxTokens,
			NumCtx:      req.ContextWindow,
			Stop:        req.Stop,
		}
	}
	var format json.RawMessage
	if req.ResponseFormat != nil {
		if req.ResponseFormat.JSONSchema != nil && len(req.ResponseFormat.JSONSchema.Schema) > 0 {
			format = req.ResponseFormat.JSONSchema.Schema
		} else {
			format = json.RawMessage(`"json"`)
		}
	}
	return json.Marshal(ollamaChatRequest{
		Model:     req.Model,
		Messages:  messages,
		Stream:    req.Stream,
		KeepAlive: p.keepAlive,
		Options:   opts,
		Tools:     tools,
		Format:    format,
	})
}

// --- response normalization ---

// ollamaToolCallsFromWire converts native tool calls. Ollama assigns no
// call IDs, so stable sequential IDs are generated for history round-trips.
func ollamaToolCallsFromWire(calls []ollamaToolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for i, call := range calls {
		out = append(out, schema.ToolCall{
			ID:   fmt.Sprintf("ollama-%d", i),
			Name: call.Function.Name,
			Args: call.Function.Arguments,
		})
	}
	out, _ = repairToolCalls(out)
	return out
}

func ollamaUsageFrom(chunk ollamaChatChunk) *schema.TokenUsage {
	return &schema.TokenUsage{
		PromptTokens:     chunk.PromptEvalCount,
		CompletionTokens: chunk.EvalCount,
		TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
	}
}

// --- Chat ---

// Chat posts to /api/chat. HTTP-level failures return synchronously; once
// the channel is handed back, failures arrive as one ChatEventError followed
// by channel close (same contract as OpenAICompatible.Chat).
func (p *OllamaNative) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	if len(req.Tools) > 0 && !p.modelSupportsTools(ctx, req.Model) {
		req.Tools = nil
	}
	body, err := p.buildChatRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, p.connHint(fmt.Errorf("provider %q: chat request failed: %w", p.name, err))
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}

	events := make(chan schema.ChatEvent)
	if req.Stream {
		go p.streamChatEvents(resp.Body, events)
	} else {
		go p.readChatResponse(resp.Body, events)
	}
	return events, nil
}

func (p *OllamaNative) readChatResponse(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	var parsed ollamaChatChunk
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode chat response: %w", err)}
		return
	}
	if parsed.Error != "" {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(parsed.Error)}
		return
	}
	if parsed.Message.Thinking != "" {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: parsed.Message.Thinking}
	}
	if parsed.Message.Content != "" {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: parsed.Message.Content}
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: parsed.DoneReason,
		Usage:        ollamaUsageFrom(parsed),
		ToolCalls:    ollamaToolCallsFromWire(parsed.Message.ToolCalls),
	}
}

// streamChatEvents consumes Ollama's NDJSON stream: one JSON object per
// line, terminated by an object with done:true carrying token counts.
func (p *OllamaNative) streamChatEvents(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var toolCalls []schema.ToolCall
	var usage *schema.TokenUsage
	var finishReason string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode stream line: %w", err)}
			return
		}
		if chunk.Error != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(chunk.Error)}
			return
		}
		if chunk.Message.Thinking != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: chunk.Message.Thinking}
		}
		if chunk.Message.Content != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: chunk.Message.Content}
		}
		toolCalls = append(toolCalls, ollamaToolCallsFromWire(chunk.Message.ToolCalls)...)
		if chunk.Done {
			finishReason = chunk.DoneReason
			usage = ollamaUsageFrom(chunk)
		}
	}
	if err := scanner.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: finishReason,
		Usage:        usage,
		ToolCalls:    toolCalls,
	}
}

// cachedCaps returns model's probed capabilities, probing /api/show once
// per model and caching the result (including failures, so a server
// lacking the endpoint is not re-probed on every call).
func (p *OllamaNative) cachedCaps(ctx context.Context, model string) ollamaModelCaps {
	p.capsMu.Lock()
	caps, ok := p.capsCache[model]
	p.capsMu.Unlock()
	if !ok {
		caps = p.probeModelCaps(ctx, model)
		p.capsMu.Lock()
		p.capsCache[model] = caps
		p.capsMu.Unlock()
	}
	return caps
}

// modelSupportsTools resolves tool-calling support for one model, probing
// /api/show once per model and caching the result (including failures).
func (p *OllamaNative) modelSupportsTools(ctx context.Context, model string) bool {
	caps := p.cachedCaps(ctx, model)
	if !caps.known {
		return p.capabilities.ToolCalling
	}
	return caps.tools
}

// ProbeCapabilities reports model's tool-calling support and context
// window, probed via /api/show and cached per model. ToolCalling is nil
// when the server's response omitted the capabilities field entirely (old
// servers). ContextWindow is 0 when model_info reported no
// *.context_length key.
func (p *OllamaNative) ProbeCapabilities(ctx context.Context, model string) ModelCapabilities {
	caps := p.cachedCaps(ctx, model)
	var toolCalling *bool
	if caps.known {
		t := caps.tools
		toolCalling = &t
	}
	return ModelCapabilities{ToolCalling: toolCalling, ContextWindow: caps.contextWindow}
}

func (p *OllamaNative) probeModelCaps(ctx context.Context, model string) ollamaModelCaps {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return ollamaModelCaps{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return ollamaModelCaps{}
	}
	req.Header.Set("Content-Type", "application/json")
	p.setHeaders(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return ollamaModelCaps{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ollamaModelCaps{}
	}
	var parsed ollamaShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ollamaModelCaps{}
	}
	if parsed.Capabilities == nil {
		return ollamaModelCaps{} // old server: field absent entirely
	}
	caps := ollamaModelCaps{known: true, contextWindow: contextLengthFromModelInfo(parsed.ModelInfo)}
	for _, c := range parsed.Capabilities {
		if c == "tools" {
			caps.tools = true
		}
	}
	return caps
}

// contextLengthFromModelInfo scans /api/show's model_info block for a
// "<architecture>.context_length" key. The architecture name varies per
// model family (e.g. "qwen3.context_length", "llama.context_length"), so
// this looks the key up by suffix rather than requiring the caller to know
// the family name in advance.
func contextLengthFromModelInfo(info map[string]json.RawMessage) int {
	for key, raw := range info {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// --- model listing ---

type ollamaTagsResponse struct {
	Models []ollamaTagModel `json:"models"`
}

type ollamaTagModel struct {
	Name string `json:"name"`
}

// Models lists locally pulled models via GET /api/tags. The endpoint
// reports no context-window metadata, so limit-table enrichment applies
// when a table is configured, exactly as in OpenAICompatible.Models.
func (p *OllamaNative) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("provider %q: build models request: %w", p.name, err)
	}
	p.setHeaders(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, p.connHint(fmt.Errorf("provider %q: models request failed: %w", p.name, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, p.httpError(resp)
	}
	var parsed ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("provider %q: decode models response: %w", p.name, err)
	}
	models := make([]schema.ModelInfo, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		info := schema.ModelInfo{ID: m.Name, OwnedBy: "ollama"}
		if p.limitsTable != nil {
			if lim, kind := p.limitsTable.Lookup(p.name, m.Name); kind != limits.MatchNone {
				info.ContextWindow = lim.ContextWindow
				info.MaxOutputTokens = lim.MaxOutputTokens
				info.ToolCalling = lim.ToolCalling
			}
		}
		models = append(models, info)
	}
	return models, nil
}
