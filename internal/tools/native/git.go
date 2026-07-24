package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

type gitDiffArgs struct {
	Path string `json:"path"`
}

func (t *toolSet) gitStatusTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.status",
		Description: "Show git status --short for the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if _, err := decodeArgs[struct{}](tool, call.Args); err != nil {
			return registry.ToolResult{}, err
		}
		return t.runReadOnlyCommand(ctx, "git status --short", 30*time.Second, func(stdout string) string {
			if strings.TrimSpace(stdout) == "" {
				return "working tree clean"
			}
			return "working tree has changes"
		})
	}
	return tool
}

func (t *toolSet) gitDiffTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.diff",
		Description: "Show git diff for the workspace or a relative path.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[gitDiffArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := "git diff --"
		if args.Path != "" {
			path, err := resolveWorkspacePath(t.root, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			rel, err := workspaceRel(t.root, path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			command = fmt.Sprintf("git diff -- %s", shellQuote(rel))
		}
		return t.runReadOnlyCommand(ctx, command, 30*time.Second, func(stdout string) string {
			if strings.TrimSpace(stdout) == "" {
				return "no diff"
			}
			return "diff present"
		})
	}
	return tool
}

func (t *toolSet) runReadOnlyCommand(ctx context.Context, command string, timeout time.Duration, summary func(stdout string) string) (registry.ToolResult, error) {
	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.root, Timeout: timeout})
	exitCode := result.ExitCode
	content := formatCommandOutput(result.Stdout, result.Stderr)
	return registry.ToolResult{
		Summary:         summary(result.Stdout),
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
		Sandbox:         result.Meta,
	}, err
}
