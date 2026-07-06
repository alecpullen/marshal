package provider

import "marshal/internal/llm/schema"

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type chatMessageBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type usageBody struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionRequestBody struct {
	Model          string                 `json:"model"`
	Messages       []chatMessageBody      `json:"messages"`
	Stream         bool                   `json:"stream"`
	Temperature    *float64               `json:"temperature,omitempty"`
	TopP           *float64               `json:"top_p,omitempty"`
	MaxTokens      *int                   `json:"max_tokens,omitempty"`
	Stop           []string               `json:"stop,omitempty"`
	ResponseFormat *schema.ResponseFormat `json:"response_format,omitempty"`
	StreamOptions  *streamOptions         `json:"stream_options,omitempty"`
}

// chatCompletionChunk is a single SSE `data:` payload for streaming
// responses: choices[0].delta.content, plus the reasoning_content
// convention used by DeepSeek-R1-style reasoning models served over an
// OpenAI-compatible endpoint (vLLM, Ollama, etc.).
type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError  `json:"error"`
	Usage *usageBody `json:"usage,omitempty"`
}

// chatCompletionResponse is the full non-streaming response body:
// choices[0].message.content.
type chatCompletionResponse struct {
	Choices []struct {
		Message      chatMessageBody `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError  `json:"error"`
	Usage *usageBody `json:"usage,omitempty"`
}

type modelsResponseBody struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

type embedRequestBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponseBody struct {
	Model string `json:"model"`
	Data  []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *apiError `json:"error"`
}
