package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"marshal/internal/app/config"
)

func TestOllamaEmbedderEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// Native endpoint returns embeddings in input order (no index field).
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
	}))
	defer server.Close()

	e := newOllamaEmbedder(server.URL, "", "nomic-embed-text", "")
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.3 {
		t.Fatalf("vecs = %#v", vecs)
	}
	if e.Dims() != 2 || e.Model() != "nomic-embed-text" {
		t.Fatalf("Dims = %d, Model = %q", e.Dims(), e.Model())
	}
}

func TestOllamaEmbedderCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`)) // 1 vec for 2 inputs
	}))
	defer server.Close()

	e := newOllamaEmbedder(server.URL, "", "m", "")
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected error on vector/input count mismatch")
	}
}

func TestOllamaEmbedderSendsKeepAlive(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`))
	}))
	defer server.Close()

	e := newOllamaEmbedder(server.URL, "", "test-embed", "2h")
	if _, err := e.Embed(t.Context(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := gotBody["keep_alive"]; got != "2h" {
		t.Fatalf("keep_alive = %v, want %q", got, "2h")
	}
}

func TestOllamaEmbedderFactoryDefaultsKeepAlive(t *testing.T) {
	emb, err := NewFromConfig("local", config.ProviderConfig{
		Type:    "ollama",
		BaseURL: "http://unused",
	}, "test-embed")
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	oe, ok := emb.(*ollamaEmbedder)
	if !ok {
		t.Fatalf("NewFromConfig returned %T, want *ollamaEmbedder", emb)
	}
	if oe.keepAlive != DefaultOllamaKeepAlive {
		t.Fatalf("keepAlive = %q, want %q", oe.keepAlive, DefaultOllamaKeepAlive)
	}
}
