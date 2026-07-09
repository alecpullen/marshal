package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

// recordingProvider is a scripted provider that also records Chat calls.
type recordingProvider struct {
	responses []string
	calls     int
	// block, if non-nil, is closed by the test to release a pending Chat.
	block chan struct{}
}

func (r *recordingProvider) Name() string { return "rec" }
func (r *recordingProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (r *recordingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	idx := r.calls
	r.calls++
	resp := "title"
	if idx < len(r.responses) {
		resp = r.responses[idx]
	}
	ch := make(chan schema.ChatEvent, 2)
	if r.block != nil {
		// Block until the test closes `block` so the test can race a
		// SetTitleManual call in between the early-return check and
		// the pre-write re-check.
		<-r.block
	}
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: resp}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}
func (r *recordingProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}
func (r *recordingProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func TestGenerateTitleStoresNonEmptyTitle(t *testing.T) {
	p := &recordingProvider{responses: []string{"Fix parser bug"}}
	state := newTestState(t)
	tg := &titleGenerator{
		provider: p,
		model:    "tiny",
		state:    state,
		timeout:  2 * time.Second,
	}
	tg.generate(context.Background(), "where is the parser?")

	if got := state.Title(); got != "Fix parser bug" {
		t.Fatalf("title = %q, want %q", got, "Fix parser bug")
	}
}

func TestGenerateTitleSkipsWhenTitleAlreadySetManually(t *testing.T) {
	p := &recordingProvider{responses: []string{"auto"}}
	state := newTestState(t)
	state.SetTitleManual("my manual title")
	tg := &titleGenerator{provider: p, model: "tiny", state: state, timeout: time.Second}
	tg.generate(context.Background(), "anything")
	if got := state.Title(); got != "my manual title" {
		t.Fatalf("manual title overwritten: %q", got)
	}
	if p.calls != 0 {
		t.Fatalf("provider called %d times, want 0 (manual title should skip)", p.calls)
	}
}

func TestGenerateTitleTruncatesOverlong(t *testing.T) {
	long := strings.Repeat("a", 120)
	p := &recordingProvider{responses: []string{long}}
	state := newTestState(t)
	tg := &titleGenerator{provider: p, model: "tiny", state: state, timeout: time.Second}
	tg.generate(context.Background(), "q")
	if got := state.Title(); len(got) > 50 {
		t.Fatalf("title not truncated: %d chars", len(got))
	}
}

func TestGenerateTitleDoesNotOverwriteConcurrentRename(t *testing.T) {
	block := make(chan struct{})
	p := &recordingProvider{responses: []string{"auto title"}, block: block}
	state := newTestState(t)

	tg := &titleGenerator{
		provider: p,
		model:    "tiny",
		state:    state,
		timeout:  2 * time.Second,
	}

	// generate() runs synchronously in the test goroutine. The provider
	// blocks inside Chat, so we can race a /rename between the early-return
	// check (line 45 of title.go) and the SetTitle call. The only thing
	// protecting the manual title is the new pre-write re-check.
	done := make(chan struct{})
	go func() {
		tg.generate(context.Background(), "q")
		close(done)
	}()

	// Wait until the provider has been entered (so we know we are past the
	// early-return check), then issue the rename and release the provider.
	for p.calls == 0 {
		time.Sleep(time.Millisecond)
	}
	state.SetTitleManual("manual")
	close(block)

	<-done

	if got := state.Title(); got != "manual" {
		t.Fatalf("title = %q, want %q (manual title overwritten by auto-title)", got, "manual")
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (test should have passed the early-return guard)", p.calls)
	}
}

func TestGenerateTitleFailureLeavesFallback(t *testing.T) {
	p := &failProvider{}
	state := newTestState(t)
	state.SetTitle("fallback") // simulate an existing fallback name
	tg := &titleGenerator{provider: p, model: "tiny", state: state, timeout: time.Second}
	tg.generate(context.Background(), "q")
	if got := state.Title(); got != "fallback" {
		t.Fatalf("title changed on failure: %q", got)
	}
}

type failProvider struct{ recordingProvider }

func (p *failProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, errors.New("boom")
}

// Ensure provider.Provider is still satisfied by the test fakes (compile-time).
var (
	_ provider.Provider = (*recordingProvider)(nil)
	_ provider.Provider = (*failProvider)(nil)
)
