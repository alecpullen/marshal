package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// seamModule is this module's import path, and seamPrefix is the prefix every
// TUI import carries when it reaches into the repository.
const seamModule = "marshal"
const seamPrefix = seamModule + "/"

// seamImportAllowed reports whether a TUI file may import importPath under
// P2 C1: the TUI talks to the agent side through acpclient, never by importing
// it. Standard library and third-party packages are always fine, the TUI
// itself is fine, and a small set of pure data packages is fine. Everything
// else in this module is agent side.
func seamImportAllowed(importPath string) bool {
	// Anything that is neither the module root nor inside the module belongs
	// to the standard library or to a third party, both of which are fine.
	if importPath != seamModule && !strings.HasPrefix(importPath, seamPrefix) {
		return true
	}
	switch importPath {
	case "marshal/internal/app/tui",
		"marshal/internal/acpwire",
		"marshal/internal/acpclient",
		// Data packages the TUI renders from.
		"marshal/internal/app/config",
		"marshal/internal/llm/routing",
		"marshal/internal/llm/schema",
		"marshal/internal/interaction",
		"marshal/internal/app/clipboard",
		"marshal/internal/strutil",
		"marshal/internal/diffview",
		"marshal/internal/jsonextract",
		"marshal/internal/progress":
		return true
	}
	// A sibling such as .../tuix must not pass the prefix check below.
	return strings.HasPrefix(importPath, "marshal/internal/app/tui/")
}

// deniedConfigFuncs returns the names in internal/app/config that the TUI must
// not call directly: the loaders, the savers and the path helpers. The TUI
// receives already-resolved configuration through the seam instead.
func deniedConfigFuncs(configDir string) (map[string]bool, error) {
	denied := map[string]bool{
		"UserDir":           true,
		"DataDir":           true,
		"UserConfigPath":    true,
		"ProjectConfigPath": true,
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(configDir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Save") || strings.HasPrefix(fn.Name.Name, "Load") {
				denied[fn.Name.Name] = true
			}
		}
	}
	return denied, nil
}

// seamViolations walks root and reports every place a TUI file crosses the
// seam, keyed by slash path relative to root and sorted within each file:
// imports that seamImportAllowed rejects, and selectors of the config package
// that name a denied function.
func seamViolations(root string, denied map[string]bool) (map[string][]string, error) {
	violations := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		var entries []string
		add := func(entry string) {
			if seen[entry] {
				return
			}
			seen[entry] = true
			entries = append(entries, entry)
		}
		configName := ""
		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if !seamImportAllowed(importPath) {
				add(importPath)
			}
			if importPath == "marshal/internal/app/config" {
				configName = path.Base(importPath)
				if imp.Name != nil {
					configName = imp.Name.Name
				}
			}
		}
		if configName != "" {
			switch configName {
			case "_":
				// A blank import binds no name, so it cannot call a config
				// function.
			case ".":
				// A dot-import reaches every denied function unqualified, so
				// the selector scan below cannot see it. Record the import
				// itself as the violation.
				add("config.(dot-import)")
			default:
				ast.Inspect(file, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok || id.Name != configName {
						return true
					}
					if denied[sel.Sel.Name] {
						add("config." + sel.Sel.Name)
					}
					return true
				})
			}
		}
		if len(entries) > 0 {
			sort.Strings(entries)
			violations[filepath.ToSlash(rel)] = entries
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// TestTUIStaysOnTheClientSeam fails on any TUI use of the agent side. There is
// no allowlist (P2 plan 9): the fix is always to move the call behind
// acpclient.
func TestTUIStaysOnTheClientSeam(t *testing.T) {
	denied, err := deniedConfigFuncs(filepath.Join("..", "config"))
	if err != nil {
		t.Fatalf("deniedConfigFuncs: %v", err)
	}
	found, err := seamViolations(".", denied)
	if err != nil {
		t.Fatalf("seamViolations: %v", err)
	}
	for _, file := range sortedKeysOf(found) {
		for _, entry := range found[file] {
			t.Errorf("%s uses %s, which is agent side (P2 C1): move it behind acpclient", file, entry)
		}
	}
}

func sortedKeysOf(v map[string][]string) []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringInSlice(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestSeamImportAllowedClassifies(t *testing.T) {
	allowed := []string{
		"fmt",
		"charm.land/lipgloss/v2",
		"marshal/internal/app/tui/glyph",
		"marshal/internal/acpwire",
		"marshal/internal/acpclient",
		"marshal/internal/app/config",
	}
	for _, importPath := range allowed {
		if !seamImportAllowed(importPath) {
			t.Errorf("seamImportAllowed(%q) = false, want true", importPath)
		}
	}
	denied := []string{
		"marshal",
		"marshal/internal/acpclient/inproc",
		"marshal/internal/acpclient/rpc",
		"marshal/internal/app/session",
		"marshal/internal/viewmodel",
		"marshal/internal/repo/gitinfo",
		"marshal/internal/app/tuix",
	}
	for _, importPath := range denied {
		if seamImportAllowed(importPath) {
			t.Errorf("seamImportAllowed(%q) = true, want false", importPath)
		}
	}
}

func TestSeamViolationsSeesADotImportedConfig(t *testing.T) {
	dir := t.TempDir()
	src := `package tui

import . "marshal/internal/app/config"

func configDir() string { return UserConfigPath() }
`
	if err := os.WriteFile(filepath.Join(dir, "dotimport.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	found, err := seamViolations(dir, map[string]bool{"UserConfigPath": true})
	if err != nil {
		t.Fatalf("seamViolations: %v", err)
	}
	if entries := found["dotimport.go"]; !stringInSlice(entries, "config.(dot-import)") {
		t.Fatalf("dotimport.go entries = %v, want config.(dot-import)", entries)
	}
}

func TestDeniedConfigFuncsFindsTheFileFunctions(t *testing.T) {
	denied, err := deniedConfigFuncs(filepath.Join("..", "config"))
	if err != nil {
		t.Fatalf("deniedConfigFuncs: %v", err)
	}
	for _, name := range []string{"SaveUserConfigValue", "UserConfigPath", "DataDir", "UserDir", "ProjectConfigPath"} {
		if !denied[name] {
			t.Errorf("deniedConfigFuncs is missing %q", name)
		}
	}
	for _, name := range []string{"Default", "Diagnose", "LookupPath"} {
		if denied[name] {
			t.Errorf("deniedConfigFuncs should not contain %q", name)
		}
	}
}
