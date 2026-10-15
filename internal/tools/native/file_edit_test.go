package native

import (
	"context"
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

// A file.edit against a path that does not exist is a common model slip (a
// typo, or a file it meant to create). The error it gets back must name a
// tool the model actually has — file.write — and must not teach the retired
// SEARCH-block format, which file.edit cannot express.
func TestFileEditMissingFilePointsAtFileWriteNotPatchFormat(t *testing.T) {
	reg := newEditRegistry(t, t.TempDir())
	_, err := invokeTool(t, reg, "file.edit", `{"path":"missing.go","old_string":"a","new_string":"b"}`)
	if err == nil {
		t.Fatal("expected an error editing a missing file")
	}
	if !strings.Contains(err.Error(), "file.write") {
		t.Errorf("error = %v, want it to point at file.write", err)
	}
	if strings.Contains(err.Error(), "SEARCH block") {
		t.Errorf("error = %v, must not teach the retired patch format", err)
	}
}

type fakeSync struct {
	begun   []string
	rescans int
	ok      bool
}

func (f *fakeSync) BeginEdit(paths []string) (func(context.Context) string, bool) {
	f.begun = append(f.begun, paths...)
	if !f.ok {
		return nil, false
	}
	return func(context.Context) string { return "diagnostics: no new errors" }, true
}
func (f *fakeSync) Rescan() { f.rescans++ }

func TestFileEditReportsLSPDiagnostics(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nvar x = 1\n"), 0o644)
	sync := &fakeSync{ok: true}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, LSPSync: sync}); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"var x = 1","new_string":"var x = 2"}`)
	if err != nil || !strings.Contains(res.Content, "diagnostics: no new errors") || len(sync.begun) != 1 {
		t.Fatalf("res=%+v err=%v begun=%v", res, err, sync.begun)
	}
}

func TestShellRunRescansLSP(t *testing.T) {
	sync := &fakeSync{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, LSPSync: sync}); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeTool(t, reg, "shell.run", `{"command":"true"}`); err != nil {
		t.Fatal(err)
	}
	if sync.rescans != 1 {
		t.Fatalf("rescans = %d, want 1", sync.rescans)
	}
}
