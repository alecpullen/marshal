package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

const (
	defaultShellTimeout = 120 * time.Second
	maxShellTimeout     = 300 * time.Second
	defaultTestTimeout  = 300 * time.Second
	maxTestTimeout      = 600 * time.Second
)

type shellRunArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Background     bool   `json:"background"`
}

type testRunArgs struct {
	Command string `json:"command"`
}

func (t *toolSet) shellRunTool() registry.Tool {
	tool := registry.Tool{
		Name:        "shell.run",
		Description: "Run a shell command in the workspace with conservative guardrails.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background":{"type":"boolean"}},"required":["command"],"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[shellRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		timeout := clampTimeout(args.TimeoutSeconds, defaultShellTimeout, maxShellTimeout)
		if args.Background {
			command := strings.TrimSpace(args.Command)
			if command == "" {
				return registry.ToolResult{}, fmt.Errorf("command is required")
			}
			if t.guardrail != nil {
				if err := t.guardrail(command); err != nil {
					return registry.ToolResult{}, err
				}
			}
			id, err := t.jobManager.Start(ctx, command, timeout)
			if err != nil {
				return registry.ToolResult{}, err
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("started background job %s", id),
				Content: fmt.Sprintf("job_id: %s", id),
			}, nil
		}
		return t.runShellCommand(ctx, args.Command, timeout)
	}
	return tool
}

func (t *toolSet) testRunTool() registry.Tool {
	tool := registry.Tool{
		Name:        "test.run",
		Description: "Run the configured test command in the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[testRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := args.Command
		if command == "" {
			command = t.testCommand
		}
		return t.runShellCommand(ctx, command, defaultTestTimeout)
	}
	return tool
}

func (t *toolSet) runShellCommand(ctx context.Context, command string, timeout time.Duration) (registry.ToolResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return registry.ToolResult{}, fmt.Errorf("command is required")
	}
	if t.guardrail != nil {
		if err := t.guardrail(command); err != nil {
			return registry.ToolResult{}, err
		}
	}

	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.root, Timeout: timeout})
	exitCode := result.ExitCode
	content := formatCommandOutput(result.Stdout, result.Stderr)
	summary := fmt.Sprintf("command %q exited with code %d", command, result.ExitCode)
	if result.Meta.Enabled && result.Meta.KilledReason != "" {
		summary = fmt.Sprintf("command %q killed: %s", command, result.Meta.KilledReason)
	}
	return registry.ToolResult{
		Summary:         summary,
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
		Sandbox:         result.Meta,
	}, err
}

func clampTimeout(seconds int, defaultTimeout time.Duration, maxTimeout time.Duration) time.Duration {
	if seconds <= 0 {
		return defaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func formatCommandOutput(stdout string, stderr string) string {
	return "stdout:\n" + stdout + "\n\nstderr:\n" + stderr
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
