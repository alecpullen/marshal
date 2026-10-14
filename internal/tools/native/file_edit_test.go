package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

func newEditRegistry(t *testing.T, root string) *registry.Registry {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "ft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, FileTracker: filetrack.New(database.SQLDB(), "s")}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestFileEditReplacesExactlyOnce(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o644)
	reg := newEditRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"a.go"}`); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"var x = 1","new_string":"var x = 2"}`)
	if err != nil {
		t.Fatalf("file.edit: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(got) != "package a\n\nvar x = 2\n" {
		t.Fatalf("content = %q", got)
	}
	if len(res.FilesChanged) != 1 || res.FilesChanged[0] != "a.go" || !strings.Contains(res.Content, "+var x = 2") {
		t.Fatalf("result = %+v", res)
	}
}

// Review focus 2.
func TestFileEditContentWithPatchMarkersAppliesExactly(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "doc.md"), []byte("intro\nold\n"), 0o644)
	reg := newEditRegistry(t, root)
	invokeTool(t, reg, "file.read", `{"path":"doc.md"}`)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"doc.md","old_string":"old","new_string":"<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE"}`); err != nil {
		t.Fatalf("file.edit: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "doc.md"))
	if string(got) != "intro\n<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileEditRequiresReadAndNonEmptyOldString(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("x\n"), 0o644)
	reg := newEditRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"x","new_string":"y"}`); err == nil || !strings.Contains(err.Error(), "never read") {
		t.Fatalf("unread edit error = %v, want never-read rejection", err)
	}
	invokeTool(t, reg, "file.read", `{"path":"a.go"}`)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"","new_string":"y"}`); err == nil || !strings.Contains(err.Error(), "file.write") {
		t.Fatalf("empty old_string error = %v, want pointer to file.write", err)
	}
}

func TestFileWritePatchIsInternal(t *testing.T) {
	reg := newEditRegistry(t, t.TempDir())
	tool, ok := reg.Lookup("file.write_patch")
	if !ok || !tool.Internal {
		t.Fatalf("file.write_patch must stay registered as Internal; ok=%v", ok)
	}
}
