//go:build integration

package embedding

// Run manually against a local Ollama server:
//   MARSHAL_TEST_OLLAMA_URL=http://localhost:11434 \
//     go test -tags=integration ./internal/llm/embedding/... -run Native -v
//
// Run against the OpenAI-compatible path (e.g. Ollama's /v1 or LM Studio):
//   MARSHAL_TEST_EMBED_V1_URL=http://localhost:11434/v1 \
//     go test -tags=integration ./internal/llm/embedding/... -run OpenAICompat -v
//
// Override the model with MARSHAL_TEST_EMBED_MODEL (default nomic-embed-text).

import (
	"context"
	"os"
	"testing"
	"time"
)

func embedModel() string {
	if m := os.Getenv("MARSHAL_TEST_EMBED_MODEL"); m != "" {
		return m
	}
	return "nomic-embed-text"
}

func TestOllamaNativeEmbedIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_OLLAMA_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_OLLAMA_URL to run against a local Ollama server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	e := newOllamaEmbedder(baseURL, "", embedModel())
	vecs, err := e.Embed(ctx, []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || e.Dims() == 0 || len(vecs[0]) != e.Dims() {
		t.Fatalf("vecs=%d dims=%d", len(vecs), e.Dims())
	}
}

func TestOpenAICompatEmbedIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_EMBED_V1_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_EMBED_V1_URL to run against an OpenAI-compatible endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// baseURL already ends in /v1; the backend appends /v1/embeddings, so
	// strip a trailing /v1 to avoid duplication.
	e := newOpenAIEmbedder(trimV1(baseURL), os.Getenv("MARSHAL_TEST_EMBED_API_KEY"), embedModel())
	vecs, err := e.Embed(ctx, []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || e.Dims() == 0 {
		t.Fatalf("vecs=%d dims=%d", len(vecs), e.Dims())
	}
}

func trimV1(s string) string {
	for _, suffix := range []string{"/v1/", "/v1"} {
		if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return s[:len(s)-len(suffix)]
		}
	}
	return s
}
