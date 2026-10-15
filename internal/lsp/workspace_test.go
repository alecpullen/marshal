package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	root    string
	clients map[string]*Client
}

func (f *fakeSource) ServerFor(lang string) (*Client, bool) { c, ok := f.clients[lang]; return c, ok }
func (f *fakeSource) Root() string                          { return f.root }

func newTestWorkspace(t *testing.T) (*Workspace, *scripted, string) {
	t.Helper()
	root := t.TempDir()
	c, s := newScripted(t)
	w := NewWorkspace(&fakeSource{root: root, clients: map[string]*Client{"go": c}})
	w.quiet, w.grace, w.maxWait = 50*time.Millisecond, 200*time.Millisecond, time.Second
	return w, s, root
}

func TestNewErrorsIgnoresLineShiftsAndPreexisting(t *testing.T) {
	before := map[string][]Diagnostic{
		"file:///r/a.go": {{Severity: 1, Message: "old", Range: Range{Start: Position{Line: 3}}}},
		"file:///r/b.go": {{Severity: 1, Message: "elsewhere"}},
	}
	after := map[string][]Diagnostic{
		"file:///r/a.go": {{Severity: 1, Message: "old", Range: Range{Start: Position{Line: 9}}}, {Severity: 1, Message: "new one", Range: Range{Start: Position{Line: 4}}}},
		"file:///r/b.go": {{Severity: 1, Message: "elsewhere"}},
		"file:///r/c.go": {{Severity: 2, Message: "just a warning"}},
	}
	got := NewErrors(before, after)
	if len(got) != 1 || got[0].Message != "new one" || got[0].Line != 5 {
		t.Fatalf("NewErrors = %+v, want only the new error at line 5", got)
	}
}

func TestRenderNewErrorsCapsAndCounts(t *testing.T) {
	var errs []FileError
	for i := 0; i < 13; i++ {
		errs = append(errs, FileError{Path: "p" + string(rune('a'+i%3)) + ".go", Line: i + 1, Message: "x"})
	}
	out := RenderNewErrors(errs, true)
	if strings.Count(out, ": x") != 10 || !strings.Contains(out, "+3 more across 3 files") {
		t.Fatalf("render = %q", out)
	}
	if RenderNewErrors(nil, true) != "diagnostics: no new errors" {
		t.Fatal("empty render wrong")
	}
	if !strings.Contains(RenderNewErrors(nil, false), "still settling") {
		t.Fatal("unsettled note missing")
	}
}

func TestBeginEditReportsErrorCausedInAnotherFile(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	finish, ok := w.BeginEdit([]string{"a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok with a ready server")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.publish(toFileURI(root, "b.go"), Diagnostic{Severity: 1, Message: "not enough arguments in call to a.Add", Range: Range{Start: Position{Line: 6}}})
	}()
	out := finish(context.Background())
	if !strings.Contains(out, "b.go:7: not enough arguments") {
		t.Fatalf("finish = %q", out)
	}
	if len(s.notes("textDocument/didOpen")) != 1 || len(s.notes("textDocument/didSave")) != 1 {
		t.Fatal("write must be synced with didOpen + didSave")
	}
}

func TestBeginEditCleanEditReturnsAfterGrace(t *testing.T) {
	w, _, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	finish, _ := w.BeginEdit([]string{"a.go"})
	start := time.Now()
	out := finish(context.Background())
	if out != "diagnostics: no new errors" || time.Since(start) > 600*time.Millisecond {
		t.Fatalf("clean edit: %q after %v", out, time.Since(start))
	}
}

func TestSecondWriteSendsDidChange(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	_ = w.DidWrite("a.go")
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o644)
	_ = w.DidWrite("a.go")
	changes := s.notes("textDocument/didChange")
	if len(changes) != 1 || !strings.Contains(string(changes[0]), "var x = 1") {
		t.Fatalf("didChange = %s", changes)
	}
}

// Review focus 5.
func TestBeginEditRejectsPathsOutsideRoot(t *testing.T) {
	w, _, _ := newTestWorkspace(t)
	if _, ok := w.BeginEdit([]string{"/etc/hosts"}); ok {
		t.Fatal("a path outside the root must not use the LSP")
	}
	if _, ok := w.BeginEdit([]string{"notes.txt"}); ok {
		t.Fatal("a language with no server must not use the LSP")
	}
}

// Review focus 2.
func TestRescanResyncsOpenDocumentChangedOnDisk(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package a\n"), 0o644)
	_ = w.DidWrite("a.go")
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a\n"), 0o644)
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(p, []byte("package a // gofmt'd\n"), 0o644)
	future := time.Now().Add(time.Second)
	os.Chtimes(p, future, future)
	w.Rescan()
	time.Sleep(50 * time.Millisecond)
	if ch := s.notes("textDocument/didChange"); len(ch) != 1 || !strings.Contains(string(ch[0]), "gofmt'd") {
		t.Fatalf("open doc not resynced: %s", ch)
	}
	var sawB bool
	for _, n := range s.notes("workspace/didChangeWatchedFiles") {
		var p struct{ Changes []struct{ URI string } }
		_ = json.Unmarshal(n, &p)
		for _, c := range p.Changes {
			sawB = sawB || strings.HasSuffix(c.URI, "/b.go")
		}
	}
	if !sawB {
		t.Fatal("unopened changed file not reported via didChangeWatchedFiles")
	}
}
