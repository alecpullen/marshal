package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoIndexTool(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

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

	files, err := dbConn.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected 1 indexed main.go, got %+v", files)
	}
}
