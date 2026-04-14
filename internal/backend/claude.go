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

// ClaudeBackend implements the Backend interface for Anthropic's native API.
// It provides access to Claude models (Claude 3.5 Sonnet, Claude 3 Opus, etc.)
// with native features like prompt caching.
type ClaudeBackend struct {
	name        string
	apiKey      string
	temperature float64
	maxTokens   int
	client      *http.Client
}

// NewClaudeBackend creates a new Anthropic Claude backend.
func NewClaudeBackend(name, apiKey string) *ClaudeBackend {
	return &ClaudeBackend{
		name:   name,
		apiKey: apiKey,
		client: &http.Client{},
	}
}

// WithTemperature sets the temperature for generation.
func (b *ClaudeBackend) WithTemperature(t float64) *ClaudeBackend {
	b.temperature = t
	return b
}

// WithMaxTokens sets the maximum tokens to generate.
func (b *ClaudeBackend) WithMaxTokens(n int) *ClaudeBackend {
	b.maxTokens = n
	return b
}

// Name returns the backend identifier.
func (b *ClaudeBackend) Name() string { return b.name }

// wire types for Anthropic API
type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature,omitempty"`
	System      string          `json:"system,omitempty"`
	Messages    []claudeMessage `json:"messages"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete sends messages to Claude and returns the response.
func (b *ClaudeBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	// Convert messages to Claude format
	var claudeMsgs []claudeMessage
	var systemPrompt string

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}

		role := m.Role
		if role == "tool" {
			role = "user" // Claude doesn't have a "tool" role
		}

		claudeMsgs = append(claudeMsgs, claudeMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	reqBody := claudeRequest{
		Model:       model,
		MaxTokens:   b.maxTokensOrDefault(),
		Temperature: b.temperature,
		System:      systemPrompt,
		Messages:    claudeMsgs,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", b.apiKey)
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result claudeResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}

	// Extract content
	var content string
	for _, block := range result.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return Response{
		Content:          content,
		PromptTokens:     result.Usage.InputTokens,
		CompletionTokens: result.Usage.OutputTokens,
	}, nil
}

// CompleteWithTools sends messages with tool definitions to Claude.
// This uses Claude's native tool use beta API.
func (b *ClaudeBackend) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	// Build tool definitions for Claude
	claudeTools := make([]map[string]interface{}, 0, len(toolDefs))
	for _, d := range toolDefs {
		claudeTools = append(claudeTools, map[string]interface{}{
			"name":         d.Name,
			"description":  d.Description,
			"input_schema": d.Parameters,
		})
	}

	// Convert messages
	var claudeMsgs []claudeMessage
	var systemPrompt string

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		claudeMsgs = append(claudeMsgs, claudeMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	reqBody := map[string]interface{}{
		"model":       model,
		"max_tokens":  b.maxTokensOrDefault(),
		"temperature": b.temperature,
		"system":      systemPrompt,
		"messages":    claudeMsgs,
		"tools":       claudeTools,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return tools.Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return tools.Response{}, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", b.apiKey)
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return tools.Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tools.Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response - Claude returns content blocks including tool_use
	var result struct {
		Content []struct {
			Type  string                 `json:"type"`
			Text  string                 `json:"text,omitempty"`
			ID    string                 `json:"id,omitempty"`
			Name  string                 `json:"name,omitempty"`
			Input map[string]interface{} `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return tools.Response{}, fmt.Errorf("decode response: %w", err)
	}

	var content string
	var calls []tools.Call
	stopReason := result.StopReason

	for _, block := range result.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			calls = append(calls, tools.Call{
				ID:        block.ID,
				ToolName:  block.Name,
				Arguments: block.Input,
			})
			if stopReason == "" {
				stopReason = "tool_calls"
			}
		}
	}

	return tools.Response{
		Content:    content,
		ToolCalls:  calls,
		StopReason: stopReason,
	}, nil
}

func (b *ClaudeBackend) maxTokensOrDefault() int {
	if b.maxTokens == 0 {
		return 4096
	}
	return b.maxTokens
}
