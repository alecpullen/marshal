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

// GeminiBackend implements the Backend interface for Google's Gemini API.
// It provides access to Gemini models (Gemini 1.5 Flash, Gemini 1.5 Pro, etc.)
type GeminiBackend struct {
	name        string
	apiKey      string
	temperature float64
	maxTokens   int
	client      *http.Client
}

// NewGeminiBackend creates a new Google Gemini backend.
func NewGeminiBackend(name, apiKey string) *GeminiBackend {
	return &GeminiBackend{
		name:   name,
		apiKey: apiKey,
		client: &http.Client{},
	}
}

// WithTemperature sets the temperature for generation.
func (b *GeminiBackend) WithTemperature(t float64) *GeminiBackend {
	b.temperature = t
	return b
}

// WithMaxTokens sets the maximum tokens to generate.
func (b *GeminiBackend) WithMaxTokens(n int) *GeminiBackend {
	b.maxTokens = n
	return b
}

// Name returns the backend identifier.
func (b *GeminiBackend) Name() string { return b.name }

// wire types for Gemini API
type geminiContent struct {
	Role  string `json:"role,omitempty"`
	Parts []struct {
		Text string `json:"text,omitempty"`
	} `json:"parts"`
}

type geminiRequest struct {
	Contents         []geminiContent `json:"contents"`
	GenerationConfig struct {
		Temperature     float64 `json:"temperature,omitempty"`
		MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	} `json:"generationConfig,omitempty"`
	SystemInstruction *geminiContent `json:"systemInstruction,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string `json:"text,omitempty"`
				FunctionCall *struct {
					Name string                 `json:"name"`
					Args map[string]interface{} `json:"args"`
				} `json:"functionCall,omitempty"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// Complete sends messages to Gemini and returns the response.
func (b *GeminiBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	var contents []geminiContent
	var systemInstruction *geminiContent

	for _, m := range messages {
		if m.Role == "system" {
			systemInstruction = &geminiContent{
				Parts: []struct {
					Text string `json:"text,omitempty"`
				}{{Text: m.Content}},
			}
			continue
		}

		role := m.Role
		if role == "assistant" {
			role = "model" // Gemini uses "model" not "assistant"
		}
		if role == "tool" {
			role = "user" // Gemini doesn't have a "tool" role
		}

		contents = append(contents, geminiContent{
			Role: role,
			Parts: []struct {
				Text string `json:"text,omitempty"`
			}{{Text: m.Content}},
		})
	}

	reqBody := geminiRequest{
		Contents: contents,
	}
	reqBody.GenerationConfig.Temperature = b.temperature
	reqBody.GenerationConfig.MaxOutputTokens = b.maxTokensOrDefault()
	if systemInstruction != nil {
		reqBody.SystemInstruction = systemInstruction
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	// Gemini API endpoint: POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, b.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result geminiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Candidates) == 0 {
		return Response{}, fmt.Errorf("no candidates in response")
	}

	// Extract content
	var content string
	for _, part := range result.Candidates[0].Content.Parts {
		content += part.Text
	}

	return Response{
		Content:          content,
		PromptTokens:     result.UsageMetadata.PromptTokenCount,
		CompletionTokens: result.UsageMetadata.CandidatesTokenCount,
	}, nil
}

// CompleteWithTools sends messages with tool definitions to Gemini.
// Gemini has native function calling support.
func (b *GeminiBackend) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	// Build function declarations for Gemini
	functionDeclarations := make([]map[string]interface{}, 0, len(toolDefs))
	for _, d := range toolDefs {
		functionDeclarations = append(functionDeclarations, map[string]interface{}{
			"name":        d.Name,
			"description": d.Description,
			"parameters":  d.Parameters,
		})
	}

	toolsConfig := []map[string]interface{}{
		{
			"functionDeclarations": functionDeclarations,
		},
	}

	// Convert messages
	var contents []geminiContent
	var systemInstruction *geminiContent

	for _, m := range messages {
		if m.Role == "system" {
			systemInstruction = &geminiContent{
				Parts: []struct {
					Text string `json:"text,omitempty"`
				}{{Text: m.Content}},
			}
			continue
		}

		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		if role == "tool" {
			role = "user"
		}

		// For tool calls, we need to include them in the content
		// Gemini uses a different format - we append function call info to the text
		content := m.Content
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				content += fmt.Sprintf("\n[Function call: %s with args: %v]", tc.ToolName, tc.Arguments)
			}
		}

		contents = append(contents, geminiContent{
			Role: role,
			Parts: []struct {
				Text string `json:"text,omitempty"`
			}{{Text: content}},
		})
	}

	reqBody := map[string]interface{}{
		"contents": contents,
		"generationConfig": map[string]interface{}{
			"temperature":     b.temperature,
			"maxOutputTokens": b.maxTokensOrDefault(),
		},
		"tools": toolsConfig,
	}

	if systemInstruction != nil {
		reqBody["systemInstruction"] = systemInstruction
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return tools.Response{}, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, b.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return tools.Response{}, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return tools.Response{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tools.Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result geminiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return tools.Response{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Candidates) == 0 {
		return tools.Response{}, fmt.Errorf("no candidates in response")
	}

	// Parse function calls
	var content string
	var calls []tools.Call
	stopReason := "stop"

	for i, part := range result.Candidates[0].Content.Parts {
		if part.Text != "" {
			content += part.Text
		}
		if part.FunctionCall != nil {
			calls = append(calls, tools.Call{
				ID:        fmt.Sprintf("call-%d", i), // Gemini doesn't provide IDs
				ToolName:  part.FunctionCall.Name,
				Arguments: part.FunctionCall.Args,
			})
			stopReason = "tool_calls"
		}
	}

	return tools.Response{
		Content:    content,
		ToolCalls:  calls,
		StopReason: stopReason,
	}, nil
}

func (b *GeminiBackend) maxTokensOrDefault() int {
	if b.maxTokens == 0 {
		return 4096
	}
	return b.maxTokens
}
