package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/llm/embedding"
	"marshal/internal/tools/registry"
)

// fakeEmbedder is a 2-dim embedder used by TestRepoIndexEmbeds.
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

func TestRepoIndexTool(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.index")
	if !ok {
		t.Fatal("repo.index not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}
	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(res.Summary, "1 file") {
		t.Fatalf("expected summary to contain '1 file', got %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "1 symbol") {
		t.Fatalf("expected summary to contain '1 symbol', got %q", res.Summary)
	}
	if !strings.Contains(res.Content, "go: 1") {
		t.Fatalf("expected content to contain 'go: 1', got %q", res.Content)
	}

	files, err := dbConn.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected 1 indexed main.go, got %+v", files)
	}
	if files[0].Language != "go" {
		t.Fatalf("expected Language == 'go', got %q", files[0].Language)
	}

	symbols, err := dbConn.GetSymbols(projectID, 0)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "main" || symbols[0].Kind != "function" {
		t.Fatalf("expected 1 main function symbol, got %+v", symbols)
	}
}

func TestRepoIndexToolIgnoresByConfigPattern(t *testing.T) {
	tmp := t.TempDir()
	// Create a file that should be kept.
	if err := os.MkdirAll(filepath.Join(tmp, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "keepme.go"), []byte("package keepme\n"), 0644); err != nil {
		t.Fatalf("write keepme.go: %v", err)
	}
	// Create a file that should be ignored by the vendor/** pattern.
	if err := os.MkdirAll(filepath.Join(tmp, "vendor"), 0755); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "vendor", "secret.go"), []byte("package secret\n"), 0644); err != nil {
		t.Fatalf("write secret.go: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: tmp,
		DB:            dbConn,
		ProjectID:     projectID,
		Config: config.Config{
			Indexing: config.IndexingConfig{
				Ignore: []string{"vendor/**"},
			},
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.index")
	if !ok {
		t.Fatal("repo.index not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}
	if !strings.HasPrefix(res.Summary, "Indexed 1 file") {
		t.Fatalf("expected 1 file indexed (only keepme.go), got %q", res.Summary)
	}

	files, err := dbConn.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "src/keepme.go" {
		t.Fatalf("expected 1 file (src/keepme.go), got %+v", files)
	}
}

func TestRepoIndexToolSkipsSymbolsForNonGoFiles(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "README.md"), []byte("# hi\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("repo.index")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{}); err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}

	symbols, err := dbConn.GetSymbols(projectID, 0)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 0 {
		t.Fatalf("expected no symbols for non-Go files, got %+v", symbols)
	}
}

func TestRepoIndexEmbeds(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
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
	ts.resolveEmbedder = func() (embedding.Embedder, error) {
		return &fakeEmbedder{model: "m"}, nil
	}

	res, err := ts.repoIndexTool().Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "Embedded") {
		t.Fatalf("expected embedded line, got: %s", res.Content)
	}
}

func TestRepoIndexEmbedsNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
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
	// resolveEmbedder left nil — should produce "not configured" message.

	res, err := ts.repoIndexTool().Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "not configured") {
		t.Fatalf("expected 'not configured' line, got: %s", res.Content)
	}
}
