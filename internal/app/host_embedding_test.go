package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// embeddingConfigFor returns a config whose embedding route resolves against
// baseURL: a local provider plus the canonical preset name the router
// synthesizes. The provider is local so the privacy gate lets it through.
func embeddingConfigFor(baseURL string) config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: baseURL},
	}
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	return cfg
}

// TestIndexPassKeepsIndexingWhenTheEmbeddingBackendIsDown pins the fail-soft
// contract: an unreachable embedding backend costs the embeddings, not the
// index. The file and symbol index do not depend on the embedder, so a down
// Ollama must not take file indexing down with it. The pass completes, the
// report carries the reason, and the file index is filled.
func TestIndexPassKeepsIndexingWhenTheEmbeddingBackendIsDown(t *testing.T) {
	// A server that answers every request with 500: the embedder's probe
	// fails, exactly as it would against a down or broken daemon.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	hs := testHosts(t, true, time.Minute)
	root := hostRoot(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, hs, root)
	projectID, err := h.db.GetOrCreateProject(root, "hosts-embedding-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := h.reports.Subscribe(ctx)

	cfg := embeddingConfigFor(backend.URL)
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, projectID)

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted {
			t.Fatalf("report = %q; want a completed pass despite the dead embedding backend", ev.Type)
		}
		if ev.Payload.Files == 0 {
			t.Fatal("the pass indexed no files; a dead embedding backend must not stop the file index")
		}
		var warned bool
		for _, w := range ev.Payload.Warnings {
			if strings.Contains(w, "embedding") {
				warned = true
			}
		}
		if !warned {
			t.Fatalf("report warnings = %v; want one naming the embedding failure", ev.Payload.Warnings)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the startup scan published no report")
	}

	files, err := h.db.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		t.Fatalf("file index after the degraded pass: %d files, err %v", len(files), err)
	}
}

// TestIndexPassEmbedsWhenTheEmbeddingBackendAnswers is the positive half of
// the contract: a reachable backend still gets probed and still embeds, and
// the pass reports no embedding warning.
func TestIndexPassEmbedsWhenTheEmbeddingBackendAnswers(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer backend.Close()

	hs := testHosts(t, true, time.Minute)
	root := hostRoot(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, hs, root)
	projectID, err := h.db.GetOrCreateProject(root, "hosts-embedding-live-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := h.reports.Subscribe(ctx)

	cfg := embeddingConfigFor(backend.URL)
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, projectID)

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted {
			t.Fatalf("report = %q; want a completed pass", ev.Type)
		}
		for _, w := range ev.Payload.Warnings {
			if strings.Contains(w, "embedding") {
				t.Fatalf("report warnings = %v; want no embedding warning from a reachable backend", ev.Payload.Warnings)
			}
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the startup scan published no report")
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		n, err := h.db.CountEmbeddedChunks(projectID)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no chunks were embedded against a reachable embedding backend")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
