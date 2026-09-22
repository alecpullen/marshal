package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

// scriptedStub is a fake provider that returns a pre-scripted sequence of
// ChatEvents from its Chat method. Used by ChatText tests.
type scriptedStub struct {
	events []schema.ChatEvent
}

func (s *scriptedStub) Name() string { return "stub" }

func (s *scriptedStub) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (s *scriptedStub) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (s *scriptedStub) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	ch := make(chan schema.ChatEvent, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestChatTextAccumulatesDeltas(t *testing.T) {
	p := &scriptedStub{events: []schema.ChatEvent{
		{Type: schema.ChatEventDelta, Delta: "hel"},
		{Type: schema.ChatEventDelta, Delta: "lo"},
		{Type: schema.ChatEventDone},
	}}
	got, err := ChatText(context.Background(), p, schema.ChatRequest{Model: "m"})
	if err != nil || got != "hello" {
		t.Fatalf("ChatText = %q, %v", got, err)
	}
}

// Reasoning models stream their chain-of-thought as DeltaThinking events
// ahead of the answer. ChatText feeds single-shot consumers (title
// generator, drift/task classifiers, knowledge extractor) whose outputs
// must contain the answer only — thinking text leaking in produced
// titles like "The user is asking me to name this session…".
func TestChatTextSkipsThinkingDeltas(t *testing.T) {
	p := &scriptedStub{events: []schema.ChatEvent{
		{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: "The user is asking me to name this session"},
		{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: ", so I will pick a concise label."},
		{Type: schema.ChatEventDelta, Delta: "Fix parser bug"},
		{Type: schema.ChatEventDone},
	}}
	got, err := ChatText(context.Background(), p, schema.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("ChatText err = %v", err)
	}
	if got != "Fix parser bug" {
		t.Fatalf("ChatText = %q, want %q (thinking deltas must not leak into the text)", got, "Fix parser bug")
	}
}

func TestChatTextReturnsErrorEvent(t *testing.T) {
	boom := errors.New("boom")
	p := &scriptedStub{events: []schema.ChatEvent{
		{Type: schema.ChatEventDelta, Delta: "x"},
		{Type: schema.ChatEventError, Err: boom},
	}}
	if _, err := ChatText(context.Background(), p, schema.ChatRequest{Model: "m"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestChatTextNoDoneReturnsError(t *testing.T) {
	p := &scriptedStub{events: []schema.ChatEvent{
		{Type: schema.ChatEventDelta, Delta: "partial"},
		// Channel closes without a Done event — no error event either.
	}}
	got, err := ChatText(context.Background(), p, schema.ChatRequest{Model: "m"})
	if err == nil {
		t.Fatalf("expected error for stream without Done, got nil; text=%q", got)
	}
	if !strings.Contains(err.Error(), "Done") {
		t.Fatalf("error should mention Done, got: %v", err)
	}
	if got != "partial" {
		t.Errorf("should still return accumulated text, got %q", got)
	}
}
