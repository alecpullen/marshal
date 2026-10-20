package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// overflowProviderError is a provider rejection shaped like the ones real
// backends send when the prompt exceeds their window.
func overflowProviderError() *provider.ProviderError {
	return &provider.ProviderError{
		Provider:   "test",
		StatusCode: 400,
		Body:       `{"error":{"message":"prompt is too long: 90000 tokens > 65536"}}`,
	}
}

// newOverflowRunner builds a runner whose route declares a 65536-token
// window, so effectiveTurnThreshold is 47513 and a small test wire sits
// comfortably under it. Tests can therefore script a provider rejection that
// the runner did not predict.
func newOverflowRunner(t *testing.T, p *agenttest.ScriptedProvider, reg *registry.Registry) *Runner {
	t.Helper()
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "test-model", ContextWindow: 65536, MaxOutputTokens: 8192},
	}}
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	return r
}

// registerLargeRead registers a file.read returning content, so a test can
// put a genuinely large tool result on the wire.
func registerLargeRead(t *testing.T, reg *registry.Registry, content string) {
	t.Helper()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: content}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// compactionNotices returns the transcript's compaction markers.
func compactionNotices(state *session.State) []string {
	var out []string
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeCompaction {
			out = append(out, m.Content)
		}
	}
	return out
}

func wireContentLen(msgs []schema.ChatMessage) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}

func readCall(id, path string) []schema.ToolCall {
	return []schema.ToolCall{{ID: id, Name: "file.read", Args: jsonRaw(`{"path":"` + path + `"}`)}}
}

// TestRunnerRecoversFromContextOverflowOnce is the only test that proves
// recovery actually shrank the wire rather than resending it. The failed
// request carries a large tool result; the retried request must not.
func TestRunnerRecoversFromContextOverflowOnce(t *testing.T) {
	big := strings.Repeat("package main\n", 5000)
	reg := registry.New()
	registerLargeRead(t, reg, big)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "summary of work", "Done."},
		Errs:          []error{nil, overflowProviderError(), nil, nil},
		ToolCalls:     [][]schema.ToolCall{readCall("c1", "a.go")},
		FinishReasons: []string{"tool_calls", "stop", "stop", "stop"},
	}
	r := newOverflowRunner(t, p, reg)

	if _, err := r.RunTask(context.Background(), "read the file"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 4 {
		t.Fatalf("got %d requests, want at least 4 (tool call, overflow, summary, retry)", len(p.Requests))
	}

	failed, retried := p.Requests[1].Messages, p.Requests[len(p.Requests)-1].Messages
	if after, before := wireContentLen(retried), wireContentLen(failed); after >= before {
		t.Fatalf("retry did not shrink the wire: %d bytes after compaction, %d before", after, before)
	}
	// The retry must be a rebuilt wire carrying the handoff summary, not the
	// transcript that was just rejected. Search rather than index: the
	// per-request budget line sits after the summary in the volatile tail.
	compacted := false
	for _, m := range retried {
		if strings.Contains(m.Content, "Progress summary") {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatalf("retry is not a compacted wire: %+v", retried)
	}
	if !hasCompactionNotice(r.State) {
		t.Fatalf("no compaction record; notices: %v", compactionNotices(r.State))
	}
}

// TestRunnerOverflowBranchIsReachableOnEmptyText pins the insertion point.
// A provider 400 delivers no text, so the overflow branch must sit BEFORE the
// "empty response" guard. Move it after, and this test fails with the turn
// terminated instead of compacted.
func TestRunnerOverflowBranchIsReachableOnEmptyText(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "Done."},
		Errs:      []error{overflowProviderError(), nil},
	}
	r := newOverflowRunner(t, p, registry.New())

	if _, err := r.RunTask(context.Background(), "do the thing"); err != nil {
		t.Fatalf("RunTask: %v (the overflow branch was unreachable and the turn failed)", err)
	}
	if !hasCompactionNotice(r.State) {
		t.Fatalf("compaction never ran; notices: %v", compactionNotices(r.State))
	}
}

// TestRunnerOverflowPrunesBeforeCompacting: compaction resends the whole
// transcript, so an oversize wire must shrink before summarisation can
// succeed. When pruning alone lands under the threshold, no compaction is
// needed and the turn continues for free.
func TestRunnerOverflowPrunesBeforeCompacting(t *testing.T) {
	big := strings.Repeat("x", 5000) // > pruneMinSizeDefault
	reg := registry.New()
	registerLargeRead(t, reg, big)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "", "Done."},
		Errs:          []error{nil, nil, overflowProviderError(), nil},
		ToolCalls:     [][]schema.ToolCall{readCall("c1", "a.go"), readCall("c2", "a.go")},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop", "stop"},
	}
	r := newOverflowRunner(t, p, reg)

	if _, err := r.RunTask(context.Background(), "read a.go twice"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if hasCompactionNotice(r.State) {
		t.Fatalf("pruning sufficed; compaction should not have run: %v", compactionNotices(r.State))
	}
	if !hasPruneNotice(r.State) {
		t.Fatalf("expected a prune notice naming the dropped tool result; notices: %v", compactionNotices(r.State))
	}
}

// TestRunnerFailsAfterSecondOverflow: one compaction, one retry, then fail.
// A provider that rejects even the compacted prompt must not loop, and the
// error must name what the user can change.
func TestRunnerFailsAfterSecondOverflow(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs: []error{overflowProviderError(), overflowProviderError()},
	}
	r := newOverflowRunner(t, p, registry.New())

	_, err := r.RunTask(context.Background(), "do the thing")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if p.Calls != 2 {
		t.Fatalf("expected 2 calls (overflow, then the compaction resend), got %d", p.Calls)
	}
	for _, want := range []string{"test-model", "65536", "47513", "agent.max_turn_context_tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// TestRunnerOverflowCompactionFailureIsActionable: when compaction itself
// fails, the turn fails once with an actionable error — no retry loop and no
// confusing double error. The compaction call returns an empty summary, which
// is the errEmptyHandoffSummary path.
func TestRunnerOverflowCompactionFailureIsActionable(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", ""},
		Errs:          []error{overflowProviderError(), nil},
		FinishReasons: []string{"stop", "stop"},
	}
	r := newOverflowRunner(t, p, registry.New())

	_, err := r.RunTask(context.Background(), "do the thing")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if p.Calls != 2 {
		t.Fatalf("expected 2 calls (the rejection, then the failed summarisation) and no retry loop, got %d", p.Calls)
	}
	for _, want := range []string{"too long", "agent.max_turn_context_tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// TestRunnerOverflowRespectsCancellation: the user's stop outranks the
// overflow. Reporting the overflow would blame the provider for a turn the
// user ended.
func TestRunnerOverflowRespectsCancellation(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs: []error{overflowProviderError(), context.Canceled},
	}
	r := newOverflowRunner(t, p, registry.New())

	_, err := r.RunTask(context.Background(), "do the thing")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled to win over the overflow, got %v", err)
	}
	if errors.Is(err, errContextOverflow) {
		t.Fatalf("cancellation must not be reported as a context overflow: %v", err)
	}
}

func hasCompactionNotice(state *session.State) bool {
	for _, n := range compactionNotices(state) {
		if strings.HasPrefix(n, "compacted ") {
			return true
		}
	}
	return false
}

func hasPruneNotice(state *session.State) bool {
	for _, n := range compactionNotices(state) {
		if strings.Contains(strings.ToLower(n), "pruned") {
			return true
		}
	}
	return false
}
