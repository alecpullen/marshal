package native

import (
	"context"
	"encoding/json"

	"marshal/internal/diagnostics"
	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

// newDiagnosticsChecker creates a Checker and wires the optional LSP source.
func newDiagnosticsChecker(opts Options) *diagnostics.Checker {
	c := diagnostics.NewChecker(opts.Config.Diagnostics.Commands)
	if opts.LSPSource != nil {
		c.SetLSPSource(opts.LSPSource)
	}
	return c
}

type diagnosticsArgs struct {
	Path string `json:"path"`
}

func (t *toolSet) diagnosticsCheckTool() registry.Tool {
	tool := registry.Tool{
		Name:        "diagnostics.check",
		Description: "Run the configured language checker on a file or package and return findings.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`),
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

// languageOf returns the first detectable language among paths.
func languageOf(paths []string) string {
	for _, p := range paths {
		if lang := repo.DetectLanguage(p); lang != "" {
			return lang
		}
	}
	return ""
}
