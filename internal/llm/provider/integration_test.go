//go:build integration

package provider

// Run manually against a local Ollama server:
//   MARSHAL_TEST_OLLAMA_URL=http://localhost:11434/v1 \
//     go test -tags=integration ./internal/llm/provider/... -run Ollama -v
//
// Run manually against a local LM Studio server:
//   MARSHAL_TEST_LMSTUDIO_URL=http://localhost:1234/v1 \
//     go test -tags=integration ./internal/llm/provider/... -run LMStudio -v

import (
	"context"
	"os"
	"testing"
	"time"

	"marshal/internal/llm/schema"
)

// TestOllamaChatIntegration exercises a real chat completion round trip
// against a locally running Ollama server (OpenAI-compatible endpoint).
// It is skipped unless MARSHAL_TEST_OLLAMA_URL is set.
func TestOllamaChatIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_OLLAMA_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_OLLAMA_URL to run this test against a local Ollama server")
	}

	model := os.Getenv("MARSHAL_TEST_OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5-coder:1.5b"
	}

	// Ollama's default local setup requires no API key.
	p, err := NewOpenAICompatible(Options{Name: "ollama", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}

	runChatIntegration(t, p, model)
}

// TestLMStudioChatIntegration exercises a real chat completion round trip
// against a locally running LM Studio server (OpenAI-compatible endpoint).
// It is skipped unless MARSHAL_TEST_LMSTUDIO_URL is set.
func TestLMStudioChatIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_LMSTUDIO_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_LMSTUDIO_URL to run this test against a local LM Studio server")
	}

	model := os.Getenv("MARSHAL_TEST_LMSTUDIO_MODEL")
	if model == "" {
		// LM Studio typically ignores the exact model field when only one
		// model is loaded in the server, so any placeholder string works
		// in that common single-model setup.
		model = "local-model"
	}

	p, err := NewOpenAICompatible(Options{Name: "lmstudio", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}

	runChatIntegration(t, p, model)
}

// runChatIntegration sends a streaming chat request to p and asserts a
// well-formed event stream: no error event, a terminal done event, and
// non-empty accumulated content.
func runChatIntegration(t *testing.T, p *OpenAICompatible, model string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := schema.ChatRequest{
		Model: model,
		Messages: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "Say the word 'test' and nothing else."},
		},
		Stream: true,
	}

	events, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var content string
	var sawDone bool
	for ev := range events {
		switch ev.Type {
		case schema.ChatEventDelta:
			content += ev.Delta
		case schema.ChatEventDone:
			sawDone = true
		case schema.ChatEventError:
			t.Fatalf("received ChatEventError: %v", ev.Err)
		}
	}

	if !sawDone {
		t.Error("expected a ChatEventDone event, got none")
	}
	if content == "" {
		t.Error("expected non-empty accumulated content, got empty string")
	}
}
