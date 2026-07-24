package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// SubagentRunnerFactory builds a fresh Runner bound to a fresh child session
// state. The factory deliberately does NOT take the prompt as an argument:
// the caller (agent.run) constructs the runner with the parent's provider,
// session config, and shared policy engine, and then passes the prompt to
// RunTask on the returned runner. This keeps the per-session construction
// (provider resolution, route, registry view sizing, role binding) in one
// place while leaving the prompt fetching/decoding to the tool handler.
type SubagentRunnerFactory func() (*Runner, error)

// runSubagentChild is the default runner runtime the agent.run tool uses to
// execute a child runner. It is a small wrapper around RunTask that returns
// the task summary as a string for tool-result rendering; tests inject a
// stub via SubagentOptionWithExec to avoid spinning a real provider.
func runSubagentChild(ctx context.Context, child *Runner, prompt string) (string, error) {
	if child == nil {
		return "", errors.New("agent.run: child runner is nil")
	}
	if prompt == "" {
		return "", errors.New("agent.run: prompt is required")
	}
	task, err := child.RunTask(ctx, prompt)
	if err != nil {
		return "", err
	}
	if task == nil || task.Summary == "" {
		return "", nil
	}
	return task.Summary, nil
}

// SubtaskScopeView returns a registry filtered to the tool set a subtask
// child is allowed to see. It is broader than the swarm's ReadOnlyView
// (RiskReadOnly-only): it also exposes read+network tools (e.g. web.fetch,
// web.search) so the child can gather external context if needed. It
// deliberately excludes agent.run — nested subagents are forbidden — and
// deferred MCP tools — the child should not auto-load arbitrary MCP
// surfaces during an ad-hoc task.
func SubtaskScopeView(src *registry.Registry) *registry.Registry {
	view := registry.New()
	for _, tool := range src.List() {
		if tool.Deferred {
			continue
		}
		if tool.Name == "agent.run" {
			continue
		}
		switch tool.Risk {
		case registry.RiskReadOnly, registry.RiskNetwork:
		default:
			continue
		}
		_ = view.Register(tool)
	}
	return view
}

// SubagentOption configures NewSubagentTool in tests.
type SubagentOption func(*subagentToolConfig)

type subagentToolConfig struct {
	exec func(ctx context.Context, child *Runner, prompt string) (string, error)
}

// WithSubagentExec overrides the default child runner executor. Used in
// tests so the suite does not need a live provider/model stub.
func WithSubagentExec(exec func(ctx context.Context, child *Runner, prompt string) (string, error)) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.exec = exec
	}
}

// agentRunArgs captures the JSON payload the agent passes to agent.run.
// Description is a short human label used in the tool result summary so the
// parent agent can identify which subtask produced which artefact.
type agentRunArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
}

// NewSubagentTool returns the registry.Tool entry for agent.run. The
// factory builds a fresh subagent runner; the state enforces the depth and
// concurrency guards.
func NewSubagentTool(factory SubagentRunnerFactory, reg *registry.Registry, state *session.State, opts ...SubagentOption) registry.Tool {
	cfg := subagentToolConfig{exec: runSubagentChild}
	for _, opt := range opts {
		opt(&cfg)
	}
	tool := registry.Tool{
		Name:        "agent.run",
		Description: "Delegate a scoped, read-only subtask to a fresh child agent context and return its summary. Maximum depth: 1. Maximum concurrency: 2. The child has no access to write/command tools and cannot spawn further subagents.",
		Schema: json.RawMessage(
			`{"type":"object","properties":{"prompt":{"type":"string","description":"The subtask description passed verbatim to the child agent."},"description":{"type":"string","description":"A short label for the subtask shown in the tool result summary."}},"required":["prompt","description"],"additionalProperties":false}`,
		),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeAgentRunArgs(tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if err := state.EnterSubagent(); err != nil {
			return registry.ToolResult{}, err
		}
		defer state.ExitSubagent()

		child, err := factory()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("agent.run: build child: %w", err)
		}
		summary, err := cfg.exec(ctx, child, args.Prompt)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("subagent completed: %s", args.Description),
			Content: summary,
		}, nil
	}
	return tool
}

func decodeAgentRunArgs(tool registry.Tool, raw json.RawMessage) (agentRunArgs, error) {
	var args agentRunArgs
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return agentRunArgs{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	if args.Prompt == "" {
		return agentRunArgs{}, fmt.Errorf("%s requires non-empty prompt", tool.Name)
	}
	if args.Description == "" {
		return agentRunArgs{}, fmt.Errorf("%s requires non-empty description", tool.Name)
	}
	return args, nil
}
