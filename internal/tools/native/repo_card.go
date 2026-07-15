package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

func (t *toolSet) repoCardTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.card",
		Description: "Render a short project card from the indexed repository. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for repo.card")
		}
		files, err := t.db.GetFileIndex(t.projectID, 0)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load file index: %w", err)
		}
		if len(files) == 0 {
			return registry.ToolResult{
				Summary: "No indexed files",
				Content: "Run repo.index to build the file index first.",
			}, nil
		}
		project, err := t.db.GetProject(t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load project: %w", err)
		}
		content := repo.RenderRepoCard(project.Name, files)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Project card with %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
