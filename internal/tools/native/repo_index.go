package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"marshal/internal/db"
	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

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

		scanner := repo.NewScanner(repo.Config{Root: t.root})
		files, err := scanner.Scan()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("scan repo: %w", err)
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
		for _, f := range files {
			if f.Language != "go" {
				continue
			}
			content, readErr := os.ReadFile(filepath.Join(t.root, f.Path))
			if readErr != nil {
				// Unreadable file: keep its file-index entry, skip symbols.
				continue
			}
			fileSymbols, extractErr := repo.ExtractSymbols(f.Path, content)
			if extractErr != nil {
				// Unparseable file: keep its file-index entry, skip symbols.
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

		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files, %d symbols", len(files), len(symbols)),
			Content: b.String(),
		}, nil
	}
	return tool
}
