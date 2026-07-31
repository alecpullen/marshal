package agent

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
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
