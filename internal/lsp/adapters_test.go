package lsp

import (
	"context"
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
