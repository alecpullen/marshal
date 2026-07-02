package native

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestSymbolsFindTool(t *testing.T) {
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
	if err := dbConn.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", Signature: "func NewScanner(root string) *Scanner", LineStart: 3, LineEnd: 5},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", Signature: "func (s *Scanner) Scan() ([]string, error)", LineStart: 7, LineEnd: 9},
		{FilePath: "scanner.go", Kind: "type", Name: "File", Signature: "type File struct", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("symbols.find")
	if !ok {
		t.Fatal("symbols.find not found")
	}

	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"name":"scan"}`)})
	if err != nil {
		t.Fatalf("symbols.find failed: %v", err)
	}
	if !strings.Contains(res.Content, "NewScanner") || !strings.Contains(res.Content, "Scan") {
		t.Fatalf("expected NewScanner and Scan in content: %s", res.Content)
	}
	if strings.Contains(res.Content, "type File") {
		t.Fatalf("expected type File excluded by name filter: %s", res.Content)
	}

	res, err = tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"kind":"type"}`)})
	if err != nil {
		t.Fatalf("symbols.find kind filter failed: %v", err)
	}
	if !strings.Contains(res.Content, "type File") {
		t.Fatalf("expected type File in kind-filtered content: %s", res.Content)
	}

	res, err = tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("symbols.find with no filters failed: %v", err)
	}
	if !strings.Contains(res.Summary, "3 symbols") {
		t.Fatalf("expected 3 symbols summary, got %q", res.Summary)
	}
}

func TestSymbolsFindToolRejectsUnknownKind(t *testing.T) {
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

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("symbols.find")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"kind":"bogus"}`)}); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestSymbolsFindToolRequiresDB(t *testing.T) {
	tmp := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("symbols.find")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{}); err == nil {
		t.Fatal("expected error when DB not configured")
	}
}
