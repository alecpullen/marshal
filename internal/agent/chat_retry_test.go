package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestChatWithRetryStopsOnNonRetryable4xx(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs: []error{&provider.ProviderError{Provider: "test", StatusCode: 400, Body: "bad request"}},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 2

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if p.Calls != 1 {
		t.Fatalf("expected 1 call for non-retryable 4xx, got %d", p.Calls)
	}
	var pe *provider.ProviderError
	if !errors.As(err, &pe) || pe.StatusCode != 400 {
		t.Fatalf("expected HTTP 400 provider error, got %v", err)
	}
}

func TestChatWithRetryStopsOnContextCancel(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs: []error{context.Canceled},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 2

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if p.Calls != 1 {
		t.Fatalf("expected 1 call for context.Canceled, got %d", p.Calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestChatWithRetryRetriesTransientError(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{errors.New("connection reset"), errors.New("timeout"), nil},
		Responses: []string{"", "", `{"rationale":"ok","action":{"type":"answer","content":"hi"}}`},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 2

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("expected 3 calls (MaxRetries+1), got %d", p.Calls)
	}
}

func TestChatWithRetryRetries429(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{&provider.ProviderError{Provider: "test", StatusCode: 429, Body: "rate limited"}, nil},
		Responses: []string{"", `{"rationale":"ok","action":{"type":"answer","content":"hi"}}`},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 2

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after 429 retry, got %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("expected 2 calls for retryable 429, got %d", p.Calls)
	}
}

// TestIsNetworkErrorClassifiesTransportFailures pins the classifier the
// reconnect loop depends on: connection-shaped errors (the ones a Wi-Fi
// drop, VPN reconnect, or sleep/wake actually produces) must be recognised
// whether they surface bare, wrapped in fmt.Errorf (the provider adds
// "chat request failed: %w" / "read stream: %w"), or nested in *url.Error.
func TestIsNetworkErrorClassifiesTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"url_error", &url.Error{Op: "Post", URL: "http://x", Err: syscall.ECONNREFUSED}, true},
		{"wrapped_url_error", fmt.Errorf("provider %q: chat request failed: %w", "openai", &url.Error{Op: "Post", URL: "http://x", Err: syscall.ECONNRESET}), true},
		{"op_error", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"dns_error", &net.DNSError{Err: "no such host", IsNotFound: true}, true},
		{"conn_refused", syscall.ECONNREFUSED, true},
		{"conn_reset", syscall.ECONNRESET, true},
		{"timed_out", syscall.ETIMEDOUT, true},
		{"net_unreachable", syscall.ENETUNREACH, true},
		{"host_unreachable", syscall.EHOSTUNREACH, true},
		{"broken_pipe", syscall.EPIPE, true},
		{"eof", io.EOF, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"wrapped_unexpected_eof", fmt.Errorf("read stream: %w", io.ErrUnexpectedEOF), true},
		{"http_400", &provider.ProviderError{Provider: "t", StatusCode: 400, Body: "bad"}, false},
		{"http_500", &provider.ProviderError{Provider: "t", StatusCode: 500, Body: "oops"}, false},
		{"http_429", &provider.ProviderError{Provider: "t", StatusCode: 429, Body: "slow down"}, false},
		{"context_canceled", context.Canceled, false},
		{"context_deadline", context.DeadlineExceeded, false},
		{"wrapped_canceled", fmt.Errorf("chat request failed: %w", context.Canceled), false},
		{"plain_error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNetworkError(tc.err); got != tc.want {
				t.Fatalf("isNetworkError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// instantSleep makes the reconnect backoff instantaneous in tests while
// still honouring context cancellation.
func instantSleep(r *Runner) {
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	}
}

// TestChatWithRetryResumesAfterNetworkDrop is the regression test for the
// reported bug: a dropped connection used to burn the whole retry budget in
// ~150ms and fail the turn even when the network came back a moment later.
// Network errors must enter the wait-for-connectivity loop instead of the
// MaxRetries ladder, and the same request must be resent until it succeeds.
func TestChatWithRetryResumesAfterNetworkDrop(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
	p := &agenttest.ScriptedProvider{
		// Three consecutive drops — more than MaxRetries — then recovery.
		Errs:      []error{drop, drop, drop, nil},
		Responses: []string{"", "", "", `{"rationale":"ok","action":{"type":"answer","content":"hi"}}`},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0 // the normal ladder must not be involved at all
	instantSleep(r)

	res, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected turn to resume after reconnection, got %v", err)
	}
	if res.Text == "" {
		t.Fatal("expected the recovered response text, got empty")
	}
	if p.Calls != 4 {
		t.Fatalf("expected 4 calls (3 drops + 1 success), got %d", p.Calls)
	}
}

// TestChatWithRetryReconnectSurfacesStatus verifies the user can see what
// is happening: while waiting for connectivity the runner must publish a
// reconnecting activity instead of leaving the UI on a stale 'thinking'.
func TestChatWithRetryReconnectSurfacesStatus(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNREFUSED}
	p := &agenttest.ScriptedProvider{
		Errs:      []error{drop, nil},
		Responses: []string{"", `{"rationale":"ok","action":{"type":"answer","content":"hi"}}`},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0

	seen := make(chan session.Activity, 4)
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		seen <- state.Activity()
		return ctx.Err()
	}

	if _, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("expected resume after reconnect, got %v", err)
	}
	select {
	case a := <-seen:
		if a.Kind != session.ActivityReconnecting {
			t.Fatalf("activity kind during reconnect = %q, want %q", a.Kind, session.ActivityReconnecting)
		}
	default:
		t.Fatal("reconnect sleep never ran — no activity observed")
	}
}

