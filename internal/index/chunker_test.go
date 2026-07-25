package index

import (
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/repo"
)

func TestChunkCodeFileWithSymbols(t *testing.T) {
	src := "package p\n\n// Foo does a thing.\nfunc Foo(x int) int {\n\treturn x\n}\n"
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "p.go", Language: "go", Hash: "h"}, Content: []byte(src)}
	symbols := []db.Symbol{{FilePath: "p.go", Kind: "function", Name: "Foo", Signature: "func Foo(x int) int", LineStart: 4, LineEnd: 6}}

	chunks := ChunkFile(file, symbols)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	c := chunks[0]
	if c.Kind != "code" || c.SymbolName != "Foo" || c.FileHash != "h" {
		t.Fatalf("chunk = %#v", c)
	}
	if !strings.Contains(c.Content, "p.go") || !strings.Contains(c.Content, "func Foo") || !strings.Contains(c.Content, "return x") {
		t.Fatalf("enriched content missing parts: %q", c.Content)
	}
	if c.TokenCount <= 0 {
		t.Fatal("token count not set")
	}
}

func TestChunkMarkdownByHeading(t *testing.T) {
	src := "# Title\n\nintro\n\n## Section A\n\nbody a\n\n## Section B\n\nbody b\n"
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "README.md", Language: "markdown", Hash: "h"}, Content: []byte(src)}

	chunks := ChunkFile(file, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple heading sections, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Kind != "doc" {
			t.Fatalf("markdown chunk kind = %q", c.Kind)
		}
	}
}

func TestChunkSymbollessFileWindows(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 150; i++ {
		b.WriteString("line\n")
	}
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "big.txt", Language: "", Hash: "h"}, Content: []byte(b.String())}
	chunks := ChunkFile(file, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.SymbolName != "" || c.Kind != "code" {
			t.Fatalf("window chunk = %#v", c)
		}
	}
}
