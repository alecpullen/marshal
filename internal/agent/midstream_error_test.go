package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/session"
	"marshal/internal/tools/native"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// partialThenErrorProvider streams usable content and then fails mid-stream,
// the shape a malformed SSE chunk produces: openai_compatible.go emits
// ChatEventError and returns, abandoning the rest of the stream.
type partialThenErrorProvider struct {
	calls   int
	partial string
	err     error
}

func (p *partialThenErrorProvider) Name() string { return "partial-then-error" }

func (p *partialThenErrorProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *partialThenErrorProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *partialThenErrorProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.calls++
	ch := make(chan schema.ChatEvent, 3)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.partial}
	ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.err}
	close(ch)
	return ch, nil
}

// TestChatOnceDiscardsPartialTextOnMidStreamError documents the data loss
// behind "JSON error mid turn that caused a failure". A single undecodable SSE
// chunk aborts the stream, and every delta received before it is thrown away —
// so a turn that had already produced a complete, parseable action can still
// fail with nothing salvaged.
func TestChatOnceDiscardsPartialTextOnMidStreamError(t *testing.T) {
	complete := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &partialThenErrorProvider{
		partial: complete,
		err:     errors.New("decode stream chunk: invalid character 'k' looking for beginning of object key string"),
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	res, err := runner.chatOnce(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err == nil {
		t.Fatal("expected the mid-stream error to surface")
	}
	if res.Text != complete {
		t.Errorf("partial text discarded on mid-stream error:\n got: %q\nwant: %q", res.Text, complete)
	}
}

// TestChatWithRetryKeepsSalvageableTextAfterRetries pins the caller-visible
// consequence: after retries are exhausted the accumulated text must still
// reach the turn loop, which has a full recovery ladder for text that fails to
// parse but no recovery at all for text it never receives.
func TestChatWithRetryKeepsSalvageableTextAfterRetries(t *testing.T) {
	complete := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &partialThenErrorProvider{
		partial: complete,
		err:     errors.New("decode stream chunk: unexpected end of JSON input"),
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxRetries = 1

	res, err := runner.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected the mid-stream error to surface after retries")
	}
	if res.Text != complete {
		t.Errorf("salvageable text lost after retries:\n got: %q\nwant: %q", res.Text, complete)
	}
}

// TestRunTaskSalvagesTurnAfterMidStreamError is the behavior the bug report
// asked for: a JSON error mid-turn must not fail the turn when the response
// already received is usable.
func TestRunTaskSalvagesTurnAfterMidStreamError(t *testing.T) {
	complete := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &partialThenErrorProvider{
		partial: complete,
		err:     errors.New("decode stream chunk: invalid character 'k'"),
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run failed instead of salvaging the partial response: %v", err)
	}

	var sawAnswer, sawNotice bool
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "the whole answer") {
			sawAnswer = true
		}
		if strings.Contains(m.Content, "Recovered from a provider stream error") {
			sawNotice = true
		}
	}
	if !sawAnswer {
		t.Error("salvaged answer never reached the transcript")
	}
	if !sawNotice {
		t.Error("no system notice recorded; a silent recovery hides provider trouble")
	}
}

// TestRunTaskFailsWhenNothingToSalvage keeps the fix honest: a stream error
// with no content is still a failure, not a silently swallowed one.
func TestRunTaskFailsWhenNothingToSalvage(t *testing.T) {
	p := &partialThenErrorProvider{
		partial: "",
		err:     errors.New("decode stream chunk: unexpected end of JSON input"),
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run succeeded despite an empty failed stream; the error was swallowed")
	}
}

// TestRunTaskDoesNotSalvageCancellation pins that Esc still stops the turn.
// Salvaging a cancelled stream would resurrect work the user asked to abandon.
func TestRunTaskDoesNotSalvageCancellation(t *testing.T) {
	complete := `{"rationale":"r","action":{"type":"answer","content":"the whole answer"}}`
	p := &partialThenErrorProvider{partial: complete, err: context.Canceled}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "do the thing"); err == nil {
		t.Fatal("Run salvaged a cancelled stream; cancellation must stop the turn")
	}
}

// TestAuditEventsCarryFinishReason pins the instrumentation that makes
// truncated tool arguments diagnosable: a patch that fails to parse looks
// identical whether the model wrote it badly or the provider cut it off at the
// output-token limit. The finish reason is the only thing that distinguishes
// them, so it must reach the audit record.
func TestAuditEventsCarryFinishReason(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.txt"}}}`,
			`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
		},
		FinishReasons: []string{"stop", "stop"},
	}
	reg := registry.New()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot:  dir,
		MaxOutputBytes: 1000,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	runner := NewRunner(p, reg, pol, state, "test-model")

	_ = runner.Run(context.Background(), "read the file")

	events := state.AuditLog()
	if len(events) == 0 {
		t.Fatal("no audit events recorded")
	}
	if got := events[0].FinishReason; got != "stop" {
		t.Errorf("FinishReason = %q, want %q — truncated arguments stay undiagnosable without it", got, "stop")
	}
}

// TestLengthTruncatedActionIsStillExecuted characterises a confirmed gap, not
// desired behavior. isLengthFinish (runner.go:56) says a response cut off at
// the output-token limit "may carry silently truncated arguments and must not
// be executed" — but that guard only covers native tool calls. On the
// JSON-action path the action is executed regardless, as asserted below.
//
// This is the likely source of the 25-of-43 recorded file.write_patch failures
// whose payloads stop mid-block: a patch truncated at the token limit reaches
// the parser and is indistinguishable from a badly written one. The recorded
// finish_reason now distinguishes them.
//
// When the guard is extended to this path, this test should be inverted to
// assert the call is refused.
func TestLengthTruncatedActionIsStillExecuted(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.txt"}}}`,
			`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
		},
		FinishReasons: []string{"length", "stop"},
	}
	reg := registry.New()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := native.RegisterAll(reg, native.Options{WorkspaceRoot: dir, MaxOutputBytes: 1000}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	runner := NewRunner(p, reg, pol, state, "test-model")

	_ = runner.Run(context.Background(), "read the file")

	events := state.AuditLog()
	if len(events) != 1 {
		t.Fatalf("got %d audit events, want 1", len(events))
	}
	if events[0].Error != "" {
		t.Errorf("call was refused with %q; if the length guard now covers this path, invert this test", events[0].Error)
	}
	if got := events[0].FinishReason; got != "length" {
		t.Errorf("FinishReason = %q, want %q", got, "length")
	}
}
