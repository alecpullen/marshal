package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

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

	files, err := dbConn.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected 1 indexed main.go, got %+v", files)
	}
	if files[0].Language != "go" {
		t.Fatalf("expected Language == 'go', got %q", files[0].Language)
	}

	symbols, err := dbConn.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "main" || symbols[0].Kind != "function" {
		t.Fatalf("expected 1 main function symbol, got %+v", symbols)
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

	symbols, err := dbConn.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 0 {
		t.Fatalf("expected no symbols for non-Go files, got %+v", symbols)
	}
}
