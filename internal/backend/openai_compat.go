package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/alecpullen/marshal/internal/tools"
)

type OpenAICompatibleBackend struct {
	name        string
	baseURL     string
	apiKey      string
	sessionID   string  // x-session-affinity value for Fireworks KV cache routing
	temperature float64 // default 0.0
	maxTokens   int     // default 1024
	jsonOutput  bool    // default false
	client      *http.Client
}

func NewOpenAICompatible(name, baseURL, apiKey string) *OpenAICompatibleBackend {
	return &OpenAICompatibleBackend{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{},
	}
}

func (b *OpenAICompatibleBackend) WithTemperature(t float64) *OpenAICompatibleBackend {
	b.temperature = t
	return b
}

func (b *OpenAICompatibleBackend) WithMaxTokens(n int) *OpenAICompatibleBackend {
	b.maxTokens = n
	return b
}

func (b *OpenAICompatibleBackend) WithJSONOutput(enabled bool) *OpenAICompatibleBackend {
	b.jsonOutput = enabled
	return b
}

func (b *OpenAICompatibleBackend) WithSession(id string) *OpenAICompatibleBackend {
	b.sessionID = id
	return b
}

func (b *OpenAICompatibleBackend) Name() string { return b.name }

func (b *OpenAICompatibleBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	maxTokens := b.maxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}

	type requestBody struct {
		Model          string    `json:"model"`
		Messages       []Message `json:"messages"`
		MaxTokens      int       `json:"max_tokens,omitempty"`
		Temperature    float64   `json:"temperature,omitempty"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format,omitempty"`
	}

	body := requestBody{
		Model:     model,
		Messages:  messages,
		MaxTokens: maxTokens,
	}
	if b.temperature > 0 {
		body.Temperature = b.temperature
	}
	if b.jsonOutput {
		body.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		b.baseURL+"/chat/completions",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)
	// Session affinity routes all rounds of a session to the same Fireworks
	// replica so the system-prompt KV cache is reused (50% cached-token pricing).
	if b.sessionID != "" {
		httpReq.Header.Set("x-session-affinity", b.sessionID)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return Response{}, fmt.Errorf("no choices in response")
	}

	return Response{
		Content:          result.Choices[0].Message.Content,
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
	}, nil
}

// CompleteWithTools sends messages with tool definitions and returns a Response
// that may contain tool calls or a final content string.
// This implements ToolCapableBackend.
func (b *OpenAICompatibleBackend) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	maxTokens := b.maxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}

	// Wire types for the OpenAI tools API
	type oaiFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	type oaiTool struct {
		Type     string      `json:"type"` // "function"
		Function oaiFunction `json:"function"`
	}

	// Convert provider-agnostic definitions into OpenAI wire format
	oaiTools := make([]oaiTool, 0, len(toolDefs))
	for _, d := range toolDefs {
		paramJSON, err := json.Marshal(d.Parameters)
		if err != nil {
			return tools.Response{}, fmt.Errorf("marshal tool params: %w", err)
		}
		oaiTools = append(oaiTools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  paramJSON,
			},
		})
	}

	// Wire type for messages that may carry tool calls
	type oaiToolCallFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type oaiToolCall struct {
		ID       string              `json:"id"`
		Type     string              `json:"type"`
		Function oaiToolCallFunction `json:"function"`
	}
	type oaiMessage struct {
		Role       string        `json:"role"`
		Content    string        `json:"content,omitempty"`
		ToolCallID string        `json:"tool_call_id,omitempty"`
		ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	}

	// Convert backend Messages to oaiMessage (carry tool_calls / tool_call_id)
	oaiMessages := make([]oaiMessage, 0, len(messages))
	for _, m := range messages {
		om := oaiMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			om.ToolCalls = append(om.ToolCalls, oaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: oaiToolCallFunction{
					Name:      tc.ToolName,
					Arguments: string(argsJSON),
				},
			})
		}
		oaiMessages = append(oaiMessages, om)
	}

	reqBody, err := json.Marshal(map[string]any{
		"model":       model,
		"messages":    oaiMessages,
		"tools":       oaiTools,
		"tool_choice": "auto",
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return tools.Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return tools.Response{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)
	if b.sessionID != "" {
		httpReq.Header.Set("x-session-affinity", b.sessionID)
	}

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
		Choices []struct {
			Message struct {
				Content   string        `json:"content"`
				ToolCalls []oaiToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return tools.Response{}, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return tools.Response{}, fmt.Errorf("no choices in response")
	}

	choice := result.Choices[0]

	// Parse tool calls from the response
	var calls []tools.Call
	for _, tc := range choice.Message.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		calls = append(calls, tools.Call{
			ID:        tc.ID,
			ToolName:  tc.Function.Name,
			Arguments: args,
		})
	}

	return tools.Response{
		Content:    choice.Message.Content,
		ToolCalls:  calls,
		StopReason: choice.FinishReason,
	}, nil
}
