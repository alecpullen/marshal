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

// anthropicAPIVersion is the Messages API version header value.
const anthropicAPIVersion = "2023-06-01"

// anthropicDefaultMaxTokens is used when the request sets no MaxTokens —
// the Messages API requires max_tokens on every call.
const anthropicDefaultMaxTokens = 8192

// Anthropic talks to the native Anthropic Messages API (/v1/messages),
// giving marshal prompt caching, native tool use, and extended thinking —
// none of which are reachable through the OpenAI-compatible shim.
type Anthropic struct {
	name           string
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	capabilities   schema.ProviderCapabilities
	thinkingBudget int
}

func NewAnthropic(opts Options) (*Anthropic, error) {
	if opts.Name == "" {
		return nil, errors.New("provider: name is required")
	}
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("provider %q: base_url is required", opts.Name)
	}
	client := opts.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	caps := schema.ProviderCapabilities{ToolCalling: true, JSONMode: false, StructuredOutput: false, Reasoning: true}
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	return &Anthropic{
		name:           opts.Name,
		baseURL:        strings.TrimRight(opts.BaseURL, "/"),
		apiKey:         opts.APIKey,
		httpClient:     client,
		capabilities:   caps,
		thinkingBudget: opts.ThinkingBudget,
	}, nil
}

func (p *Anthropic) Name() string { return p.name }

func (p *Anthropic) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

func (p *Anthropic) setHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("x-api-key", p.apiKey)
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
}

func (p *Anthropic) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: string(body)}
}

// --- wire types ---

type anthropicCacheControl struct {
	Type string `json:"type"`
}

var anthropicEphemeralCache = &anthropicCacheControl{Type: "ephemeral"}

// anthropicContentBlock serves every block shape (text, tool_use,
// tool_result, thinking); omitempty keeps each wire block minimal.
type anthropicContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        json.RawMessage        `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      string                 `json:"content,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicRequest struct {
	Model         string                  `json:"model"`
	MaxTokens     int                     `json:"max_tokens"`
	System        []anthropicContentBlock `json:"system,omitempty"`
	Messages      []anthropicMessage      `json:"messages"`
	Stream        bool                    `json:"stream"`
	Temperature   *float64                `json:"temperature,omitempty"`
	TopP          *float64                `json:"top_p,omitempty"`
	StopSequences []string                `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool         `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice    `json:"tool_choice,omitempty"`
	Thinking      *anthropicThinking      `json:"thinking,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

// --- message conversion ---

// toAnthropicMessages extracts system messages into the top-level system
// field and converts the rest into strictly alternating user/assistant
// messages. Consecutive tool-result messages merge into a single user
// message, as the Messages API requires.
func toAnthropicMessages(msgs []schema.ChatMessage) (system []anthropicContentBlock, out []anthropicMessage) {
	for _, m := range msgs {
		switch m.Role {
		case schema.RoleSystem:
			if m.Content != "" {
				system = append(system, anthropicContentBlock{Type: "text", Text: m.Content})
			}
		case schema.RoleUser:
			out = appendAnthropicUserBlock(out, anthropicContentBlock{Type: "text", Text: m.Content})
		case schema.RoleTool:
			out = appendAnthropicUserBlock(out, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
		case schema.RoleAssistant:
			blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				input := call.Args
				if len(input) == 0 || !json.Valid(input) {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: call.ID, Name: call.Name, Input: input,
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			}
		}
	}
	return system, out
}

func appendAnthropicUserBlock(out []anthropicMessage, block anthropicContentBlock) []anthropicMessage {
	if n := len(out); n > 0 && out[n-1].Role == "user" {
		out[n-1].Content = append(out[n-1].Content, block)
		return out
	}
	return append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{block}})
}

// anthropicFinishReason maps Messages API stop reasons to the OpenAI-style
// finish reasons marshal's agent loop already understands.
func anthropicFinishReason(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return stop
	}
}

// anthropicUsageFrom maps Messages API usage to schema semantics: Anthropic
// excludes cached tokens from input_tokens, while schema.TokenUsage expects
// PromptTokens to cover the full prompt (cached tokens included).
func anthropicUsageFrom(u anthropicUsage) *schema.TokenUsage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	return &schema.TokenUsage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

func anthropicToolCallsFromBlocks(blocks []anthropicContentBlock) []schema.ToolCall {
	var out []schema.ToolCall
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		out = append(out, schema.ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
	}
	out, _ = repairToolCalls(out)
	return out
}

// --- request building ---

func (p *Anthropic) buildChatRequestBody(req schema.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("chat request: model is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("chat request: at least one message is required")
	}
	system, messages := toAnthropicMessages(req.Messages)

	// Prompt caching breakpoints on the two large, stable prefixes: the
	// system prompt and the tool definitions.
	if len(system) > 0 {
		system[len(system)-1].CacheControl = anthropicEphemeralCache
	}
	var tools []anthropicTool
	for _, tool := range req.Tools {
		tools = append(tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}
	if len(tools) > 0 {
		tools[len(tools)-1].CacheControl = anthropicEphemeralCache
	}

	maxTokens := anthropicDefaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	// The preset thinking effort overrides the provider-level thinking_budget.
	// Mapping: off -> 0 (no thinking block), low -> 1024 (Anthropic's minimum
	// budget), medium -> 4096, high -> 16384. "" leaves the provider budget
	// unchanged.
	var thinking *anthropicThinking
	budget := p.thinkingBudget
	if req.Thinking != "" {
		switch req.Thinking {
		case "off":
			budget = 0
		case "low":
			budget = 1024
		case "medium":
			budget = 4096
		case "high":
			budget = 16384
		}
	}
	if budget > 0 {
		if maxTokens <= budget {
			maxTokens = budget + 4096
		}
		thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	}

	var toolChoice *anthropicToolChoice
	switch req.ToolChoice {
	case "auto":
		toolChoice = &anthropicToolChoice{Type: "auto"}
	case "none":
		toolChoice = &anthropicToolChoice{Type: "none"}
	case "required":
		toolChoice = &anthropicToolChoice{Type: "any"}
	}

	body := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     maxTokens,
		System:        system,
		Messages:      messages,
		Stream:        req.Stream,
		StopSequences: req.Stop,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Thinking:      thinking,
	}
	// Extended thinking rejects temperature/top_p overrides.
	if thinking == nil {
		body.Temperature = req.Temperature
		body.TopP = req.TopP
	}
	// ResponseFormat (JSON mode) is intentionally dropped: Anthropic has no
	// JSON mode; structured output goes through tool use.
	return json.Marshal(body)
}

// --- Chat ---

// Chat posts to /v1/messages. Same error contract as OpenAICompatible.Chat:
// HTTP-level failures return synchronously; stream failures arrive as one
// ChatEventError followed by channel close.
func (p *Anthropic) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	body, err := p.buildChatRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
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
		go p.streamChatEvents(resp.Body, events)
	} else {
		go p.readChatResponse(resp.Body, events)
	}
	return events, nil
}

func (p *Anthropic) readChatResponse(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	var parsed anthropicResponse
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode chat response: %w", err)}
		return
	}
	var hasContent bool
	for _, block := range parsed.Content {
		switch block.Type {
		case "thinking":
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: block.Thinking}
			hasContent = true
		case "text":
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: block.Text}
			if block.Text != "" {
				hasContent = true
			}
		}
	}
	toolCalls := anthropicToolCallsFromBlocks(parsed.Content)
	if !hasContent && len(toolCalls) > 0 {
		// A response that only carries tool_use blocks is still valid.
		hasContent = true
	}
	if !hasContent {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("anthropic returned empty content (usage: in=%d out=%d)", parsed.Usage.InputTokens, parsed.Usage.OutputTokens)}
		return
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: anthropicFinishReason(parsed.StopReason),
		Usage:        anthropicUsageFrom(parsed.Usage),
		ToolCalls:    toolCalls,
	}
}

// --- streaming ---

// anthropicStreamEvent is the union of every SSE data payload the Messages
// API emits; the Type field discriminates.
type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index"`
	ContentBlock *anthropicContentBlock `json:"content_block"`
	Delta        *anthropicStreamDelta  `json:"delta"`
	Message      *anthropicResponse     `json:"message"`
	Usage        *anthropicUsage        `json:"usage"`
}

