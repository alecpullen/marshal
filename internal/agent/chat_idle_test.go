package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// trickleProvider emits a thinking delta every interval, then a done event.
// It models a reasoning model that is working but produces no content.
type trickleProvider struct {
	interval time.Duration
	count    int
}

func (p *trickleProvider) Name() string { return "trickle" }

func (p *trickleProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *trickleProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *trickleProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	events := make(chan schema.ChatEvent, 1)
	go func() {
		defer close(events)
		for i := 0; i < p.count; i++ {
			select {
			case <-ctx.Done():
				events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
				return
			case <-time.After(p.interval):
			}
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: "thinking "}
		}
		events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: "stop"}
	}()
	return events, nil
}

func TestChatOnceIdleTimeoutFiresOnSilentStream(t *testing.T) {
	state := newTestState(t)
	p := &blockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("chatOnce returned %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("idle timeout took too long: %v", elapsed)
	}
}

func TestChatOnceIdleTimeoutResetsOnThinkingDeltas(t *testing.T) {
	state := newTestState(t)
	p := &trickleProvider{interval: 20 * time.Millisecond, count: 10}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 50 * time.Millisecond

	res, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("chatOnce err = %v, want success: reasoning deltas must reset the idle timer", err)
	}
	if res.Text != "" {
		t.Fatalf("res.Text = %q, want empty (thinking only)", res.Text)
	}
}

func TestChatOnceCeilingFiresDespiteActivity(t *testing.T) {
	state := newTestState(t)
	p := &trickleProvider{interval: 5 * time.Millisecond, count: 1000}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 50 * time.Millisecond
	r.RequestCeiling = 150 * time.Millisecond

	_, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if !errors.Is(err, errRequestCeiling) {
		t.Fatalf("chatOnce returned %v, want errRequestCeiling", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ceiling error must not be DeadlineExceeded: %v", err)
	}
}

func TestRequestCeilingIsNotReconnectEligible(t *testing.T) {
	if isReconnectEligible(context.Background(), errRequestCeiling) {
		t.Fatal("errRequestCeiling must not be reconnect-eligible")
	}
}
