package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type agentAwaitArgs struct {
	ID  int64 `json:"id"`
	All bool  `json:"all"`
	Any bool  `json:"any"`
}

// NewSubagentAwaitTool returns the registry.Tool entry for agent.await, the
// blocking half of the async subagent contract: the model calls it when it
// genuinely needs a background child's result before continuing.
func NewSubagentAwaitTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "agent.await",
		Description: `Wait for background subagents started by agent.run. Pass "id" to wait for one specific subagent, "any": true to return as soon as the first outstanding subagent finishes, or "all": true to wait for every outstanding subagent. Exactly one of the three. Prefer "any" when you are at the concurrency cap and want to start more work as soon as a slot frees — "all" blocks until the slowest child finishes. Blocks until the target(s) finish or the turn is cancelled — there is no timeout. Each subagent's report is also delivered to you automatically when it finishes.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"any":{"type":"boolean","description":"Return as soon as the first outstanding subagent finishes."},"all":{"type":"boolean","description":"Wait for all outstanding subagents."}},"additionalProperties":false}`),
		// MUST stay read-only: a blocking handler that held the write gate
		// would deadlock the very child it is waiting for, once the parent
		// runner carries a WriteGate.
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args agentAwaitArgs
		if len(call.Args) > 0 {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
			}
		}
		modes := 0
		if args.ID != 0 {
			modes++
		}
		if args.All {
			modes++
		}
		if args.Any {
			modes++
		}
		if modes != 1 {
			return registry.ToolResult{}, fmt.Errorf("%s requires exactly one of \"id\", \"any\": true, or \"all\": true", tool.Name)
		}
		if args.Any {
			alreadyFinished := make(map[int64]bool)
			for _, v := range state.Subagents() {
				if v.Child != nil && v.Status != session.SubagentRunning {
					alreadyFinished[v.ID] = true
				}
			}
			var pending []int64
			for _, v := range state.Subagents() {
				if v.Child != nil && !alreadyFinished[v.ID] {
					pending = append(pending, v.ID)
				}
			}
			if len(pending) == 0 {
				return registry.ToolResult{
					Summary: "no running subagents",
					Content: "No background subagents are currently running.",
				}, nil
			}
			state.SetActiveToolCallArgs(fmt.Sprintf("any (%d running)", len(pending)))
			v, err := state.WaitAnySubagent(ctx, pending)
			if err != nil {
				return registry.ToolResult{}, err
			}
			line, content := subagentResultText(v.ID, v.Label, v.Summary, v.SalvagedReason, v.Error)
			return registry.ToolResult{Summary: line, Content: content}, nil
		}
		if !args.All {
			// I-3: before blocking, check if the child is pending user
			// approval. If so, return immediately with a liveness notice
			// instead of blocking the parent turn indefinitely — the child
			// is waiting on the same user, and blocking here would tie up
			// the parent with no way for the user to address the approval
			// through the parent's turn.
			if v, ok := state.Subagent(args.ID); ok {
				if v.Status == session.SubagentRunning && v.Child != nil {
					if pa := v.Child.PendingApproval(); pa != nil {
						return registry.ToolResult{
							Summary: fmt.Sprintf("subagent %d is waiting for user approval", args.ID),
							Content: fmt.Sprintf("Subagent %d (%s) is blocked waiting for user approval of %s. It will continue once the approval is resolved. You can continue other work and call agent.await again later, or address the approval in the subagent's panel.", args.ID, v.Label, pa.Name),
						}, nil
					}
				}
			}
			// Single-ID wait.
			v, err := state.WaitSubagent(ctx, args.ID)
			if err != nil {
				return registry.ToolResult{}, err
			}
			line, content := subagentResultText(v.ID, v.Label, v.Summary, v.SalvagedReason, v.Error)
			return registry.ToolResult{
				Summary: line,
				Content: content,
			}, nil
		}
		// "all": wait for every outstanding background child. Loop so children
		// registered after the initial snapshot are included, and only real
		// background children (Child != nil) are waited on — pipeline/SDD
		// cards share the parent's state and are not agent.run children.
		// waited tracks IDs already collected so a child that finishes
		// between scans is still picked up (it is no longer Running, but it
		// is still a background child we have not yet reported).
		//
		// M-1: snapshot the set of children that are already finished
		// before the call starts. Those are skipped (their reports were
		// already delivered via the queue drain or the persisted RoleUser
		// message). Children that are still Running, or that finish during
		// this call, are collected — a child that finishes between scans
		// is picked up because it was in the initial pending set.
		var lines, bodies []string
		waited := make(map[int64]bool)
		alreadyFinished := make(map[int64]bool)
		for _, v := range state.Subagents() {
			if v.Child != nil && v.Status != session.SubagentRunning {
				alreadyFinished[v.ID] = true
			}
		}
		for {
			var pending []int64
			for _, v := range state.Subagents() {
				if v.Child != nil && !waited[v.ID] && !alreadyFinished[v.ID] {
					pending = append(pending, v.ID)
				}
			}
			if len(pending) == 0 {
				break
			}
			for i, id := range pending {
				state.SetActiveToolCallArgs(fmt.Sprintf("all (%d running)", len(pending)-i))
				v, err := state.WaitSubagent(ctx, id)
				if err != nil {
					// Preserve partial results: a cancelled batch must not
					// discard the siblings that already finished.
					if len(lines) > 0 {
						return registry.ToolResult{
							Summary: strings.Join(lines, "\n"),
							Content: strings.Join(bodies, "\n\n"),
						}, err
					}
					return registry.ToolResult{}, err
				}
				waited[id] = true
				line, content := subagentResultText(v.ID, v.Label, v.Summary, v.SalvagedReason, v.Error)
				lines = append(lines, line)
				bodies = append(bodies, content)
			}
		}
		if len(lines) == 0 {
			return registry.ToolResult{
				Summary: "no running subagents",
				Content: "No background subagents are currently running.",
			}, nil
		}
		return registry.ToolResult{
			Summary: strings.Join(lines, "\n"),
			Content: strings.Join(bodies, "\n\n"),
		}, nil
	}
	return tool
}

