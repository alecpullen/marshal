package bridge

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestWebIsStdlibOnly enforces the rule in AGENTS.md: everything under
// web/ is an external ACP client and must depend on nothing but the Go
// standard library. It may not reach into marshal/internal, and it may
// not take third-party dependencies — a terminal binary people install
// with `go install` should not inherit a web server's supply chain.
//
// If this fails, do not add an exception: either the code belongs on the
// agent side of ACP, or the data belongs in the JSON contract.
func TestWebIsStdlibOnly(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve web/ root: %v", err)
	}

	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendored and generated trees under web/ui.
			switch d.Name() {
			case "node_modules", ".svelte-kit", "dist", "static":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				return uerr
			}
			if reason, ok := forbiddenImport(imp); ok {
				t.Errorf("web/%s imports %q: %s", rel, imp, reason)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web/: %v", err)
	}
}

// forbiddenImport reports whether an import path breaks the boundary.
// A standard library path's first element never contains a dot; a
// third-party path's always does.
func forbiddenImport(path string) (reason string, forbidden bool) {
	if strings.HasPrefix(path, "marshal/") {
		return "web/ must not depend on the agent's Go packages; put the data in the JSON contract instead", true
	}
	first, _, _ := strings.Cut(path, "/")
	if strings.Contains(first, ".") {
		return "web/ must stay dependency-free so it can ship as its own module", true
	}
	return "", false
}

// TestForbiddenImportClassifies guards the guard.
func TestForbiddenImportClassifies(t *testing.T) {
	allowed := []string{"net/http", "encoding/json", "os", "path/filepath"}
	for _, p := range allowed {
		if _, bad := forbiddenImport(p); bad {
			t.Errorf("forbiddenImport(%q) = forbidden, want allowed", p)
		}
	}
	denied := []string{
		"marshal/internal/app/session",
		"github.com/pelletier/go-toml/v2",
		"golang.org/x/sys/unix",
	}
	for _, p := range denied {
		if _, bad := forbiddenImport(p); !bad {
			t.Errorf("forbiddenImport(%q) = allowed, want forbidden", p)
		}
	}
}
