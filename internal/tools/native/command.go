package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) shellRunTool() registry.Tool {
	return registry.Tool{
		Name:        "shell.run",
		Description: "Run a shell command",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}

func (t *toolSet) testRunTool() registry.Tool {
	return registry.Tool{
		Name:        "test.run",
		Description: "Run the configured test command",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}
}
