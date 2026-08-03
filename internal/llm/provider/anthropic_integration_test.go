//go:build integration

package provider

import (
	"os"
	"testing"

	"marshal/internal/llm/schema"
)

func TestAnthropicIntegration(t *testing.T) {
	apiKey := os.Getenv("MARSHAL_TEST_ANTHROPIC_KEY")
	if apiKey == "" {
		t.Skip("MARSHAL_TEST_ANTHROPIC_KEY not set")
	}
	p, err := NewAnthropic(Options{
		Name:    "anthropic",
		BaseURL: "https://api.anthropic.com",
		APIKey:  apiKey,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "claude-haiku-4-5",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "Reply with exactly: ok"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var sawDelta, sawDone bool
	for ev := range events {
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == schema.ChatEventDelta {
			sawDelta = true
		}
		if ev.Type == schema.ChatEventDone {
			sawDone = true
			if ev.Usage == nil || ev.Usage.PromptTokens == 0 {
				t.Fatalf("Usage = %+v, want non-zero prompt tokens", ev.Usage)
			}
		}
	}
	if !sawDelta || !sawDone {
		t.Fatalf("sawDelta=%v sawDone=%v, want both", sawDelta, sawDone)
	}
}
