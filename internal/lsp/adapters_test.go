package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSymbolAdapterNoServer(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{}, nil)
	a := NewSymbolAdapter(m)
	syms, ok := a.DocumentSymbols(context.Background(), "go", "a.go", []byte("package p"))
	if ok || syms != nil {
		t.Fatalf("expected (nil,false) with no server, got %v %v", syms, ok)
	}
}

func TestDiagnosticsAdapterEmptyCacheFallsBack(t *testing.T) {
	// Manager with no servers → ServerFor returns ok=false → adapter returns ("", false).
	m := NewManager(t.TempDir(), map[string]ServerSpec{}, nil)
	a := NewDiagnosticsAdapter(m)
	out, ok := a.Diagnostics("go", "x.go")
	if ok || out != "" {
		t.Fatalf("expected ('', false) when no server, got (%q, %v)", out, ok)
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
