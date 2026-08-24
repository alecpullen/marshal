package limits

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	openRouterURL = "https://openrouter.ai/api/v1/models"
	liteLLMURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	fetchTimeout  = 30 * time.Second
)

// limitsHTTPClient is the dedicated HTTP client used for limit-table
// fetches. It carries its own timeout so a remote source that hangs cannot
// tie up the process-global http.DefaultClient (which has no timeout by
// default) or share connection state with unrelated calls.
var limitsHTTPClient = &http.Client{
	Timeout: fetchTimeout,
}

// Fetch retrieves a limit table from a named public source. Supported
// sources are "openrouter" and "litellm". No API keys are required.
func Fetch(ctx context.Context, source string) (map[string]Limit, error) {
	switch source {
	case "openrouter":
		return fetchURL(ctx, openRouterURL, NormalizeOpenRouter)
	case "litellm":
		return fetchURL(ctx, liteLLMURL, NormalizeLiteLLM)
	default:
		return nil, fmt.Errorf("unknown limit source %q", source)
	}
}

func fetchURL(ctx context.Context, url string, normalize func([]byte) (map[string]Limit, error)) (map[string]Limit, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := limitsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return normalize(body)
}

// NormalizeOpenRouter parses an OpenRouter /api/v1/models response body
// into a provider/model-keyed limit table.
func NormalizeOpenRouter(data []byte) (map[string]Limit, error) {
	var payload struct {
		Data []struct {
			ID                  string   `json:"id"`
			ContextLength       int      `json:"context_length"`
			SupportedParameters []string `json:"supported_parameters"`
			TopProvider         struct {
				ContextLength       int `json:"context_length"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse openrouter response: %w", err)
	}
	out := make(map[string]Limit, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		// Prefer top_provider limits; fall back to outer fields.
		cw := m.TopProvider.ContextLength
		if cw == 0 {
			cw = m.ContextLength
		}
		maxOut := m.TopProvider.MaxCompletionTokens

		var toolCalling, reasoning *bool
		if m.SupportedParameters != nil {
			supportsTools, supportsReasoning := false, false
			for _, param := range m.SupportedParameters {
				switch param {
				case "tools":
					supportsTools = true
				case "reasoning", "include_reasoning":
					supportsReasoning = true
				}
			}
			toolCalling = &supportsTools
			reasoning = &supportsReasoning
		}
		out[m.ID] = Limit{ContextWindow: cw, MaxOutputTokens: maxOut, ToolCalling: toolCalling, Reasoning: reasoning}
	}
	return out, nil
}

// intFromRaw attempts to extract an int from a json.RawMessage that may be
// a JSON number (int or float) or a non-numeric value. Returns 0 if the
// value is absent, null, a string, or otherwise unparseable.
func intFromRaw(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	// Try int first.
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	// Try float (LiteLLM sometimes uses floats like 2000000.0).
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int(f)
	}
	return 0
}

// NormalizeLiteLLM parses LiteLLM's model_prices_and_context_window.json
// into a provider/model-keyed limit table.
func NormalizeLiteLLM(data []byte) (map[string]Limit, error) {
	var raw map[string]struct {
		MaxTokens               json.RawMessage `json:"max_tokens"`
		MaxInputTokens          json.RawMessage `json:"max_input_tokens"`
		MaxOutputTokens         json.RawMessage `json:"max_output_tokens"`
		SupportsFunctionCalling *bool           `json:"supports_function_calling"`
		SupportsReasoning       *bool           `json:"supports_reasoning"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse litellm response: %w", err)
	}
	out := make(map[string]Limit, len(raw))
	for id, v := range raw {
		if id == "" {
			continue
		}
		var lim Limit
		if cw := intFromRaw(v.MaxInputTokens); cw > 0 {
			lim.ContextWindow = cw
		} else if cw := intFromRaw(v.MaxTokens); cw > 0 {
			lim.ContextWindow = cw
		}
		if mo := intFromRaw(v.MaxOutputTokens); mo > 0 {
			lim.MaxOutputTokens = mo
		} else if mo := intFromRaw(v.MaxTokens); mo > 0 {
			lim.MaxOutputTokens = mo
		}
		lim.ToolCalling = v.SupportsFunctionCalling
		lim.Reasoning = v.SupportsReasoning
		if lim.ContextWindow != 0 || lim.MaxOutputTokens != 0 || lim.ToolCalling != nil || lim.Reasoning != nil {
			out[id] = lim
		}
	}
	return out, nil
}
