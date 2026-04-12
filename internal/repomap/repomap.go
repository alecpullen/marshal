// Package repomap builds a PageRank-ranked symbol map for a repository.
// It walks source files, extracts definition and reference tags via
// tree-sitter, builds a file-to-file reference graph, and returns a
// formatted text summary suitable for injection into an executor prompt.
package repomap

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Tag is a symbol extracted from a source file.
type Tag struct {
	RelPath string  // relative path from repo root
	Name    string  // symbol name
	Kind    TagKind // Def or Ref
	Line    int     // 1-based
}

// TagKind distinguishes definitions from references.
type TagKind int

const (
	TagDef TagKind = iota
	TagRef
)

// Options control how the map is built.
type Options struct {
	// MaxFiles caps how many files are included in the rendered output.
	// 0 means use the default (50).
	MaxFiles int
	// MaxSymbolsPerFile caps the symbols shown per file.
	// 0 means use the default (10).
	MaxSymbolsPerFile int
	// ChatFiles are file paths (relative to root) mentioned in recent chat
	// history.  They seed the personalized PageRank distribution.
	ChatFiles []string
}

func (o *Options) maxFiles() int {
	if o.MaxFiles > 0 {
		return o.MaxFiles
	}
	return 50
}

func (o *Options) maxSymbols() int {
	if o.MaxSymbolsPerFile > 0 {
		return o.MaxSymbolsPerFile
	}
	return 10
}

// Section is one file's contribution to the map.
type Section struct {
	Path    string
	Symbols []string // formatted symbol lines
}

// Map is the final ranked repository map.
type Map struct {
	Sections []Section
}

// String returns the map formatted as multi-line text ready for prompt
// injection.  Returns empty string if the map has no sections.
func (m *Map) String() string {
	if len(m.Sections) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, s := range m.Sections {
		sb.WriteString(s.Path)
		sb.WriteString(":\n")
		for _, sym := range s.Symbols {
			sb.WriteString("│ ")
			sb.WriteString(sym)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// Build walks root, parses source files, and returns a Map.
// root must be an absolute path.  ig filters files that should be ignored;
// pass nil to skip ignore filtering.
func Build(root string, ig Ignorer, opts Options) (*Map, error) {
	// 1. Walk and collect source files.
	files, err := walkFiles(root, ig)
	if err != nil {
		return nil, fmt.Errorf("walk: %w", err)
	}

	// 2. Extract tags from every file.
	tagger := newTagger()
	allTags := make([]Tag, 0, len(files)*10)
	for _, rel := range files {
		abs := filepath.Join(root, rel)
		tags, tagErr := tagger.extractFile(abs, rel)
		if tagErr != nil {
			// Skip unparseable files silently.
			continue
		}
		allTags = append(allTags, tags...)
	}

	// 3. Build def map: symbol name → set of files that define it.
	defFiles := make(map[string]map[string]struct{})
	for _, t := range allTags {
		if t.Kind != TagDef {
			continue
		}
		if defFiles[t.Name] == nil {
			defFiles[t.Name] = make(map[string]struct{})
		}
		defFiles[t.Name][t.RelPath] = struct{}{}
	}

	// 4. Build file→file reference graph.
	//    Edge A→B means file A calls a symbol defined in B.
	graph := make(map[string]map[string]float64)
	for _, f := range files {
		graph[f] = make(map[string]float64)
	}
	for _, t := range allTags {
		if t.Kind != TagRef {
			continue
		}
		for defFile := range defFiles[t.Name] {
			if defFile == t.RelPath {
				continue // skip self-references
			}
			graph[t.RelPath][defFile]++
		}
	}

	// 5. PageRank.
	personal := make(map[string]float64, len(opts.ChatFiles))
	for _, cf := range opts.ChatFiles {
		personal[cf] = 1.0
	}
	scores := pageRank(files, graph, personal, 20)

	// 6. Sort files by score descending.
	type fileScore struct {
		path  string
		score float64
	}
	ranked := make([]fileScore, 0, len(files))
	for _, f := range files {
		ranked = append(ranked, fileScore{path: f, score: scores[f]})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].path < ranked[j].path
	})

	// 7. Build def-tag index per file for rendering.
	defsByFile := make(map[string][]Tag)
	for _, t := range allTags {
		if t.Kind == TagDef {
			defsByFile[t.RelPath] = append(defsByFile[t.RelPath], t)
		}
	}

	// 8. Assemble output sections.
	maxF := opts.maxFiles()
	maxS := opts.maxSymbols()
	sections := make([]Section, 0, maxF)
	for _, fs := range ranked {
		if len(sections) >= maxF {
			break
		}
		defs := defsByFile[fs.path]
		if len(defs) == 0 {
			continue
		}
		// Sort defs by line number.
		sort.Slice(defs, func(i, j int) bool {
			return defs[i].Line < defs[j].Line
		})
		syms := make([]string, 0, maxS)
		seen := make(map[string]bool)
		for _, d := range defs {
			if len(syms) >= maxS {
				break
			}
			if seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			syms = append(syms, d.Name)
		}
		sections = append(sections, Section{Path: fs.path, Symbols: syms})
	}

	return &Map{Sections: sections}, nil
}

// Ignorer is implemented by *git.Ignorer; we declare a minimal interface here
// to avoid a circular import.
type Ignorer interface {
	Match(relPath string) bool
}