type agentOutputArgs struct {
	ID        int64 `json:"id"`
	TailLines int   `json:"tail_lines"`
}

// NewSubagentOutputTool returns the registry.Tool entry for agent.output,
// the non-blocking peek at a background subagent — mirroring job.output
// (internal/tools/native/jobs.go).
func NewSubagentOutputTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "agent.output",
		Description: `Peek at a background subagent started by agent.run without waiting for it: returns its status (running/finished/failed), its final report once finished (or the failure text), and a short tail of its recent activity while running. Use agent.await to block until a subagent finishes.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"tail_lines":{"type":"integer","description":"Number of recent activity lines to include while running (default 5)."}},"required":["id"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args agentOutputArgs
		if len(call.Args) > 0 {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
			}
		}
		if args.ID == 0 {
			return registry.ToolResult{}, fmt.Errorf("%s requires \"id\"", tool.Name)
		}
		if args.TailLines <= 0 {
			args.TailLines = 5
		}
		v, ok := state.Subagent(args.ID)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("agent.output: unknown subagent id %d", args.ID)
		}
		status := "running"
		switch v.Status {
		case session.SubagentDone:
			status = "finished"
		case session.SubagentFailed:
			status = "failed"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "status: %s\nlabel: %s\n", status, v.Label)
		switch v.Status {
		case session.SubagentRunning:
			if tail := subagentActivityTail(v.Child, args.TailLines); len(tail) > 0 {
				b.WriteString("\nrecent activity:\n")
				for _, line := range tail {
					b.WriteString(line + "\n")
				}
			}
		case session.SubagentDone:
			if v.Summary != "" {
				b.WriteString("\n" + v.Summary + "\n")
			}
		case session.SubagentFailed:
			b.WriteString("\nerror: " + v.Error + "\n")
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("subagent %d is %s", v.ID, status),
			Content: strings.TrimRight(b.String(), "\n"),
		}, nil
	}
	return tool
}

type agentKillArgs struct {
	ID int64 `json:"id"`
}

// NewSubagentKillTool returns the registry.Tool entry for agent.kill, the
// parent-agent counterpart to the TUI's per-child interrupt (keypress.go:398
// calls State.CancelSubagent with the drilled-in card's ID). It cancels the
// child's context; the child's own completion goroutine then finishes the
// view (FinishSubagent marks it failed with the cancellation error) and
// delivers the report like any other completion, so the parent observes the
// terminal state with agent.await or agent.output rather than getting a
// synchronous completion here.
func NewSubagentKillTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "agent.kill",
		Description: `Cancel a background subagent started by agent.run. Kills are immediate but asynchronous: the child's context is cancelled and its normal completion path marks it failed with "context canceled" and delivers a [subagent N failed] report, so call agent.await (or agent.output) afterwards to observe the terminal state. Returns "killed", "already finished", or an error for an unknown id. Kills cannot be used on pipeline/SDD cards that share this session (they have no cancel handle).`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."}},"required":["id"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args agentKillArgs
		if len(call.Args) > 0 {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
			}
		}
		if args.ID == 0 {
			return registry.ToolResult{}, fmt.Errorf("%s requires \"id\"", tool.Name)
		}
		v, ok := state.Subagent(args.ID)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("agent.kill: unknown subagent id %d", args.ID)
		}
		if v.Status != session.SubagentRunning {
			detail := v.Summary
			if v.Error != "" {
				detail = "error: " + v.Error
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("subagent %d already finished", args.ID),
				Content: fmt.Sprintf("Subagent %d (%s) already finished with status %s — nothing to kill. %s", args.ID, v.Label, subagentStatusName(v.Status), detail),
			}, nil
		}
		if !state.CancelSubagent(args.ID) {
			return registry.ToolResult{
				Summary: fmt.Sprintf("subagent %d cannot be killed", args.ID),
				Content: fmt.Sprintf("Subagent %d (%s) is running but has no cancel handle — it was not started by agent.run (a pipeline/SDD card sharing this session) and cannot be killed from here.", args.ID, v.Label),
			}, nil
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("killed subagent %d", args.ID),
			Content: fmt.Sprintf("Subagent %d (%s) cancelled. Its completion path will mark it failed (\"context canceled\") and deliver a [subagent %d failed] report; call agent.await or agent.output with \"id\": %d to observe the terminal state.", args.ID, v.Label, args.ID, args.ID),
		}, nil
	}
	return tool
}

// subagentStatusName renders a SubagentStatus as a human-readable word for
// tool result text. SubagentStatus is an int with no String() method, so the
// agent tools map it explicitly rather than printing a raw integer.
func subagentStatusName(s session.SubagentStatus) string {
	switch s {
	case session.SubagentDone:
		return "done"
	case session.SubagentFailed:
		return "failed"
	default:
		return "running"
	}
}

// subagentActivityTail delegates to the shared session implementation so
// agent.output and the TUI card read the same tail source and cannot drift.
func subagentActivityTail(child *session.State, n int) []string {
	if child == nil {
		return nil
	}
	return child.SubagentActivityTail(n)
}
