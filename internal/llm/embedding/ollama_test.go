package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

	e := newOllamaEmbedder(server.URL, "", "nomic-embed-text")
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

	e := newOllamaEmbedder(server.URL, "", "m")
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected error on vector/input count mismatch")
	}
}
