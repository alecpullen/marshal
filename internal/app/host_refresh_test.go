package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/trust"
)

// embeddingServer is an OpenAI-compatible /v1/embeddings endpoint that
// returns a small fixed-width vector for every input.
func embeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		var out struct {
			Data []item `json:"data"`
		}
		for i := range req.Input {
			out.Data = append(out.Data, item{Embedding: []float32{1, float32(i), 0.5}, Index: i})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const headlessIndexConfig = `[project]
name = "headless-index"

[profile]
default = "p"

[providers.embed]
type = "openai_compatible"
base_url = "%s"
api_key = "k"

[models.presets."embed/e"]
local_only = true

[agent_profiles.p]
implementer = "embed/e"
planner = "embed/e"
repo_scout = "embed/e"
tester = "embed/e"
reviewer = "embed/e"

[lsp]
enabled = false

[indexing]
watch = true
watch_debounce_ms = 50
embedding_preset = "embed/e"
`

func chunkedFile(t *testing.T, database *db.DB, projectID int64, path string) bool {
	t.Helper()
	files, err := database.ChunkedFiles(projectID)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	_, ok := files[path]
	return ok
}

// The P1 done-when: with indexing.watch on, an edit in a headless session's
// tree refreshes its embeddings with no TUI running.
func TestHeadlessRuntimeRefreshesEmbeddingsAfterAnEdit(t *testing.T) {
	srv := embeddingServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte(fmt.Sprintf(headlessIndexConfig, srv.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hosts := NewHosts()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hosts.Close(ctx)
	})
	rt, err := StartRuntime(context.Background(), WithWorkingDir(root), WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}), WithHosts(hosts))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	database := must[*db.DB](rt.DB)

	deadline := time.Now().Add(30 * time.Second)
	for !chunkedFile(t, database, rt.ProjectID, "main.go") {
		if time.Now().After(deadline) {
			t.Fatal("the startup scan never embedded main.go")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Rewrite the new file until the watcher has picked it up: the watcher
	// starts only after the startup scan returns, so a single write could
	// land before it is watching.
	added := filepath.Join(root, "added.go")
	for i := 0; !chunkedFile(t, database, rt.ProjectID, "added.go"); i++ {
		if time.Now().After(deadline) {
			t.Fatal("an edit in the tree was never embedded")
		}
		src := fmt.Sprintf("package main\n\n// Added%d is new.\nfunc Added%d() {}\n", i, i)
		if err := os.WriteFile(added, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
