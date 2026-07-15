package agenttest

import (
	"context"
	"sync"

	"marshal/internal/llm/schema"
)

// ScriptedProvider returns pre-canned responses in call order. Each call to
// Chat consumes the next entry from responses/errs (whichever is non-empty
// at that index); once the scripts run out, the last response is repeated
// so tests exercising max-iteration limits do not need to script every turn.
// Safe for concurrent Chat calls.
type ScriptedProvider struct {
	mu            sync.Mutex
	Responses     []string
	ToolCalls     [][]schema.ToolCall
	FinishReasons []string
	Thinking      []string
	Errs          []error
	Usages        []*schema.TokenUsage
	Calls         int
	Requests      []schema.ChatRequest
	ProviderCaps  schema.ProviderCapabilities
	OnChat        func(idx int, req schema.ChatRequest)
}

func (p *ScriptedProvider) Name() string { return "scripted" }

func (p *ScriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *ScriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *ScriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.ProviderCaps
}

func (p *ScriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	idx := p.Calls
	p.Requests = append(p.Requests, req)
	p.Calls++
	p.mu.Unlock()

	if p.OnChat != nil {
		p.OnChat(idx, req)
	}

	ch := make(chan schema.ChatEvent, 3)
	if idx < len(p.Thinking) && p.Thinking[idx] != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.Thinking[idx]}
	}

	if idx < len(p.Errs) && p.Errs[idx] != nil {
		ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.Errs[idx]}
		close(ch)
		return ch, nil
	}

	content := ""
	switch {
	case idx < len(p.Responses):
		content = p.Responses[idx]
	case len(p.Responses) > 0:
		content = p.Responses[len(p.Responses)-1]
	}
	if content != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	}
	done := schema.ChatEvent{Type: schema.ChatEventDone}
	if idx < len(p.Usages) {
		done.Usage = p.Usages[idx]
	}
	if idx < len(p.ToolCalls) {
		done.ToolCalls = p.ToolCalls[idx]
	}
	if idx < len(p.FinishReasons) {
		done.FinishReason = p.FinishReasons[idx]
	}
	ch <- done
	close(ch)
	return ch, nil
}
