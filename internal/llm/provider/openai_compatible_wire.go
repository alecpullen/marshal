package provider

import (
	"encoding/json"

	"marshal/internal/llm/schema"
)

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type chatMessageBody struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []toolCallBody `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type usageBody struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// OpenAI usage detail breakdowns.
	PromptTokensDetails     *tokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *tokenDetails `json:"completion_tokens_details,omitempty"`
	// DeepSeek extends the top-level usage object directly (not under
	// _details). Pointers so absent = nil: a 0 cache hit is meaningful
	// and distinct from "not reported".
	PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens,omitempty"`
}

// tokenDetails holds the OpenAI usage detail breakdowns.
type tokenDetails struct {
	CachedTokens    int `json:"cached_tokens"`    // prompt cache hits
	ReasoningTokens int `json:"reasoning_tokens"` // reasoning model output
}

type chatCompletionRequestBody struct {
	Model           string                 `json:"model"`
	Messages        []chatMessageBody      `json:"messages"`
	Stream          bool                   `json:"stream"`
	Temperature     *float64               `json:"temperature,omitempty"`
	TopP            *float64               `json:"top_p,omitempty"`
	MaxTokens       *int                   `json:"max_tokens,omitempty"`
	Stop            []string               `json:"stop,omitempty"`
	ResponseFormat  *schema.ResponseFormat `json:"response_format,omitempty"`
	Tools           []toolDefinitionBody   `json:"tools,omitempty"`
	ToolChoice      string                 `json:"tool_choice,omitempty"`
	StreamOptions   *streamOptions         `json:"stream_options,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
}

type toolDefinitionBody struct {
	Type     string           `json:"type"`
	Function toolFunctionBody `json:"function"`
}

type toolFunctionBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type toolCallBody struct {
	ID       string           `json:"id,omitempty"`
	Index    int              `json:"index,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function toolFunctionBody `json:"function"`
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
			// OpenRouter conventions: a plain reasoning string, or
			// structured reasoning_details entries carrying text.
			Reasoning        string `json:"reasoning"`
			ReasoningDetails []struct {
				Text string `json:"text"`
			} `json:"reasoning_details"`
			ToolCalls []toolCallBody `json:"tool_calls"`
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
