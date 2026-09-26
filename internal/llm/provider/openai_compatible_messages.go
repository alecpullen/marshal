package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"marshal/internal/llm/schema"
)

// messagesChat posts to the Anthropic Messages API (/messages) — the
// wire protocol OpenCode Go serves the minimax and qwen families on.
// Body construction and both readers are shared with the native
// Anthropic provider (anthropic.go); only the URL and auth differ:
// Bearer via setHeaders (plus the Phase 1 x-opencode-session and
// marshal User-Agent), not x-api-key. The anthropic-version header is
// still sent — @ai-sdk/anthropic (OpenCode's own client) sends it
// against this gateway too.
//
// thinkingBudget/thinkingBudgetMargin are 0/0: the Go path has no
// provider-level thinking knob, so extended thinking activates only
// when the request's Thinking field asks for it (low/medium/high map
// to budget_tokens inside buildAnthropicRequestBody). Reuse brings
// max_tokens (required by the API), prompt-cache breakpoints on the
// system prompt and last tool definition, and the cache usage mapping
// for free.
func (p *OpenAICompatible) messagesChat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	body, err := buildAnthropicRequestBody(req, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}
	writeRequestCapture(p.name, body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+endpointMessages, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, &RequestError{Provider: p.name, Op: "chat request failed", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, HTTPError(p.name, resp)
	}

	events := make(chan schema.ChatEvent)
	capture := newWireCapture(p.name)
	if req.Stream {
		go streamAnthropicChatEvents(capture.wrap(resp.Body), capture, events)
	} else {
		go readAnthropicChatResponse(capture.wrap(resp.Body), events)
	}
	return events, nil
}
