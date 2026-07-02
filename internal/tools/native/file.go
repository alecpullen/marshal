package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"marshal/internal/tools/registry"
)

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file, optionally limited to a 1-based line range.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["path"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileReadArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.read path is required")
		}
		if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
			return registry.ToolResult{}, fmt.Errorf("file.read start_line must be <= end_line")
		}

		path, err := resolveWorkspacePath(t.root, args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("stat %s: %w", args.Path, err)
		}
		if !info.Mode().IsRegular() {
			return registry.ToolResult{}, fmt.Errorf("%s is not a regular file", args.Path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read %s: %w", args.Path, err)
		}

		content, start, end := selectLines(string(data), args.StartLine, args.EndLine)
		content = limitOutput(content, t.maxOutputBytes)
		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}, nil
	}
	return tool
}

func selectLines(content string, startLine int, endLine int) (string, int, int) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", startLine, endLine
	}
	selected := strings.Join(lines[startLine-1:endLine], "\n")
	if endLine == len(lines) && strings.HasSuffix(content, "\n") {
		selected += "\n"
	}
	return selected, startLine, endLine
}
