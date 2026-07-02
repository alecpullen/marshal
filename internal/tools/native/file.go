package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) fileReadTool() registry.Tool {
	return registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}
