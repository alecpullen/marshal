package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSymbolAdapterNoServer(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{}, nil)
	a := NewSymbolAdapter(NewHandle(m, nil, nil))
	syms, ok := a.DocumentSymbols(context.Background(), "go", "a.go", []byte("package p"))
	if ok || syms != nil {
		t.Fatalf("expected (nil,false) with no server, got %v %v", syms, ok)
	}
}

func TestDiagnosticsAdapterEmptyCacheFallsBack(t *testing.T) {
	// Manager with no servers → ServerFor returns ok=false → adapter returns ("", false).
	m := NewManager(t.TempDir(), map[string]ServerSpec{}, nil)
	a := NewDiagnosticsAdapter(NewHandle(m, nil, nil))
	out, ok := a.Diagnostics("go", "x.go")
	if ok || out != "" {
		t.Fatalf("expected ('', false) when no server, got (%q, %v)", out, ok)
	}
}

func TestReadDiagnosticsFileCapsSize(t *testing.T) {
	dir := t.TempDir()

	// A small file under the cap is read back in full.
	small := filepath.Join(dir, "small.go")
	if err := os.WriteFile(small, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := readDiagnosticsFile(small)
	if !ok {
		t.Fatal("small file should be readable")
	}
	if string(got) != "package p\n" {
		t.Fatalf("got %q, want %q", got, "package p\n")
	}

	// A file larger than the cap is skipped rather than read.
	large := filepath.Join(dir, "large.go")
	if err := os.WriteFile(large, make([]byte, diagnosticsFileCap+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readDiagnosticsFile(large); ok {
		t.Fatal("file exceeding the cap should be skipped")
	}

	// A missing file is not readable.
	if _, ok := readDiagnosticsFile(filepath.Join(dir, "nope.go")); ok {
		t.Fatal("missing file should not be readable")
	}
}

func TestDiagnosticsAdapterVersionIncrements(t *testing.T) {
	// Verify the version tracking map increments per URI and resets to 1
	// for a fresh URI.
	da := &DiagnosticsAdapter{versions: make(map[string]int)}
	v1 := da.nextVersion("file:///test.go")
	if v1 != 1 {
		t.Fatalf("first version = %d, want 1", v1)
	}
	v2 := da.nextVersion("file:///test.go")
	if v2 != 2 {
		t.Fatalf("second version = %d, want 2", v2)
	}
	// Different URI starts at 1.
	v3 := da.nextVersion("file:///other.go")
	if v3 != 1 {
		t.Fatalf("first version for new URI = %d, want 1", v3)
	}
}

func TestFromFileURIRejectsPathOutsideRoot(t *testing.T) {
	root := "/workspace/project"
	uri := "file:///etc/passwd"
	_, err := fromFileURI(uri, root)
	if err == nil {
		t.Fatal("expected error for path outside root")
	}
}

func TestFromFileURIAcceptsPathInsideRoot(t *testing.T) {
	root := "/workspace/project"
	uri := "file:///workspace/project/src/main.go"
	got, err := fromFileURI(uri, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.FromSlash("/workspace/project/src/main.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlattenHoverContents_MarkupContent(t *testing.T) {
	raw := json.RawMessage(`{"kind":"markdown","value":"**hello**"}`)
	got := flattenHoverContents(raw)
	if got != "**hello**" {
		t.Fatalf("expected '**hello**', got %q", got)
	}
}

func TestFlattenHoverContents_MarkedString(t *testing.T) {
	raw := json.RawMessage(`{"language":"go","value":"func Foo()"}`)
	got := flattenHoverContents(raw)
	if got != "func Foo()" {
		t.Fatalf("expected 'func Foo()', got %q", got)
	}
}

func TestFlattenHoverContents_MarkedStringArray(t *testing.T) {
	raw := json.RawMessage(`[{"language":"go","value":"func Foo()"},{"language":"go","value":"doc comment"}]`)
	got := flattenHoverContents(raw)
	if got != "func Foo()\n\ndoc comment" {
		t.Fatalf("expected 'func Foo()\\n\\ndoc comment', got %q", got)
	}
}

func TestFlattenHoverContents_BareString(t *testing.T) {
	raw := json.RawMessage(`"plain text"`)
	got := flattenHoverContents(raw)
	if got != "plain text" {
		t.Fatalf("expected 'plain text', got %q", got)
	}
}

func TestFlattenHoverContents_Empty(t *testing.T) {
	raw := json.RawMessage(`{"kind":"markdown","value":""}`)
	got := flattenHoverContents(raw)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