// TestChatWithRetryReconnectHonoursCancel ensures a user pressing Esc (or a
// turn timeout) while waiting for the network aborts immediately instead of
// sitting in the reconnect loop until the overall cap.
func TestChatWithRetryReconnectHonoursCancel(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
	p := &agenttest.ScriptedProvider{Errs: []error{drop}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0

	ctx, cancel := context.WithCancel(context.Background())
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		cancel() // user gives up during the first wait
		return ctx.Err()
	}

	_, err := r.chatWithRetry(ctx, p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from reconnect wait, got %v", err)
	}
	if p.Calls != 1 {
		t.Fatalf("expected 1 call before cancellation, got %d", p.Calls)
	}
}

// TestChatWithRetryReconnectGivesUpAfterCap bounds the wait: if the network
// never comes back the turn must still fail (with the last network error)
// rather than hanging forever.
func TestChatWithRetryReconnectGivesUpAfterCap(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
	// Always fails, every call — the outage that never ends. (ScriptedProvider
	// would repeat its last *response* once Errs run out, faking a recovery.)
	p := &partialThenErrorProvider{partial: "", err: drop}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0

	// Fake clock: each sleep advances the wall clock by the slept duration so
	// the overall reconnect cap is reached without real waiting.
	now := time.Unix(1000, 0)
	r.Now = func() time.Time { return now }
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		return ctx.Err()
	}

	started := now
	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error after reconnect cap, got nil")
	}
	if !isNetworkError(err) {
		t.Fatalf("expected the underlying network error, got %v", err)
	}
	// The loop gives up once the next backoff would push total waiting past
	// the cap, so the observed wait lands within one backoff step under it.
	if elapsed := now.Sub(started); elapsed < reconnectMaxWait-10*time.Second {
		t.Fatalf("gave up after %v, well before the %v cap", elapsed, reconnectMaxWait)
	}
	if p.calls < 5 {
		t.Fatalf("expected several reconnect attempts over ~2m, got %d", p.calls)
	}
}

// TestChatWithRetryReconnectEscalatesToRetryLadder: if the connection comes
// back but the provider then returns an HTTP error, that error is not a
// network problem — it must leave the reconnect loop and follow the normal
// retry/fail path.
func TestChatWithRetryReconnectEscalatesToRetryLadder(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
	http400 := &provider.ProviderError{Provider: "test", StatusCode: 400, Body: "bad request"}
	p := &agenttest.ScriptedProvider{Errs: []error{drop, http400}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 2
	instantSleep(r)

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	var pe *provider.ProviderError
	if !errors.As(err, &pe) || pe.StatusCode != 400 {
		t.Fatalf("expected HTTP 400 to surface, got %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("expected 2 calls (drop + 400), got %d", p.Calls)
	}
}
