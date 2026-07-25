package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedderEmbedPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["input"].([]any); !ok {
			t.Errorf("input not an array: %#v", req["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out of order to prove index-based sorting.
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.3,0.4],"index":1},{"embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "", "nomic-embed-text")
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

func TestOpenAIEmbedderEmptyInput(t *testing.T) {
	e := newOpenAIEmbedder("http://unused.invalid", "", "m")
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || len(vecs) != 0 {
		t.Fatalf("empty input: vecs=%#v err=%v", vecs, err)
	}
}

func TestOpenAIEmbedderRetriesOn503(t *testing.T) {
	retryBackoff = 0
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "", "m")
	vecs, err := e.Embed(context.Background(), []string{"x"})
	if err != nil || len(vecs) != 1 || calls != 2 {
		t.Fatalf("calls=%d vecs=%#v err=%v", calls, vecs, err)
	}
}

func TestOpenAIEmbedderSetsAuthHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "secret", "m")
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}
