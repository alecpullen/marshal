package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/strutil"
)

// RenderDirectoryMap renders a simple indented directory tree from a file
// index.
//
// The cap is applied per file entry: at most maxFiles file rows are
// printed. Directory entries are NOT counted against the cap and are
// always printed. Symbols are inlined for the printed files only; the
// symbol table is otherwise unchanged. Unexported symbols and imports
// are omitted here to keep the map compact, but remain fully queryable
// via the symbols.find tool.
func RenderDirectoryMap(files []db.FileIndex, symbols []db.Symbol, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 200
	}

	tree := &dirNode{name: ".", children: map[string]*dirNode{}}
	for _, f := range files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		insertPath(tree, parts, f.Path)
	}

	bySymbolFile := groupExportedSymbols(symbols)
	bySummary := summariesByFile(files)

	var b strings.Builder
	var fileCount int
	renderNode(&b, tree, "", &fileCount, maxFiles, bySymbolFile, bySummary)

	if fileCount > maxFiles {
		fmt.Fprintf(&b, "\n... (%d more files)\n", fileCount-maxFiles)
	}
	return b.String()
}

type dirNode struct {
	name     string
	children map[string]*dirNode
	files    []string
}

func insertPath(node *dirNode, parts []string, fullPath string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		node.files = append(node.files, fullPath)
		return
	}
	child, ok := node.children[parts[0]]
	if !ok {
		child = &dirNode{name: parts[0], children: map[string]*dirNode{}}
		node.children[parts[0]] = child
	}
	insertPath(child, parts[1:], fullPath)
}

func renderNode(b *strings.Builder, node *dirNode, prefix string, fileCount *int, maxFiles int, bySymbolFile map[string][]db.Symbol, bySummary map[string]string) {
	dirs := make([]string, 0, len(node.children))
	for name := range node.children {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	for _, name := range dirs {
		fmt.Fprintf(b, "%s%s/\n", prefix, name)
		renderNode(b, node.children[name], prefix+"  ", fileCount, maxFiles, bySymbolFile, bySummary)
	}

	sort.Strings(node.files)
	for _, fullPath := range node.files {
		if *fileCount < maxFiles {
			fmt.Fprintf(b, "%s%s%s%s\n", prefix, filepath.Base(fullPath), exportedSymbolSuffix(fullPath, bySymbolFile), summarySuffix(fullPath, bySummary))
		}
		// fileCount counts every file (including the ones we skip because we
		// hit maxFiles), so that the truncation note at the bottom is exact.
		// Directories are intentionally not counted.
		*fileCount++
	}
}

// summaryMaxChars bounds the per-file summary suffix so the map stays compact.
const summaryMaxChars = 80

// summariesByFile indexes non-empty, whitespace-collapsed file summaries by path.
func summariesByFile(files []db.FileIndex) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		if s := strings.Join(strings.Fields(f.Summary), " "); s != "" {
			out[f.Path] = s
		}
	}
	return out
}

// summarySuffix renders " — <summary>" truncated to summaryMaxChars, or "".
func summarySuffix(path string, bySummary map[string]string) string {
	s := bySummary[path]
	if s == "" {
		return ""
	}
	return " — " + strutil.Truncate(s, summaryMaxChars, true)
}

// groupExportedSymbols indexes symbols by file path, keeping only the
// exported top-level functions, methods, and types useful for repo-map
// orientation. Imports and unexported symbols are excluded here; both
// remain fully queryable via symbols.find. Export rules are per-language
// (see isExportedName).
func groupExportedSymbols(symbols []db.Symbol) map[string][]db.Symbol {
	byFile := map[string][]db.Symbol{}
	langByFile := map[string]string{}
	for _, s := range symbols {
		lang, ok := langByFile[s.FilePath]
		if !ok {
			lang = DetectLanguage(s.FilePath)
			langByFile[s.FilePath] = lang
		}
		if s.Kind == "import" || !isExportedName(lang, s.Name) {
			continue
		}
		byFile[s.FilePath] = append(byFile[s.FilePath], s)
	}
	return byFile
}

func exportedSymbolSuffix(path string, byFile map[string][]db.Symbol) string {
	syms := byFile[path]
	if len(syms) == 0 {
		return ""
	}
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return " (" + strings.Join(names, ", ") + ")"
}

// isExportedName reports whether a symbol belongs in the repo map. Export
// rules are per-language: Go uses the first-rune-uppercase convention;
// Python treats underscore-prefixed names as private; TypeScript/JS and
// Rust include all top-level symbols (module/export/pub semantics aren't
// recoverable without deeper parsing). Unknown languages fall back to the
// Go rule.
func isExportedName(lang, name string) bool {
	switch lang {
	case "python":
		return !strings.HasPrefix(name, "_")
	case "typescript", "javascript", "rust":
		return true
	default:
		r, _ := utf8.DecodeRuneInString(name)
		return unicode.IsUpper(r)
	}
}
