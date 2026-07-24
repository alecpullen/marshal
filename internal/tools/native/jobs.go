package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

type jobOutputArgs struct {
	JobID     string `json:"job_id"`
	TailLines int    `json:"tail_lines"`
}

type jobKillArgs struct {
	JobID string `json:"job_id"`
}

func (t *toolSet) jobOutputTool() registry.Tool {
	tool := registry.Tool{
		Name:        "job.output",
		Description: "Return buffered stdout/stderr and status for a background job.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"},"tail_lines":{"type":"integer"}},"required":["job_id"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[jobOutputArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, output, err := t.jobManager.Output(args.JobID, args.TailLines)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("job %s is %s", info.ID, info.Status),
			Content: fmt.Sprintf("status: %s\npid: %d\n\n%s", info.Status, info.PID, output),
		}, nil
	}
	return tool
}

func (t *toolSet) jobKillTool() registry.Tool {
	tool := registry.Tool{
		Name:        "job.kill",
		Description: "Terminate a running background shell job by job ID.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"}},"required":["job_id"],"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[jobKillArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if err := t.jobManager.Kill(args.JobID); err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("killed job %s", args.JobID),
			Content: fmt.Sprintf("job_id: %s killed", args.JobID),
		}, nil
	}
	return tool
}

func (t *toolSet) jobListTool() registry.Tool {
	tool := registry.Tool{
		Name:        "job.list",
		Description: "List all tracked background shell jobs and their statuses.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		jobs := t.jobManager.List()
		var b strings.Builder
		for _, j := range jobs {
			line := fmt.Sprintf("%s  %s  pid=%d  status=%s", j.ID, j.Command, j.PID, j.Status)
			if j.ExitCode != nil {
				line += fmt.Sprintf("  exit=%d", *j.ExitCode)
			}
			b.WriteString(line + "\n")
		}
		content := b.String()
		if content == "" {
			content = "no background jobs"
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("%d background job(s)", len(jobs)),
			Content: content,
		}, nil
	}
	return tool
}
