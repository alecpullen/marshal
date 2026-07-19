package provider

import (
	"context"
	"errors"
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
