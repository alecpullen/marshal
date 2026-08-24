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
}

// NewSubagentAwaitTool returns the registry.Tool entry for agent.await, the
// blocking half of the async subagent contract: the model calls it when it
// genuinely needs a background child's result before continuing.
func NewSubagentAwaitTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name: "agent.await",
		Description: `Wait for a background subagent started by agent.run to finish and return its report. Pass "id" (from the agent.run start message) to wait for one subagent, or "all": true to wait for every outstanding subagent. Blocks until the target(s) finish or the turn is cancelled — there is no timeout. Prefer continuing other work when the result is not needed yet; each subagent's report is also delivered to you automatically when it finishes.`,
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"all":{"type":"boolean","description":"Wait for all outstanding subagents."}},"additionalProperties":false}`),
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
		if !args.All && args.ID == 0 {
			return registry.ToolResult{}, fmt.Errorf("%s requires \"id\" or \"all\": true", tool.Name)
		}
		var ids []int64
		if args.All {
			for _, v := range state.Subagents() {
				if v.Status == session.SubagentRunning {
					ids = append(ids, v.ID)
				}
			}
			if len(ids) == 0 {
				return registry.ToolResult{
					Summary: "no running subagents",
					Content: "No background subagents are currently running.",
				}, nil
			}
		} else {
			ids = []int64{args.ID}
		}
		// Sequential waits are correct for all: the total wait is bounded by
		// the last finisher regardless of order.
		var lines, bodies []string
		for _, id := range ids {
			v, err := state.WaitSubagent(ctx, id)
			if err != nil {
				return registry.ToolResult{}, err
			}
			line, content := subagentResultText(v.ID, v.Label, v.Summary, v.SalvagedReason, v.Error)
			lines = append(lines, line)
			bodies = append(bodies, content)
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
		Name: "agent.output",
		Description: `Peek at a background subagent started by agent.run without waiting for it: returns its status (running/finished/failed), its final report once finished (or the failure text), and a short tail of its recent activity while running. Use agent.await to block until a subagent finishes.`,
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"tail_lines":{"type":"integer","description":"Number of recent activity lines to include while running (default 5)."}},"required":["id"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
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

// subagentActivityTail mirrors the TUI card's tail source
// (internal/app/tui/transcript.go subagentTailLines): prefer the trailing
// end of streamed reasoning, then recent audit-log result summaries. The
// agent package cannot import the TUI, so the logic is duplicated here.
func subagentActivityTail(child *session.State, n int) []string {
	if child == nil || n <= 0 {
		return nil
	}
	ip := child.InProgress()
	if ip.Reasoning != "" {
		lines := strings.Split(strings.TrimSpace(ip.Reasoning), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return lines
	}
	log := child.AuditLog()
	if len(log) == 0 {
		return nil
	}
	var out []string
	for i := len(log) - 1; i >= 0 && len(out) < n; i-- {
		if log[i].ResultSummary != "" {
			out = append([]string{log[i].ResultSummary}, out...)
		}
	}
	return out
}
