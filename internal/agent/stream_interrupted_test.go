package agent

import (
	"context"
	"encoding/json"
	"errors"
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

// interruptedCut is the adapter's error for a stream that ended before the
// provider's protocol announced completion (AUD-08).
var interruptedCut = errors.New("provider stream ended without a [DONE] completion marker")

// silentProvider streams deltas and then closes its channel with no terminal
// event at all — the shape a buggy or third-party adapter would produce. The
// agent must refuse to call that a successful turn on its own account.
type silentProvider struct {
	partial string
	calls   int
}

func (p *silentProvider) Name() string { return "silent" }
func (p *silentProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p *silentProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *silentProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.calls++
	ch := make(chan schema.ChatEvent, 1)
	if p.partial != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.partial}
	}
	close(ch)
	return ch, nil
}

// onceInterruptedThenAnswer cuts its first stream and completes the second, so
// the retry ladder has something to succeed at.
type onceInterruptedThenAnswer struct {
	text  string
	calls int
}

func (p *onceInterruptedThenAnswer) Name() string { return "interrupt-once" }
func (p *onceInterruptedThenAnswer) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p *onceInterruptedThenAnswer) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *onceInterruptedThenAnswer) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.calls++
	ch := make(chan schema.ChatEvent, 3)
	if p.calls == 1 {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: "partial"}
		ch <- schema.ChatEvent{Type: schema.ChatEventInterrupted, Err: interruptedCut}
		close(ch)
		return ch, nil
	}
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.text}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func interruptedTestRunner(t *testing.T, p interface {
	Name() string
	Models(context.Context) ([]schema.ModelInfo, error)
	Capabilities(context.Context) schema.ProviderCapabilities
	Chat(context.Context, schema.ChatRequest) (<-chan schema.ChatEvent, error)
}) (*Runner, *session.State) {
	t.Helper()
	state := newTestState(t)
	return NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model"), state
}

func TestChatOnceInterruptedStreamIsNotSuccess(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:   []string{"partial"},
		Interrupted: []error{interruptedCut},
	}
	runner, _ := interruptedTestRunner(t, p)

	res, err := runner.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err == nil {
		t.Fatal("chatOnce reported success for an interrupted stream")
	}
	if !errors.Is(err, errStreamInterrupted) {
		t.Fatalf("err = %v, want the agent's stream-interruption sentinel", err)
	}
	// The text really arrived; it is preserved but is not an answer.
	if res.Text != "partial" {
		t.Errorf("partial text = %q, want %q", res.Text, "partial")
	}
	if len(res.ToolCalls) != 0 || res.FinishReason != "" {
		t.Errorf("interrupted result carries executable state: %+v", res)
	}
}

func TestChatOnceStreamWithoutDoneIsNotSuccess(t *testing.T) {
	p := &silentProvider{partial: "partial"}
	runner, _ := interruptedTestRunner(t, p)

	res, err := runner.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err == nil {
		t.Fatal("chatOnce reported success for a stream that never completed")
	}
	if !errors.Is(err, errStreamInterrupted) {
		t.Fatalf("err = %v, want the stream-interruption sentinel", err)
	}
	if res.Text != "partial" {
		t.Errorf("partial text = %q, want %q", res.Text, "partial")
	}
}

func TestChatWithRetryRetriesInterruptedStream(t *testing.T) {
	answer := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &onceInterruptedThenAnswer{text: answer}
	runner, _ := interruptedTestRunner(t, p)
	runner.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	res, err := runner.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("a transient truncation was not retried to success: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("provider calls = %d, want 2 (one interrupted, one clean)", p.calls)
	}
	if res.Text != answer {
		t.Errorf("result text = %q, want the clean attempt's answer", res.Text)
	}
}

func TestRunTaskInterruptedStreamFailsTurn(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:   []string{"partial"},
		Interrupted: []error{interruptedCut, interruptedCut, interruptedCut},
	}
	runner, state := interruptedTestRunner(t, p)
	runner.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run succeeded on a stream that never completed")
	}
	notice, ok := state.Notice()
	if !ok {
		t.Fatal("no notice raised for the interrupted turn")
	}
	if notice.Severity != session.SeverityError {
		t.Errorf("notice severity = %v, want error", notice.Severity)
	}
	if !strings.Contains(strings.ToLower(notice.Message), "interrupt") {
		t.Errorf("notice = %q, want it to name the interruption", notice.Message)
	}
}

func TestRunTaskInterruptedStreamPreservesPartialText(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:   []string{"half an ans"},
		Interrupted: []error{interruptedCut, interruptedCut, interruptedCut},
	}
	runner, state := interruptedTestRunner(t, p)
	runner.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run succeeded on a truncated stream")
	}
	var found bool
	for _, m := range state.Messages() {
		if !strings.Contains(m.Content, "half an ans") {
			continue
		}
		found = true
		if !m.Salvaged {
			t.Error("partial text was not marked salvaged; it must not read as a finished answer")
		}
		if m.SalvageReason != string(reasonInterrupted) {
			t.Errorf("SalvageReason = %q, want %q", m.SalvageReason, reasonInterrupted)
		}
		// AddMessageSalvaged sets Final alongside Salvaged: the message is
		// the turn's last one, but the salvaged flag plus the reason are what
		// the viewmodel projects as "not a finished answer".
		if m.Final && !m.Salvaged {
			t.Error("partial text was presented as an ordinary finished answer")
		}
	}
	if !found {
		t.Error("partial text that arrived before the cut was discarded")
	}
}

func TestRunTaskInterruptedStreamDoesNotExecutePartialToolCall(t *testing.T) {
	var executed bool
	reg := registry.New()
	tool := registry.Tool{
		Name:   "test.tool",
		Risk:   registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		executed = true
		return registry.ToolResult{Summary: "ran", Content: "ok"}, nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}

	partial := `{"rationale":"r","action":{"type":"tool_call","tool":"test.tool","args":{}}}`
	p := &agenttest.ScriptedProvider{
		Responses:   []string{partial},
		Interrupted: []error{interruptedCut, interruptedCut, interruptedCut},
	}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run succeeded on a truncated stream")
	}
	if executed {
		t.Fatal("a tool call from a truncated stream was executed; repair must not disguise a cut protocol stream")
	}
}

func TestRunTaskInterruptedStreamIsNotSalvagedIntoSuccess(t *testing.T) {
	answer := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &agenttest.ScriptedProvider{
		Responses:   []string{answer},
		Interrupted: []error{interruptedCut, interruptedCut, interruptedCut},
	}
	runner, state := interruptedTestRunner(t, p)
	runner.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run salvaged a truncated stream into a successful turn")
	}
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "Recovered from a provider stream error") {
			t.Fatalf("interrupted stream took the mid-stream-error salvage path: %q", m.Content)
		}
		if strings.Contains(m.Content, "the whole answer") && m.Final && !m.Salvaged {
			t.Fatal("truncated text was promoted to a finished answer")
		}
	}
}
