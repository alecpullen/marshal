package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) gitStatusTool() registry.Tool {
	return registry.Tool{
		Name:        "git.status",
		Description: "Show git status",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}

func (t *toolSet) gitDiffTool() registry.Tool {
	return registry.Tool{
		Name:        "git.diff",
		Description: "Show git diff",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}
