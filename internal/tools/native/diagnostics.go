package native

import (
	"context"
	"encoding/json"
	"strings"

	"marshal/internal/tools/registry"
)

type diagnosticsArgs struct {
	Path string `json:"path"`
}

func (t *toolSet) diagnosticsCheckTool() registry.Tool {
	tool := registry.Tool{
		Name:        "diagnostics.check",
		Description: "Run the configured language checker on a file or package and return findings.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[diagnosticsArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		path := args.Path
		if path == "" {
			path = "."
		}
		out, err := t.diagnostics.Check([]string{path}, languageOf([]string{path}))
		if err != nil {
			return registry.ToolResult{}, err
		}
		// Empty string means no checker is configured for this language; tell
		// the caller explicitly rather than returning a blank result.
		if out == "" {
			out = "diagnostics: none"
		}
		return registry.ToolResult{Summary: "diagnostics check complete", Content: out}, nil
	}
	return tool
}

func languageOf(paths []string) string {
	for _, p := range paths {
		lower := strings.ToLower(p)
		switch {
		case strings.HasSuffix(lower, ".go"):
			return "go"
		case strings.HasSuffix(lower, ".py"):
			return "python"
		case strings.HasSuffix(lower, ".rs"):
			return "rust"
		case strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".tsx"):
			return "typescript"
		case strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".jsx") || strings.HasSuffix(lower, ".mjs") || strings.HasSuffix(lower, ".cjs"):
			return "javascript"
		case strings.HasSuffix(lower, ".rb"):
			return "ruby"
		}
	}
	return ""
}
