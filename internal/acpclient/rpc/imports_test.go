package rpc

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestRPCImportsStayOnTheWireSide keeps this package's non-test files on the
// wire side of the seam (C1): the standard library, acpwire, acpclient and the
// data packages acpclient allows in signatures. Subdirectories are not walked.
func TestRPCImportsStayOnTheWireSide(t *testing.T) {
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
				t.Errorf("%s imports %q, which is off the wire side (C1)", name, path)
			}
		}
	}
}

// allowedOnTheWireSide reports whether path may be imported by this package.
func allowedOnTheWireSide(path string) bool {
	switch path {
	case "marshal/internal/acpwire",
		"marshal/internal/acpclient",
		"marshal/internal/app/config",
		"marshal/internal/llm/routing",
		"marshal/internal/llm/schema",
		"marshal/internal/interaction":
		return true
	}
	// A first path element without a dot is a standard library package.
	return !strings.Contains(strings.SplitN(path, "/", 2)[0], ".")
}

// TestOnlyTheEmbeddingDials walks the module's non-test Go files: only
// internal/launch and internal/acp/acptest import this package, nothing
// imports internal/acp/acptest, and nothing imports the in-process transport
// P3 deletes (V7).
func TestOnlyTheEmbeddingDials(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate the test file")
	}
	// This file is internal/acpclient/rpc/imports_test.go: four levels up is
	// the module root.
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))

	const (
		dialed    = "marshal/internal/acpclient/rpc"
		testAgent = "marshal/internal/acp/acptest"
		deleted   = "marshal/internal/acpclient/inproc"
	)
	allowed := map[string]bool{"internal/launch": true, "internal/acp/acptest": true}

	importers := map[string]map[string]bool{}
	add := func(pkg, dir string) {
		if importers[pkg] == nil {
			importers[pkg] = map[string]bool{}
		}
		importers[pkg][dir] = true
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if path == root {
				return nil
			}
			// The repository holds worktrees under dot directories, and the
			// web SPA keeps node_modules.
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		dir, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		dir = filepath.ToSlash(dir)
		for _, spec := range parsed.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			switch imported {
			case dialed, testAgent, deleted:
				add(imported, dir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}

	for dir := range importers[dialed] {
		if !allowed[dir] {
			t.Errorf("%s imports %s; only internal/launch and internal/acp/acptest may dial (P3 V7)", dir, dialed)
		}
	}
	for dir := range allowed {
		if !importers[dialed][dir] {
			t.Errorf("%s imports neither %s; the dial must live there (P3 V7)", dir, dialed)
		}
	}
	for dir := range importers[testAgent] {
		t.Errorf("%s imports %s; the test agent is for tests (P3 V7)", dir, testAgent)
	}
	for dir := range importers[deleted] {
		t.Errorf("%s imports %s, which P3 deletes (V7)", dir, deleted)
	}
}
