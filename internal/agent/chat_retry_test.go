package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
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
	if elapsed := now.Sub(started); elapsed < defaultReconnectMaxWait-10*time.Second {
		t.Fatalf("gave up after %v, well before the %v cap", elapsed, defaultReconnectMaxWait)
	}
	if p.calls < 5 {
		t.Fatalf("expected several reconnect attempts over ~2m, got %d", p.calls)
	}
}

// TestReconnectMaxWaitConfigurable verifies a Runner.ReconnectMaxWait
// override bounds the reconnect loop instead of the built-in default: with
// the field set small (via the suite's fake clock) the loop must give up at
// the configured cap, not the 2-minute default.
func TestReconnectMaxWaitConfigurable(t *testing.T) {
	drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
	p := &partialThenErrorProvider{partial: "", err: drop}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0
	r.ReconnectMaxWait = 7 * time.Second

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
		t.Fatal("expected error after configured reconnect cap, got nil")
	}
	if !isNetworkError(err) {
		t.Fatalf("expected the underlying network error, got %v", err)
	}
	elapsed := now.Sub(started)
	// The configured cap is far below the 2-minute default, so the observed
	// wait must track it, not defaultReconnectMaxWait.
	if elapsed >= defaultReconnectMaxWait {
		t.Fatalf("reconnect ran %v, ignoring the %v configured cap", elapsed, r.ReconnectMaxWait)
	}
	if elapsed < r.ReconnectMaxWait-10*time.Second {
		t.Fatalf("gave up after %v, well before the %v configured cap", elapsed, r.ReconnectMaxWait)
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

// deadlineThenSuccessProvider blocks its first Chat call until the (per-request)
// ctx deadline fires, returning that DeadlineExceeded; subsequent calls succeed.
// This mirrors a device sleep or stalled stream: chatOnce's WithTimeout fires,
// the parent ctx is still alive, and the request must be reconnected rather
// than failed.
type deadlineThenSuccessProvider struct {
	calls int
}

func (p *deadlineThenSuccessProvider) Name() string { return "deadline-then-success" }

func (p *deadlineThenSuccessProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *deadlineThenSuccessProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *deadlineThenSuccessProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.calls++
	events := make(chan schema.ChatEvent, 1)
	go func() {
		defer close(events)
		if p.calls == 1 {
			<-ctx.Done()
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
			return
		}
		events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: "stop"}
	}()
	return events, nil
}

// TestChatWithRetryReconnectOnPerRequestDeadline pins that a per-request
// DeadlineExceeded (parent ctx alive) is reconnect-eligible: the request is
// resent via waitForConnectivity and the eventual success wins, with the
// ActivityReconnecting status surfaced meanwhile.
func TestChatWithRetryReconnectOnPerRequestDeadline(t *testing.T) {
	p := &deadlineThenSuccessProvider{}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0
	r.RequestCeiling = 50 * time.Millisecond

	seen := make(chan session.Activity, 4)
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		seen <- state.Activity()
		return ctx.Err()
	}

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected recovery via reconnect after per-request deadline, got %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 calls (deadline + success), got %d", p.calls)
	}
	select {
	case a := <-seen:
		if a.Kind != session.ActivityReconnecting {
			t.Fatalf("activity kind during reconnect = %q, want %q", a.Kind, session.ActivityReconnecting)
		}
	default:
		t.Fatal("reconnect path never ran — no ActivityReconnecting surfaced")
	}
}

// TestChatWithRetryParentCancelNotReconnectEligible pins the ctx.Err()==nil
// guard in isReconnectEligible: a DeadlineExceeded while the parent ctx is
// already cancelled is a user/turn cancel, not a reconnect — the turn must
// fail immediately with no reconnect wait.
func TestChatWithRetryParentCancelNotReconnectEligible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // parent already cancelled before the call

	p := &deadlineThenSuccessProvider{}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0
	r.RequestCeiling = 50 * time.Millisecond

	// A Sleep that would record a reconnect attempt, proving none happens.
	slept := false
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		slept = true
		return ctx.Err()
	}

	_, err := r.chatWithRetry(ctx, p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if slept {
		t.Fatal("reconnect wait ran for a cancelled parent ctx — DeadlineExceeded must not be reconnect-eligible here")
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 call, got %d", p.calls)
	}
}

