package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// loopThinking returns a thinking payload long enough and repetitive enough to
// trip the detector on a single delta (ScriptedProvider delivers one delta per
// call).
func loopThinking() string {
	return strings.Repeat(loopBlock(loopMinBlock), loopMinRepeats+1)
}

// loopScript returns n looping thinking payloads. ScriptedProvider has no
// repeat-last fallback for Thinking (unlike Responses), so every provider call a
// test makes has to be scripted explicitly — an unscripted call emits no
// thinking delta and therefore no loop.
func loopScript(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = loopThinking()
	}
	return out
}

// newThinkLoopRunner mirrors chat_temperature_test.go's construction so the
// session log can be captured with a buffer-backed handler.
func newThinkLoopRunner(t *testing.T, logBuf *bytes.Buffer, p provider.Provider) *Runner {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})
	return NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
}

const okAnswer = `{"rationale":"r","action":{"type":"answer","content":"done"}}`

func hasNudge(messages []schema.ChatMessage) bool {
	for _, m := range messages {
		if m.Role == schema.RoleSystem && m.Content == thinkingLoopNudge {
			return true
		}
	}
	return false
}

func TestChatOnceAbortsOnThinkingLoop(t *testing.T) {
	var logBuf bytes.Buffer
	p := &agenttest.ScriptedProvider{
		Thinking:  []string{loopThinking(), ""},
		Responses: []string{"", okAnswer},
	}
	r := newThinkLoopRunner(t, &logBuf, p)

	messages := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	res, err := r.chatOnce(context.Background(), p, "test-model", messages, nil, false)
	if err != nil {
		t.Fatalf("nudged retry should have recovered the turn: %v", err)
	}
	if !strings.Contains(res.Text, "done") {
		t.Fatalf("text = %q, want the recovered answer", res.Text)
	}
	if len(p.Requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (abort + one nudge retry)", len(p.Requests))
	}
	// The retry carries the nudge; the first attempt does not.
	if hasNudge(p.Requests[0].Messages) {
		t.Fatal("first attempt must not carry the nudge")
	}
	if !hasNudge(p.Requests[1].Messages) {
		t.Fatalf("retry must carry the nudge, got %#v", p.Requests[1].Messages)
	}
	// The caller's slice is untouched.
	if len(messages) != 1 || hasNudge(messages) {
		t.Fatalf("caller messages mutated: %#v", messages)
	}
	if !strings.Contains(logBuf.String(), "thinking loop detected") {
		t.Fatalf("abort must be logged, got:\n%s", logBuf.String())
	}
}

func TestChatOnceSecondLoopPropagates(t *testing.T) {
	var logBuf bytes.Buffer
	p := &agenttest.ScriptedProvider{
		Thinking:  []string{loopThinking(), loopThinking()},
		Responses: []string{"", ""},
	}
	r := newThinkLoopRunner(t, &logBuf, p)

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected errThinkingLoop after the nudge also looped")
	}
	if !errors.Is(err, errThinkingLoop) {
		t.Fatalf("err = %v, want errThinkingLoop", err)
	}
	if p.Calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (the nudge retry only)", p.Calls)
	}
}

// TestErrThinkingLoopIsNotRetryable pins the classifier contract: the transport
// ladder must not resend the same messages after the nudge failed.
func TestErrThinkingLoopIsNotRetryable(t *testing.T) {
	err := fmt.Errorf("%w: %q", errThinkingLoop, "repeated block")
	if isRetryableChatError(err) {
		t.Fatal("isRetryableChatError(errThinkingLoop) = true, want false")
	}
	if isReconnectEligible(context.Background(), err) {
		t.Fatal("isReconnectEligible(errThinkingLoop) = true, want false")
	}
}

// TestThinkingLoopEscalatesOnceAtThirdAbort pins the escalation latch: two
// aborts are silent, the third logs exactly one escalation line, and further
// aborts do not repeat it.
func TestThinkingLoopEscalatesOnceAtThirdAbort(t *testing.T) {
	var logBuf bytes.Buffer
	// Three fail() calls, each of which is one chatOnce (abort + nudge) and so
	// two provider calls: six looped payloads, two aborts per fail().
	p := &agenttest.ScriptedProvider{
		Thinking:  loopScript(6),
		Responses: []string{"", ""},
	}
	r := newThinkLoopRunner(t, &logBuf, p)

	messages := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	fail := func() {
		t.Helper()
		if _, err := r.chatWithRetry(context.Background(), p, "test-model", messages, nil); err == nil {
			t.Fatal("expected a loop failure")
		}
	}

	fail() // 2 aborts
	if got := strings.Count(logBuf.String(), "repeatedly enters reasoning loops"); got != 0 {
		t.Fatalf("escalation after 2 aborts = %d, want 0:\n%s", got, logBuf.String())
	}

	fail() // 4 aborts: crosses the threshold at the 3rd
	if got := strings.Count(logBuf.String(), "repeatedly enters reasoning loops"); got != 1 {
		t.Fatalf("escalation after the 3rd abort = %d, want 1:\n%s", got, logBuf.String())
	}

	fail() // more aborts must not re-warn
	if got := strings.Count(logBuf.String(), "repeatedly enters reasoning loops"); got != 1 {
		t.Fatalf("escalation repeated: %d, want 1:\n%s", got, logBuf.String())
	}
}

