package app

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

// TestAppImportsNoClient fails when one of app's non-test files imports
// the TUI (internal/app/tui or a subpackage), Bubble Tea, or the in-process
// adapter (P3 R1, V7).
func TestAppImportsNoClient(t *testing.T) {
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
			if !staysOnTheAgentSide(path) {
				t.Errorf("%s imports %q, which is client code (P3 R1, V7)", name, path)
			}
		}
	}
}

// staysOnTheAgentSide reports whether path may be imported by app.
func staysOnTheAgentSide(path string) bool {
	if path == "marshal/internal/app/tui" || strings.HasPrefix(path, "marshal/internal/app/tui/") {
		return false
	}
	if path == "charm.land/bubbletea/v2" {
		return false
	}
	// app never dials: the transport is launch's, which owns the pipe (P3 R1).
	return path != "marshal/internal/acpclient/rpc"
}
