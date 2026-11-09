package acpclient

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestClientImportsStayOnTheWireSide keeps this package's non-test files on the
// wire side of the seam: the standard library, acpwire, and the data packages
// C3 allows in signatures. Subdirectories are not walked; rpc is the client the
// TUI runs on and may import more.
func TestClientImportsStayOnTheWireSide(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate the test file")
	}
	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, spec.Path.Value, err)
			}
			if !allowedOnTheWireSide(path) {
				t.Errorf("%s imports %q, which is off the wire side (C2, C3)", name, path)
			}
		}
	}
}

// allowedOnTheWireSide reports whether path may be imported by this package.
func allowedOnTheWireSide(path string) bool {
	switch path {
	case "marshal/internal/acpwire",
		"marshal/internal/app/config",
		"marshal/internal/llm/routing",
		"marshal/internal/llm/schema",
		"marshal/internal/interaction":
		return true
	}
	// A first path element without a dot is a standard library package.
	return !strings.Contains(strings.SplitN(path, "/", 2)[0], ".")
}
