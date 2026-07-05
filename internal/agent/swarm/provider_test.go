// internal/agent/swarm/provider_test.go
package swarm

import (
	"context"
	"sync"

	"marshal/internal/llm/schema"
)

// scriptedProvider mirrors the fake in internal/agent/runner_test.go: it
// returns pre-canned responses in call order and repeats the last one when
// the script runs out. Safe for concurrent Chat calls.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []string
	calls     int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *scriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *scriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *scriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.mu.Unlock()

	content := ""
	switch {
	case idx < len(p.responses):
		content = p.responses[idx]
	case len(p.responses) > 0:
		content = p.responses[len(p.responses)-1]
	}

	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}
