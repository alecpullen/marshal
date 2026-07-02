package native

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoCardTool(t *testing.T) {
	tmp := t.TempDir()

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
	if err := dbConn.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "main.go", Language: "go", Hash: "abc", SizeBytes: 14, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("save file index: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.card")
	if !ok {
		t.Fatal("repo.card not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.card failed: %v", err)
	}
	if !strings.Contains(res.Content, "Project: test") {
		t.Fatalf("expected project name in card content: %s", res.Content)
	}
}
