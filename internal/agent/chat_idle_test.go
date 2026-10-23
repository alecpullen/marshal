package agent

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// headersBlockingProvider models the pre-stream half of a request: Chat itself
// blocks until the attempt context is cancelled, then returns the transport
// error http.Client.Do produces. No ChatEvent ever carries the failure, so the
// runner's stream-error classification never sees it.
type headersBlockingProvider struct{}

func (p *headersBlockingProvider) Name() string { return "headers-blocking" }

func (p *headersBlockingProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *headersBlockingProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *headersBlockingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	<-ctx.Done()
	return nil, &url.Error{Op: "Post", URL: "https://example.invalid/v1/chat/completions", Err: ctx.Err()}
}

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

// An idle timeout that fires before the first byte must be classified exactly
// like one that fires mid-stream. The adapter reports a cancelled request as a
// generic transport error, so without classification the turn fails with a
// bare "context canceled": isRetryableChatError refuses it and
// isReconnectEligible does not recognise it, so the ladder gives up instead of
// reconnecting.
func TestChatOnceIdleTimeoutBeforeFirstByteIsClassified(t *testing.T) {
	state := newTestState(t)
	p := &headersBlockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 50 * time.Millisecond

	_, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("chatOnce returned %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("idle timeout before first byte must not surface as a bare cancellation: %v", err)
	}
}

// The request ceiling has the same pre-stream hole: it cancels the attempt
// context, the adapter returns a transport error, and the ceiling sentinel is
// lost unless the runner maps the cause.
func TestChatOnceCeilingBeforeFirstByteIsClassified(t *testing.T) {
	state := newTestState(t)
	p := &headersBlockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 10 * time.Second
	r.RequestCeiling = 50 * time.Millisecond

	_, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if !errors.Is(err, errRequestCeiling) {
		t.Fatalf("chatOnce returned %v, want errRequestCeiling", err)
	}
}

// A genuine caller cancellation must keep its own classification: the fix for
// the two tests above must not relabel an Esc or a turn cancel as a timeout.
func TestChatOnceParentCancelBeforeFirstByteStaysCancellation(t *testing.T) {
	state := newTestState(t)
	p := &headersBlockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 10 * time.Second
	r.RequestCeiling = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := r.chatOnce(ctx, p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("chatOnce returned %v, want context.Canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller cancellation must not be relabelled as a timeout: %v", err)
	}
}

// The classified pre-stream idle timeout must be reconnect-eligible: that is
// what turns a stalled first byte into a retry instead of a dead turn. It must
// still be refused by the ordinary ladder, which would resend immediately.
func TestPreStreamIdleTimeoutIsReconnectEligible(t *testing.T) {
	state := newTestState(t)
	p := &headersBlockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 50 * time.Millisecond

	_, err := r.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err == nil {
		t.Fatal("expected an idle-timeout error")
	}
	if !isReconnectEligible(context.Background(), err) {
		t.Fatalf("pre-stream idle timeout %v must be reconnect-eligible", err)
	}
	if isRetryableChatError(err) {
		t.Fatalf("pre-stream idle timeout %v must not be resent by the ordinary ladder", err)
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

func TestWaitForConnectivityRespectsCapBeforeAttempt(t *testing.T) {
	state := newTestState(t)
	p := &blockingProvider{}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.IdleTimeout = 400 * time.Millisecond
	r.ReconnectMaxWait = 150 * time.Millisecond
	r.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	var best chatResult
	start := time.Now()
	_, err := r.waitForConnectivity(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false,
		context.DeadlineExceeded, &best)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected failure when the reconnect budget is exhausted")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("waitForConnectivity %v exceeded its 150ms cap plus one bounded attempt", elapsed)
	}
}
