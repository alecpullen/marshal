package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

const repoMapMaxFiles = 200

func (t *toolSet) repoMapTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.map",
		Description: "Render a directory map of the indexed repository. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for repo.map")
		}
		files, err := t.db.GetFileIndex(t.projectID, repoMapMaxFiles)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load file index: %w", err)
		}
		if len(files) == 0 {
			return registry.ToolResult{
				Summary: "No indexed files",
				Content: "Run repo.index to build the file index first.",
			}, nil
		}
		symbols, err := t.db.GetSymbols(t.projectID, repoMapMaxFiles)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load symbol index: %w", err)
		}
		content := repo.RenderDirectoryMap(files, symbols, repoMapMaxFiles)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Directory map with %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