type anthropicStreamDelta struct {
	Type        string `json:"type"` // text_delta, thinking_delta, input_json_delta
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthropicBlockBuffer accumulates one content block's streamed state.
type anthropicBlockBuffer struct {
	isToolUse bool
	id        string
	name      string
	args      strings.Builder
}

// streamChatEvents consumes the Messages API SSE stream: typed events walk
// content blocks by index, with tool_use input arriving as JSON fragments.
func (p *Anthropic) streamChatEvents(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	dec := streaming.NewDecoder(body)
	blocks := make(map[int]*anthropicBlockBuffer)
	var usage anthropicUsage
	var haveUsage bool
	var finishReason string

	for dec.Next() {
		data := strings.TrimSpace(dec.Event().Data)
		if data == "" {
			continue
		}
		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode stream event: %w", err)}
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				usage = ev.Message.Usage
				haveUsage = true
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				blocks[ev.Index] = &anthropicBlockBuffer{
					isToolUse: true,
					id:        ev.ContentBlock.ID,
					name:      ev.ContentBlock.Name,
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: ev.Delta.Text}
			case "thinking_delta":
				events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: ev.Delta.Thinking}
			case "input_json_delta":
				if buf := blocks[ev.Index]; buf != nil {
					buf.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				finishReason = anthropicFinishReason(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "error":
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("stream error event: %s", data)}
			return
		}
		// content_block_stop, message_stop, ping: no action needed.
	}
	if err := dec.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}

	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	toolCalls := make([]schema.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		buf := blocks[index]
		toolCalls = append(toolCalls, schema.ToolCall{
			ID:   buf.id,
			Name: buf.name,
			Args: json.RawMessage(buf.args.String()),
		})
	}
	toolCalls, _ = repairToolCalls(toolCalls)

	var usageOut *schema.TokenUsage
	if haveUsage {
		usageOut = anthropicUsageFrom(usage)
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: finishReason,
		Usage:        usageOut,
		ToolCalls:    toolCalls,
	}
}

// --- model listing ---

type anthropicModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// Models lists available models via GET /v1/models. The endpoint reports no
// context-window metadata; limits come from the static catalog or config.
func (p *Anthropic) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
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
	var parsed anthropicModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("provider %q: decode models response: %w", p.name, err)
	}
	models := make([]schema.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		models = append(models, schema.ModelInfo{ID: m.ID, OwnedBy: "anthropic"})
	}
	return models, nil
}
