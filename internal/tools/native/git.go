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
			path, err := resolveWorkspacePath(t.activeRoot(), args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			rel, err := workspaceRel(t.activeRoot(), path)
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
	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.activeRoot(), Timeout: timeout})
	exitCode := result.ExitCode
	content := formatCommandOutput(result.Stdout, result.Stderr)
	return registry.ToolResult{
		Summary:         summary(result.Stdout),
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
		Sandbox:         result.Meta,
	}, err
}

type gitLogArgs struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

type gitBlameArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

const (
	gitLogDefaultLimit = 20
	gitLogMaxLimit     = 100
	gitBlameMaxLines   = 200
)

func (t *toolSet) gitLogTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.log",
		Description: "Show recent git history (hash, date, author, subject), newest first. Optionally scoped to a workspace-relative file path. Use for 'what changed lately' / 'when did this change' questions.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Optional workspace-relative file path to scope history to"},"limit":{"type":"integer","description":"Max commits to show (default 20, max 100)"}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[gitLogArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		limit := args.Limit
		if limit < 1 {
			limit = gitLogDefaultLimit
		}
		if limit > gitLogMaxLimit {
			limit = gitLogMaxLimit
		}
		command := fmt.Sprintf("git log --format='%%h %%ad %%an %%s' --date=short -n %d", limit)
		if args.Path != "" {
			path, err := resolveWorkspacePath(t.activeRoot(), args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			rel, err := workspaceRel(t.activeRoot(), path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			command += " -- " + shellQuote(rel)
		}
		return t.runReadOnlyCommand(ctx, command, 30*time.Second, func(stdout string) string {
			if strings.TrimSpace(stdout) == "" {
				return "no commits"
			}
			return fmt.Sprintf("%d commits", strings.Count(strings.TrimSpace(stdout), "\n")+1)
		})
	}
	return tool
}

func (t *toolSet) gitBlameTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.blame",
		Description: "Show git blame (per-line commit, author, date) for a workspace file. Use start_line/end_line to blame a range; without a range only the first 200 lines are shown. Answers 'who wrote this line and when'.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative file path"},"start_line":{"type":"integer","description":"First line of the range (1-based)"},"end_line":{"type":"integer","description":"Last line of the range"}},"required":["path"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[gitBlameArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		hasRange := args.StartLine > 0 || args.EndLine > 0
		if hasRange && (args.StartLine < 1 || args.EndLine < args.StartLine) {
			return registry.ToolResult{}, fmt.Errorf("git.blame: start_line and end_line must be given together with 1 <= start_line <= end_line")
		}
		path, err := resolveWorkspacePath(t.activeRoot(), args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		rel, err := workspaceRel(t.activeRoot(), path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := "git blame --date=short -- " + shellQuote(rel)
		if hasRange {
			command = fmt.Sprintf("git blame -L %d,%d --date=short -- %s", args.StartLine, args.EndLine, shellQuote(rel))
		}
		summary := func(stdout string) string {
			if hasRange {
				return fmt.Sprintf("blame %s lines %d-%d", rel, args.StartLine, args.EndLine)
			}
			return "blame " + rel
		}
		if hasRange {
			return t.runReadOnlyCommand(ctx, command, 30*time.Second, summary)
		}
		// Whole-file blame is one output line per source line; cap at a line
		// boundary so the byte-level limitOutput backstop never garbles blame
		// columns mid-line.
		return t.runReadOnlyCommandCapped(ctx, command, 30*time.Second, gitBlameMaxLines,
			"\n[first 200 lines shown — use start_line and end_line to see more]", summary)
	}
	return tool
}

// capLines truncates stdout to the first n lines, appending marker when
// truncated. n <= 0 means no cap.
func capLines(s string, n int, marker string) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + marker
}

// runReadOnlyCommandCapped is runReadOnlyCommand with a line cap applied to
// stdout before output shaping; the summary sees the capped stdout.
func (t *toolSet) runReadOnlyCommandCapped(ctx context.Context, command string, timeout time.Duration, maxLines int, capMarker string, summary func(stdout string) string) (registry.ToolResult, error) {
	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.activeRoot(), Timeout: timeout})
	exitCode := result.ExitCode
	stdout := capLines(result.Stdout, maxLines, capMarker)
	content := formatCommandOutput(stdout, result.Stderr)
	return registry.ToolResult{
		Summary:         summary(stdout),
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
		Sandbox:         result.Meta,
	}, err
}