// --- Task 8: cancel logical model calls without retry or salvage ----------

// steeringModelFixture opens one eligible steering turn on a fresh state and
// returns the state with the runner's steeringTurnID set, which is what makes
// a model call interruptible. It is the seam Task 9 wires in production: the
// turn entry point that opens the eligible turn registers its ID here.
type steeringModelFixture struct {
	state  *session.State
	turnID string
}

func newSteeringModelFixture(t *testing.T) *steeringModelFixture {
	t.Helper()
	state := newTestState(t)
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
	})
	turnID, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	return &steeringModelFixture{state: state, turnID: turnID}
}

// runner builds a runner whose model calls belong to the eligible turn.
func (f *steeringModelFixture) runner(t *testing.T, p provider.Provider) *Runner {
	t.Helper()
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), f.state, "test-model")
	r.steeringTurnID = f.turnID
	return r
}

// interrupt accepts one Send now request against the runner's open operation.
// The operation's interruption worker cancels the operation context, exactly
// as the production handler does.
func (f *steeringModelFixture) interrupt(t *testing.T) {
	t.Helper()
	if _, err := f.state.SubmitSendNow(session.SendNowRequest{
		TurnID:    f.turnID,
		RequestID: "r1",
		Text:      "stop this call",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
}

// TestSteeringCancelsModelStreamWithoutRetryOrSalvage pins the model-operation
// interruption contract end to end: a request whose stream ends on the
// operation context terminates the turn's own signal immediately, with no
// retry and nothing salvaged. The partial text and tool call the stream had
// already produced must not reach the caller — it was never accepted by the
// turn — while the usage the provider reported is real and is kept.
func TestSteeringCancelsModelStreamWithoutRetryOrSalvage(t *testing.T) {
	f := newSteeringModelFixture(t)

	// A test-owned stream that ends the way a real cancelled request does: it
	// waits for its request context and reports the cancellation as a stream
	// error, the shape a body read failing mid-decode produces. It also proves
	// the drain contract — two trailing sends on an unbuffered channel, which a
	// break would leave blocked forever.
	p := &steeringStreamProvider{
		partial:   `{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		usage:     &schema.TokenUsage{PromptTokens: 120, CompletionTokens: 7},
		toolCalls: []schema.ToolCall{{ID: "tc1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}},
		ready:     make(chan struct{}, 1),
		trailing:  make(chan struct{}, 1),
	}
	r := f.runner(t, p)
	r.MaxRetries = 3
	var observed *schema.TokenUsage
	r.UsageObserver = func(u schema.TokenUsage) { observed = &u }

	type outcome struct {
		res   chatResult
		err   error
		calls int
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.chatWithRetry(context.Background(), p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
		done <- outcome{res: res, err: err, calls: p.calls()}
	}()

	// Interrupt once the operation is open and its dispatch has begun.
	p.awaitDispatch(t)
	f.interrupt(t)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chatWithRetry never returned: the cancelled stream was not drained")
	}
	if !errors.Is(got.err, session.ErrSendNow) {
		t.Fatalf("err = %v, want ErrSendNow", got.err)
	}
	if got.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 — an interrupted request is never resent", got.calls)
	}
	if got.res.Text != "" || len(got.res.ToolCalls) != 0 {
		t.Fatalf("interrupted call returned executable output: text=%q tools=%v", got.res.Text, got.res.ToolCalls)
	}
	// The trailing-event signal fired, so the range drained rather than broke
	// and the provider's sender goroutine was not leaked.
	select {
	case <-p.trailing:
	case <-time.After(time.Second):
		t.Fatal("trailing events never consumed: the range broke instead of draining")
	}
	// Known usage stays recorded even though the response was discarded.
	if observed == nil || observed.PromptTokens != 120 {
		t.Fatalf("UsageObserver saw %+v, want the provider-reported 120 prompt tokens", observed)
	}
}

// steeringStreamProvider is the provider shape a real backend presents: a
// test-owned stream that reports a partial response, then ends on its request
// context the way a cancelled body read does — a stream error for the
// cancellation, followed by two trailing events that a broken range would
// never consume.
type steeringStreamProvider struct {
	partial   string
	usage     *schema.TokenUsage
	toolCalls []schema.ToolCall
	// err is injected as the stream's error before the request waits, so a test
	// can prove a genuine provider failure racing an interruption keeps its own
	// classification.
	err error
	// cleanEnd makes the stream report its response and finish without error,
	// the shape a provider that answered a request the turn had already
	// abandoned presents.
	cleanEnd bool

	mu       sync.Mutex
	count    int
	ready    chan struct{}
	trailing chan struct{}
}

func (p *steeringStreamProvider) Name() string { return "steering-stream" }

func (p *steeringStreamProvider) Models(context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *steeringStreamProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *steeringStreamProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

func (p *steeringStreamProvider) awaitDispatch(t *testing.T) {
	t.Helper()
	select {
	case <-p.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("the model request never reached the provider")
	}
}

func (p *steeringStreamProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	p.count++
	p.mu.Unlock()
	if p.ready != nil {
		select {
		case p.ready <- struct{}{}:
		default:
		}
	}

	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.partial}
		if p.err != nil {
			// A genuine failure raised before the interruption was even
			// submitted: the stream ends here rather than at ctx.Done().
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.err}
			if p.trailing != nil {
				select {
				case p.trailing <- struct{}{}:
				default:
				}
			}
			return
		}
		<-ctx.Done()
		if p.cleanEnd {
			// The request the turn already abandoned still got an answer: a
			// complete response with no error on the end at all. It reaches
			// chatOnce after the interruption, which is exactly the race this
			// case is here to pin.
			events <- schema.ChatEvent{Type: schema.ChatEventDone, Usage: p.usage, ToolCalls: p.toolCalls}
		} else {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
			events <- schema.ChatEvent{Type: schema.ChatEventDone, Usage: p.usage, ToolCalls: p.toolCalls}
		}
		// Two trailing sends: the second is what distinguishes a drained range
		// from a broken one (see chat_thinkloop_test.go's ctxClosingProvider).
		events <- schema.ChatEvent{Type: schema.ChatEventDone}
		events <- schema.ChatEvent{Type: schema.ChatEventDone}
		if p.trailing != nil {
			select {
			case p.trailing <- struct{}{}:
			default:
			}
		}
	}()
	return events, nil
}

// TestSteeringStopsConnectivityAndFallbackRetries pins that an explicit
// interruption ends the transport ladder wherever it stands: the retry
// backoff, the wait-for-connectivity loop, and the tool-less fallback all stop
// without sending another request. The fallback case matters most — a provider
// latched to tool-less calls because the user typed would lose the loop's
// prompt cache for the rest of the session.
func TestSteeringStopsConnectivityAndFallbackRetries(t *testing.T) {
	// interruptOnSleep is the shared stub: the first backoff of the transport
	// ladder accepts the user's steering and then waits for the operation to be
	// cancelled, exactly as the interruption worker's cancel does.
	interruptOnSleep := func(t *testing.T, f *steeringModelFixture) func(context.Context, time.Duration) error {
		t.Helper()
		return func(ctx context.Context, d time.Duration) error {
			f.interrupt(t)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Error("the operation context was never cancelled by the interruption worker")
			}
			return ctx.Err()
		}
	}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}

	t.Run("retry ladder stops", func(t *testing.T) {
		f := newSteeringModelFixture(t)
		p := &agenttest.ScriptedProvider{Errs: []error{errors.New("connection reset by peer")}}
		r := f.runner(t, p)
		r.MaxRetries = 3
		r.Sleep = interruptOnSleep(t, f)

		if _, err := r.chatWithRetry(context.Background(), p, "test-model", msgs, nil); !errors.Is(err, session.ErrSendNow) {
			t.Fatalf("err = %v, want ErrSendNow", err)
		}
		if p.Calls != 1 {
			t.Fatalf("provider calls = %d, want 1 — no retry after an explicit interception", p.Calls)
		}
	})

	t.Run("reconnect loop stops", func(t *testing.T) {
		f := newSteeringModelFixture(t)
		drop := &url.Error{Op: "Post", URL: "http://provider", Err: syscall.ECONNRESET}
		p := &agenttest.ScriptedProvider{Errs: []error{drop}}
		r := f.runner(t, p)
		r.MaxRetries = 0
		r.Sleep = interruptOnSleep(t, f)

		if _, err := r.chatWithRetry(context.Background(), p, "test-model", msgs, nil); !errors.Is(err, session.ErrSendNow) {
			t.Fatalf("err = %v, want ErrSendNow", err)
		}
		if p.Calls != 1 {
			t.Fatalf("provider calls = %d, want 1 — no reconnect storm after an explicit interception", p.Calls)
		}
	})

	t.Run("tools-disabled fallback is not latched", func(t *testing.T) {
		f := newSteeringModelFixture(t)
		p := &steeringStreamProvider{
			ready:    make(chan struct{}, 1),
			trailing: make(chan struct{}, 1),
		}
		r := f.runner(t, p)
		r.NativeTools = true

		done := make(chan error, 1)
		go func() {
			_, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", msgs, nil)
			done <- err
		}()
		p.awaitDispatch(t)
		f.interrupt(t)

		select {
		case err := <-done:
			if !errors.Is(err, session.ErrSendNow) {
				t.Fatalf("err = %v, want ErrSendNow", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("chatWithRetryNoNativeTools never returned")
		}
		if p.calls() != 1 {
			t.Fatalf("provider calls = %d, want 1 — the fallback must not resend", p.calls())
		}
		if r.toolChoiceNoneRejected(p.Name()) {
			t.Fatal("a user interruption latched the provider to tool-less calls; the fallback must not treat it as a tool_choice failure")
		}
	})
}

// TestSteeringModelUsageAndInspectionAreRetained pins that the interruption
// discards the response without destroying the evidence around it: a stream
// that ended cleanly because the turn was already abandoned still contributes
// its provider-reported usage, and the inspection snapshot records the attempt
// as cancelled rather than as a completed request the user never saw.
func TestSteeringModelUsageAndInspectionAreRetained(t *testing.T) {
	f := newSteeringModelFixture(t)
	p := &steeringStreamProvider{
		partial:   `{"rationale":"r","action":{"type":"answer","content":"never seen"}}`,
		usage:     &schema.TokenUsage{PromptTokens: 321, CompletionTokens: 9},
		toolCalls: []schema.ToolCall{{ID: "tc1", Name: "file.read", Args: json.RawMessage(`{}`)}},
		cleanEnd:  true,
		ready:     make(chan struct{}, 1),
	}
	r := f.runner(t, p)
	var observed *schema.TokenUsage
	r.UsageObserver = func(u schema.TokenUsage) { observed = &u }

	type outcome struct {
		res chatResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.chatWithRetry(context.Background(), p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
		done <- outcome{res: res, err: err}
	}()
	p.awaitDispatch(t)
	f.interrupt(t)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chatWithRetry never returned")
	}
	if !errors.Is(got.err, session.ErrSendNow) {
		t.Fatalf("err = %v, want ErrSendNow", got.err)
	}
	if got.res.Text != "" || len(got.res.ToolCalls) != 0 {
		t.Fatalf("an abandoned response was handed back: text=%q tools=%v", got.res.Text, got.res.ToolCalls)
	}
	if observed == nil || observed.PromptTokens != 321 {
		t.Fatalf("UsageObserver saw %+v, want the provider-reported 321 prompt tokens", observed)
	}
	snap, ok := f.state.RequestInspection()
	if !ok {
		t.Fatal("no request inspection snapshot recorded")
	}
	if snap.Outcome.Status != session.InspectionCancelled {
		t.Fatalf("inspection status = %q, want %q", snap.Outcome.Status, session.InspectionCancelled)
	}
}

// TestSteeringSubmissionPersistsAndAcknowledgesOnce pins the acceptance /
// submission split at the only instant delivery is true: the real chat
// dispatch. Staging writes nothing — otherwise the text would sit in replay
// history while still waiting in the follow-up queue — and the retry that
// follows finds no unsubmitted IDs, so the transcript keeps exactly one copy.
func TestSteeringSubmissionPersistsAndAcknowledgesOnce(t *testing.T) {
	f := newSteeringModelFixture(t)
	if _, err := f.state.SubmitSendNow(session.SendNowRequest{
		TurnID:    f.turnID,
		RequestID: "r1",
		Text:      "also update the README",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	batch, err := f.state.StageSteering(f.turnID)
	if err != nil {
		t.Fatalf("StageSteering: %v", err)
	}
	if len(batch.Messages) != 1 {
		t.Fatalf("staged batch = %+v, want the queued message", batch)
	}
	// Staging is not delivery: nothing is in the transcript yet.
	if n := countSteeringMessages(f.state); n != 0 {
		t.Fatalf("staging persisted %d steering messages; an undelivered entry would then exist twice", n)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{okAnswer}}
	r := f.runner(t, p)
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	if _, err := r.chatWithRetry(context.Background(), p, "test-model", msgs, nil); err != nil {
		t.Fatalf("chatWithRetry: %v", err)
	}

	if n := countSteeringMessages(f.state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
	for _, m := range f.state.Messages() {
		if m.ContentType == session.ContentTypeSteering && m.Content != "[user steering, mid-turn]: also update the README" {
			t.Fatalf("persisted steering content = %q, want the enveloped wire form", m.Content)
		}
	}
	// Submission is what makes the receipt truthful.
	receipt, err := f.state.SubmitSendNow(session.SendNowRequest{
		TurnID:    f.turnID,
		RequestID: "r1",
		Text:      "also update the README",
	})
	if err != nil {
		t.Fatalf("retry SubmitSendNow: %v", err)
	}
	if receipt.Status != session.SteeringDelivered {
		t.Fatalf("receipt status = %q, want delivered once the model request carried the text", receipt.Status)
	}

	// A retry finds nothing unsubmitted and writes nothing twice.
	if _, err := r.chatWithRetry(context.Background(), p, "test-model", msgs, nil); err != nil {
		t.Fatalf("retry chatWithRetry: %v", err)
	}
	if n := countSteeringMessages(f.state); n != 1 {
		t.Fatalf("persisted steering messages after the retry = %d, want still 1", n)
	}
}

// countSteeringMessages counts the persisted mid-turn steering copies, which is
// the transcript's side of the "delivered exactly once" contract.
func countSteeringMessages(state *session.State) int {
	n := 0
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.ContentType == session.ContentTypeSteering {
			n++
		}
	}
	return n
}

// TestSteeringRealProviderFailureKeepsItsCause pins the other half: a real
// provider failure that arrives around the interruption is still that failure.
// Relabelling it as the user's doing would hide an HTTP error or a decoding
// defect behind their keystroke, and the coaching the turn owes them for it
// would never be emitted.
func TestSteeringRealProviderFailureKeepsItsCause(t *testing.T) {
	f := newSteeringModelFixture(t)
	http400 := &provider.ProviderError{Provider: "test", StatusCode: 400, Body: "bad request"}
	// The failure is injected the moment the request is dispatched — before the
	// interruption is even submitted — and the stream then waits, so the user's
	// cancellation races it rather than causing it.
	p := &steeringStreamProvider{
		partial: `{"rationale":"r","action":{"type":"answer","content":"partial but real"}}`,
		err:     http400,
		ready:   make(chan struct{}, 1),
	}
	r := f.runner(t, p)

	type outcome struct {
		res chatResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.chatWithRetry(context.Background(), p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
		done <- outcome{res: res, err: err}
	}()
	p.awaitDispatch(t)
	f.interrupt(t)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chatWithRetry never returned")
	}
	if errors.Is(got.err, session.ErrSendNow) {
		t.Fatalf("a genuine provider failure was relabelled as the user's interruption: %v", got.err)
	}
	var pe *provider.ProviderError
	if !errors.As(got.err, &pe) || pe.StatusCode != 400 {
		t.Fatalf("err = %v, want the injected HTTP 400", got.err)
	}
	// The partial response is real model output this attempt produced before
	// failing; the interruption did not invalidate it.
	if got.res.Text == "" {
		t.Fatal("salvageable text from the failed attempt was discarded")
	}
}
