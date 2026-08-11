package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// SubagentRequest carries the caller's explicit choices for one agent.run
// child. Agent is "" for an ad-hoc subtask or the name of a configured
// custom agent. Model is an optional "provider/model" pair; "" means use
// the default model selection (the factory's captured base model / the
// named agent's own preset).
//
// Role requests model resolution through the profile's binding for this
// role. It applies only when Model and Agent are both empty, and only
// when the role is explicitly bound in the active profile
// (router.ResolveRoleIfBound); otherwise the default model is used.
type SubagentRequest struct {
	Agent string
	Model string
	Role  routing.AgentRole
}

// SubagentRunnerFactory builds a fresh Runner bound to a fresh child
// session state. The factory deliberately does NOT take the prompt as an
// argument: the caller (agent.run) constructs the runner with the parent's
// provider, session config, and shared policy engine, and then passes the
// prompt to RunTask on the returned runner. This keeps the per-session
// construction (provider resolution, route, registry view sizing, role
// binding) in one place while leaving the prompt fetching/decoding to the
// tool handler.
type SubagentRunnerFactory func(req SubagentRequest) (*Runner, *session.State, error)

// runSubagentChild is the default runner runtime the agent.run tool uses to
// execute a child runner. It is a small wrapper around RunTask that returns
// the task summary and any salvage reason for tool-result rendering; tests
// inject a stub via SubagentOptionWithExec to avoid spinning a real provider.
func runSubagentChild(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error) {
	if child == nil {
		return "", "", errors.New("agent.run: child runner is nil")
	}
	if prompt == "" {
		return "", "", errors.New("agent.run: prompt is required")
	}
	task, err := child.RunTask(ctx, prompt)
	if err != nil {
		return "", "", err
	}
	if task == nil {
		return "", "", nil
	}
	return task.Summary, task.SalvagedReason, nil
}

// SubtaskScopeView returns a registry view for a subtask child. It excludes
// agent.run (nested subagents are forbidden), deferred MCP tools (the child
// should not auto-load arbitrary MCP surfaces during an ad-hoc task), and
// question.ask plus its alias ask_user (a subtask runs in its own orphaned
// child session.State that no ACP client or TUI ever sees — there is no live
// user who could answer a question, so the call would block forever). All
// other tools are visible so the child can perform implementation work;
// their own Risk levels and the shared policy engine still gate approval
// for writes, commands, and destructive tools.
func SubtaskScopeView(src *registry.Registry) *registry.Registry {
	view := registry.New()
	for _, tool := range src.List() {
		if tool.Deferred {
			continue
		}
		if tool.Name == "agent.run" || tool.Name == "question.ask" || tool.Name == "ask_user" {
			continue
		}
		_ = view.Register(tool)
	}
	return view
}

// SubagentOption configures NewSubagentTool in tests.
type SubagentOption func(*subagentToolConfig)

type subagentToolConfig struct {
	exec func(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error)
}

// WithSubagentExec overrides the default child runner executor. Used in
// tests so the suite does not need a live provider/model stub.
func WithSubagentExec(exec func(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error)) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.exec = exec
	}
}

// agentRunArgs captures the JSON payload the agent passes to agent.run.
// Description is a short human label used in the tool result summary so the
// parent agent can identify which subtask produced which artefact.
// Agent is an optional custom-agent name; when set, the factory resolves
// the named agent's overrides. Model is an optional "provider/model" pair
// that overrides the default model selection (or the named agent's own
// preset).
type agentRunArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
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
		Description: "Delegate a scoped subtask to a fresh child agent context and return its summary. Maximum depth: 1. Maximum concurrency: 2. Multiple agent.run calls in a single response run in parallel (max 2 in flight); sibling writes are serialized. Pass `agent` to run as a named custom agent (configured via /agents); omit for an ad-hoc subtask. Pass `model` as an explicit provider/model pair to override the model selection; an explicit `model` takes precedence over the named `agent`'s own preset. The child has the same implementation tools as the parent except nested agent.run and question.ask (its session has no user who could answer). Use this tool when a loaded skill instructs you to dispatch or spawn a subagent.",
		Schema: json.RawMessage(
			`{"type":"object","properties":{"prompt":{"type":"string","description":"The subtask description passed verbatim to the child agent."},"description":{"type":"string","description":"A short label for the subtask shown in the tool result summary."},"agent":{"type":"string","description":"Name of a configured custom agent to run as. Omit for an ad-hoc subtask."},"model":{"type":"string","description":"Optional provider/model pair (e.g. \"openai/gpt-4o-mini\") to run the child on. Omitted uses the default model selection; explicit overrides the named agent's own preset."}},"required":["prompt","description"],"additionalProperties":false}`,
		),
		// The tool itself delegates to a child runner that may execute write
		// tools and shell commands. Treating it as read-only bypassed the
		// pre-write snapshot, WriteGate, and parallel-batch guards.
		Risk: registry.RiskWorkspaceWrite,
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

		child, childState, err := factory(SubagentRequest{Agent: args.Agent, Model: args.Model})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("agent.run: build child: %w", err)
		}
		// Register a summary card in the parent transcript in place of the
		// child's full tool log; the drill-down view reaches the live child
		// transcript through the registered Child state.
		meta := session.SubagentMeta{
			Role: RoleSubtask,
		}
		if child.Model != "" {
			meta.Model = child.Model
		}
		if child.Provider != nil {
			meta.Provider = child.Provider.Name()
		}
		view := state.RegisterSubagentWithMeta(args.Description, childState, meta)
		childCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		state.SetSubagentCancel(view.ID, cancel)
		prev := child.UsageObserver
		child.UsageObserver = func(u schema.TokenUsage) {
			if prev != nil {
				prev(u)
			}
			state.AddSubagentTokens(view.ID, u.PromptTokens+u.CompletionTokens)
		}
		summary, salvagedReason, err := cfg.exec(childCtx, child, args.Prompt)
		if salvagedReason != "" {
			state.SetSubagentSalvaged(view.ID, salvagedReason)
		}
		state.FinishSubagent(view.ID, summary, err)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if salvagedReason != "" {
			result := registry.ToolResult{
				Summary: fmt.Sprintf("subagent completed (salvaged: %s): %s", salvagedReason, args.Description),
				Content: summary,
			}
			result.Content = fmt.Sprintf("[note: this subagent hit its iteration budget (%s) and the report below is partial. Raise [agent] subtask_iterations or the custom agent's max_iterations for a longer budget.]\n\n%s", salvagedReason, summary)
			return result, nil
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
