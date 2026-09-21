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
func newThinkLoopRunner(t *testing.T, logBuf *bytes.Buffer, p *agenttest.ScriptedProvider) *Runner {
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
