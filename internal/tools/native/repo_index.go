package native

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
			return registry.ToolResult{}, fmt.Errorf("database not configured for repo.index")
		}

		scanner := repo.NewScanner(repo.Config{Root: t.root})
		files, err := scanner.Scan()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("scan repo: %w", err)
		}

		now := time.Now().UTC()
		for i := range files {
			files[i].LastIndexedAt = now
		}
		if err := t.db.SaveFileIndex(t.projectID, files); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save file index: %w", err)
		}

		langCounts := map[string]int{}
		for _, f := range files {
			if f.Language != "" {
				langCounts[f.Language]++
			}
		}

		content := "Languages:\n"
		for lang, count := range langCounts {
			content += fmt.Sprintf("  %s: %d\n", lang, count)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
