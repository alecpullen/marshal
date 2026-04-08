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
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAICompatible(name, baseURL, apiKey string) *OpenAICompatibleBackend {
	return &OpenAICompatibleBackend{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{},
	}
}

func (b *OpenAICompatibleBackend) Name() string { return b.name }

func (b *OpenAICompatibleBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	reqBody, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
	}{model, messages, 1024})
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		b.baseURL+"/chat/completions",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.client.Do(req)
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