// TestThinkingLoopEscalatesAfterRecoveredAborts pins the approved semantics that
// the counter increments at the abort site, so an abort the nudge recovers still
// counts. TestThinkingLoopEscalatesOnceAtThirdAbort scripts only hard-failing
// loops, so it would pass unchanged if the increment moved into the
// second-loop failure path — silently reverting that decision.
func TestThinkingLoopEscalatesAfterRecoveredAborts(t *testing.T) {
	var logBuf bytes.Buffer
	// Six scripted calls = three chatOnce rounds, each aborting on attempt 1
	// and recovering on the nudged attempt 2 (an empty Thinking entry emits no
	// delta, so no loop).
	p := &agenttest.ScriptedProvider{
		Thinking:  []string{loopThinking(), "", loopThinking(), "", loopThinking(), ""},
		Responses: []string{"", okAnswer, "", okAnswer, "", okAnswer},
	}
	r := newThinkLoopRunner(t, &logBuf, p)

	messages := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	const escalation = "repeatedly enters reasoning loops"

	for round := 1; round <= thinkingLoopEscalateAt; round++ {
		res, err := r.chatOnce(context.Background(), p, "test-model", messages, nil, false)
		if err != nil {
			t.Fatalf("round %d: the nudge should have recovered the turn: %v", round, err)
		}
		if !strings.Contains(res.Text, "done") {
			t.Fatalf("round %d: text = %q, want the recovered answer", round, res.Text)
		}
		want := 0
		if round == thinkingLoopEscalateAt {
			want = 1
		}
		if got := strings.Count(logBuf.String(), escalation); got != want {
			t.Fatalf("after %d recovered aborts: escalation = %d, want %d:\n%s",
				round, got, want, logBuf.String())
		}
	}
}

// ctxClosingProvider delivers one looping thinking delta, then blocks until the
// attempt context is cancelled and closes its event channel only in response —
// the shape the real backends take transitively through their ctx-bound request
// bodies. ScriptedProvider cannot stand in: it ignores the request context and
// closes its own channel, so cancel() never influences the outcome.
type ctxClosingProvider struct {
	thinking     string
	fallback     time.Duration
	sawCancel    chan struct{}
	trailingSent chan struct{}
}

func (p *ctxClosingProvider) Name() string { return "ctx-closing" }

func (p *ctxClosingProvider) Models(context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *ctxClosingProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *ctxClosingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.thinking}
		select {
		case <-ctx.Done():
		case <-time.After(p.fallback):
		}
		// Non-blocking, idempotent signals: chatOnce makes two attempts
		// (abort, then the nudge retry), so each of these fires twice and a
		// close() would panic on the second.
		select {
		case p.sawCancel <- struct{}{}:
		default:
		}
		// Two trailing events, and this is the whole point of the test. On an
		// unbuffered channel a single event cannot tell drain from break: the
		// consumer is parked in the range, so it receives that event either
		// way — draining it, or receiving it and then breaking. The second
		// send is what separates them. A drain keeps receiving, absorbs it,
		// and the channel closes normally; a break has already left the range,
		// so nothing is receiving and this send blocks forever. That is the
		// goroutine leak, made observable, and close(events) never runs.
		events <- schema.ChatEvent{Type: schema.ChatEventDone}
		events <- schema.ChatEvent{Type: schema.ChatEventDone}
		select {
		case p.trailingSent <- struct{}{}:
		default:
		}
	}()
	return events, nil
}

// TestChatOnceDrainEndsWhenProviderHonorsCancel pins the contract the abort's
// drain depends on: once the detector fires, chatOnce returns promptly because
// cancel() reaches the provider and closes the channel. Three separate
// regressions fail here — dropping cancel() (the provider blocks until the
// fallback, tripping the watchdog), breaking instead of draining (the trailing
// send never completes), and both are invisible to the ScriptedProvider tests.
func TestChatOnceDrainEndsWhenProviderHonorsCancel(t *testing.T) {
	p := &ctxClosingProvider{
		thinking: loopThinking(),
		fallback: 10 * time.Second,
		// Buffered: the signals are non-blocking sends made before the test
		// starts reading, so an unbuffered channel would drop them.
		sawCancel:    make(chan struct{}, 1),
		trailingSent: make(chan struct{}, 1),
	}
	var logBuf bytes.Buffer
	r := newThinkLoopRunner(t, &logBuf, p)
	// Deliberately longer than the watchdog below, so a missing cancel()
	// surfaces as a hang rather than being masked by the request deadline.
	r.ChatTimeout = 10 * time.Second

	type outcome struct {
		res chatResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.chatOnce(context.Background(), p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
		done <- outcome{res, err}
	}()

	select {
	case got := <-done:
		if !errors.Is(got.err, errThinkingLoop) {
			t.Fatalf("err = %v, want errThinkingLoop", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chatOnce never returned: the drain did not end, so the abort context never reached the provider")
	}

	select {
	case <-p.sawCancel:
	default:
		t.Fatal("provider never observed context cancellation")
	}
	select {
	case <-p.trailingSent:
	case <-time.After(time.Second):
		t.Fatal("trailing event never consumed: the range broke instead of draining")
	}
}
