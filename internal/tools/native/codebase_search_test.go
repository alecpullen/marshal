package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

// fakeEmbedder is a 2-dim embedder used by the codebase_search tests.
type fakeEmbedder struct {
	model string
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1}
	}
	return out, nil
}
func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return 2 }

func TestCodebaseSearchNotConfigured(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.resolveEmbedder = func() (embedding.Embedder, error) { return nil, routing.ErrEmbeddingNotConfigured }
	res, err := ts.codebaseSearchTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"query":"x"}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Content, "not configured") {
		t.Fatalf("expected 'not configured' in content, got: %q", res.Content)
	}
}

func TestCodebaseSearchEmptyIndex(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.resolveEmbedder = func() (embedding.Embedder, error) { return &fakeEmbedder{model: "m"}, nil }
	res, err := ts.codebaseSearchTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"query":"x"}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Content, "No semantic index") {
		t.Fatalf("expected 'No semantic index' in content, got: %q", res.Content)
	}
}

func TestCodebaseSearchReturnsHits(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.resolveEmbedder = func() (embedding.Embedder, error) { return &fakeEmbedder{model: "m"}, nil }

	// Seed a chunk with a vector aligned to the fake embedder's query vector {1,0}.
	err := ts.db.ReplaceFileChunks(ts.projectID, "src/main.go", "abc123", []db.ChunkWithVector{{
		Chunk: db.Chunk{
			FilePath: "src/main.go", FileHash: "abc123", Kind: "code",
			StartLine: 1, EndLine: 10, Content: "package main\n\nfunc main() {}",
			TokenCount: 5,
		},
		Model: "m", Dim: 2, Vector: []float32{1, 0},
	}})
	if err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	res, err := ts.codebaseSearchTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"query":"main function"}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Content, "src/main.go") {
		t.Fatalf("expected file path in content, got: %q", res.Content)
	}
	if !strings.Contains(res.Summary, "1 match") {
		t.Fatalf("expected '1 match' in summary, got: %q", res.Summary)
	}
}

// embeddingConfig returns a config whose embedding route resolves: a local
// provider plus the canonical preset name the router synthesizes. Web tools
// stay off so the registered set stays the base surface plus
// codebase_search, which is what the registry-surface assertions expect.
func embeddingConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	cfg.Web.Enabled = false
	return cfg
}

func TestCodebaseSearchRegistered(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, Config: embeddingConfig()}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("codebase_search")
	if !ok {
		t.Fatal("codebase_search not registered with an embedding route configured")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("expected RiskReadOnly, got %v", tool.Risk)
	}
	if tool.Handler == nil {
		t.Fatal("handler is nil")
	}
}

// TestCodebaseSearchRegisteredOnlyWithEmbeddings pins the Tier 2a cut: the
// tool only works with an embedding route, so offering it otherwise teaches
// the model a tool whose only answer is "unavailable".
func TestCodebaseSearchRegisteredOnlyWithEmbeddings(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Config: config.Default()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("codebase_search"); ok {
		t.Fatal("codebase_search must not be registered without an embedding route")
	}
}

// newTestToolSetWithRepo creates a toolSet backed by a temp dir and in-memory
// DB, suitable for testing tools that need a project ID and DB connection.
func newTestToolSetWithRepo(t *testing.T) *toolSet {
	t.Helper()
	tmp := t.TempDir()
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	ts, err := newToolSet(Options{
		WorkspaceRoot: tmp,
		DB:            dbConn,
		ProjectID:     projectID,
	})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	return ts
}
