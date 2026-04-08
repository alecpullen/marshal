package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
