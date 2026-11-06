package service

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

// TestServiceImportsStayAgentSide keeps this package's non-test files on the
// agent side of the seam: it may import app's data packages and the runtime's
// dependencies, but never the TUI (which renders only) or the wire client that
// TUI process will speak (P3 plan 8).
func TestServiceImportsStayAgentSide(t *testing.T) {
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
			if !allowedOnTheAgentSide(path) {
				t.Errorf("%s imports %q, which is off the agent side (P3 R1)", name, path)
			}
		}
	}
}

// allowedOnTheAgentSide reports whether path may be imported by this package.
func allowedOnTheAgentSide(path string) bool {
	if path == "marshal/internal/app/tui" || strings.HasPrefix(path, "marshal/internal/app/tui/") {
		return false
	}
	return path != "marshal/internal/acpclient/rpc"
}
