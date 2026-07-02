package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) repoSearchTool() registry.Tool {
	return registry.Tool{
		Name:        "repo.search",
		Description: "Search workspace files",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}
