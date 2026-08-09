package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/index"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

const defaultIndexTimeout = 10 * time.Minute

// repoIndexTool builds the repo.index tool. It honours the configured
// Indexing.Ignore patterns to exclude files from indexing, and respects
// .gitignore rules are always respected.
func (t *toolSet) repoIndexTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.index",
		Description: "Scan the workspace, compute file hashes and languages, and store the file index in the project database.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for repo.index")
		}

		var embedder embedding.Embedder
		var embedErr error
		if t.resolveEmbedder != nil {
			embedder, embedErr = t.resolveEmbedder()
		}
		if embedErr != nil && !errors.Is(embedErr, routing.ErrEmbeddingNotConfigured) {
			return registry.ToolResult{}, fmt.Errorf("embedding config: %w", embedErr)
		}
		if embedder != nil {
			if _, err := embedding.Probe(ctx, embedder); err != nil {
				return registry.ToolResult{}, fmt.Errorf("embedding probe failed: the configured embedding endpoint is unreachable or misconfigured (%w)", err)
			}
		}

		runCtx, cancel := context.WithTimeout(ctx, defaultIndexTimeout)
		defer cancel()

		onProgress := func(msg string) {
			if t.sessionState != nil {
				t.sessionState.SetActivity(session.Activity{Kind: session.ActivityTool, Label: msg})
			}
		}

		rep, err := index.Run(runCtx, index.Deps{
			DB:         t.db,
			Root:       t.root,
			Ignore:     t.config.Indexing.Ignore,
			MaxBytes:   t.config.Indexing.MaxIndexableFileBytes,
			Embedder:   embedder,
			LSP:        t.lspIndex,
			OnProgress: onProgress,
		}, t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("index run: %w", err)
		}

		langs := make([]string, 0, len(rep.LangCounts))
		for lang := range rep.LangCounts {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		var b strings.Builder
		b.WriteString("Languages:\n")
		for _, lang := range langs {
			b.WriteString(fmt.Sprintf("  %s: %d\n", lang, rep.LangCounts[lang]))
		}
		fmt.Fprintf(&b, "\nSymbols: %d\n", rep.Symbols)

		if embedder == nil {
			fmt.Fprintf(&b, "\nSemantic index: not configured\n")
		} else {
			fmt.Fprintf(&b, "\nEmbedded %d files (%d chunks)\n", rep.FilesEmbedded, rep.ChunksWritten)
		}

		if len(rep.Warnings) > 0 {
			sort.Strings(rep.Warnings)
			b.WriteString("\nWarnings:\n")
			for _, w := range rep.Warnings {
				b.WriteString(fmt.Sprintf("  %s\n", w))
			}
		}

		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files, %d symbols", rep.Files, rep.Symbols),
			Content: b.String(),
		}, nil
	}
	return tool
}
