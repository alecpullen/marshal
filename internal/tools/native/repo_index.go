package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"marshal/internal/db"
	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

// repoIndexTool builds the repo.index tool. It honours the configured
// Indexing.Ignore patterns to exclude files from indexing, and respects
// .gitignore rules are always respected.
func (t *toolSet) repoIndexTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.index",
		Description: "Scan the workspace, compute file hashes and languages, and store the file index in the project database.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for repo.index")
		}

		scanner := repo.NewScanner(repo.Config{
			Root:                  t.root,
			Ignore:                t.config.Indexing.Ignore,
			MaxIndexableFileBytes: t.config.Indexing.MaxIndexableFileBytes,
		})
		scanned, err := scanner.ScanDetailed()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("scan repo: %w", err)
		}

		files := make([]db.FileIndex, len(scanned))
		for i, sf := range scanned {
			files[i] = sf.FileIndex
		}

		// The tool layer owns LastIndexedAt: set it just before persisting so
		// callers know when this index snapshot was captured.
		now := time.Now().UTC()
		for i := range files {
			files[i].LastIndexedAt = now
		}
		if err := t.db.SaveFileIndex(t.projectID, files); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save file index: %w", err)
		}

		var symbols []db.Symbol
		var warnings []string
		for _, sf := range scanned {
			if sf.ReadErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: read error", sf.FileIndex.Path))
				continue
			}
			if sf.FileIndex.Language != "go" {
				continue
			}
			fileSymbols, extractErr := repo.ExtractSymbols(ctx, sf.FileIndex.Path, sf.Content)
			if extractErr != nil {
				// Unparseable file: keep its file-index entry, skip symbols.
				warnings = append(warnings, fmt.Sprintf("%s: parse error", sf.FileIndex.Path))
				continue
			}
			symbols = append(symbols, fileSymbols...)
		}
		if err := t.db.SaveSymbols(t.projectID, symbols); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save symbols: %w", err)
		}

		langCounts := map[string]int{}
		for _, f := range files {
			if f.Language != "" {
				langCounts[f.Language]++
			}
		}

		langs := make([]string, 0, len(langCounts))
		for lang := range langCounts {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		var b strings.Builder
		b.WriteString("Languages:\n")
		for _, lang := range langs {
			b.WriteString(fmt.Sprintf("  %s: %d\n", lang, langCounts[lang]))
		}
		fmt.Fprintf(&b, "\nSymbols: %d\n", len(symbols))
		if len(warnings) > 0 {
			sort.Strings(warnings)
			b.WriteString("\nWarnings:\n")
			for _, w := range warnings {
				b.WriteString(fmt.Sprintf("  %s\n", w))
			}
		}

		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files, %d symbols", len(files), len(symbols)),
			Content: b.String(),
		}, nil
	}
	return tool
}
