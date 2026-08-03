//go:build integration

package provider

import (
	"os"
	"testing"

	"marshal/internal/llm/schema"
)

func TestOllamaNativeIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_OLLAMA_URL")
	if baseURL == "" {
		t.Skip("MARSHAL_TEST_OLLAMA_URL not set")
	}
	p, err := NewOllamaNative(Options{Name: "ollama", BaseURL: baseURL, KeepAlive: "5m"})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Skip("no models pulled on test Ollama server")
	}

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    models[0].ID,
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
		}
	}
	if !sawDelta || !sawDone {
		t.Fatalf("sawDelta=%v sawDone=%v, want both", sawDelta, sawDone)
	}
}
