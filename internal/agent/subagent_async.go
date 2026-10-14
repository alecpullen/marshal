package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type agentAwaitArgs struct {
	ID             int64  `json:"id"`
	All            bool   `json:"all"`
	Any            bool   `json:"any"`
	JobID          string `json:"job_id"`
	Peek           bool   `json:"peek"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type awaitOptions struct {
	jobs JobAwaitSource
}

// AwaitOption customizes the agent.await tool's awaitable classes.
type AwaitOption func(*awaitOptions)

// WithAwaitJobs wires the background-job await source. Nil (the default)
// disables job awaiting: job_id errors clearly and any/all skip jobs.
func WithAwaitJobs(src JobAwaitSource) AwaitOption {
	return func(o *awaitOptions) { o.jobs = src }
}

// NewSubagentAwaitTool returns the registry.Tool entry for agent.await, the
// blocking half of the async subagent contract: the model calls it when it
// genuinely needs a background child's result before continuing.
func NewSubagentAwaitTool(state *session.State, opts ...AwaitOption) registry.Tool {
	var cfg awaitOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	tool := registry.Tool{
		Name:        "agent.await",
		Description: `Wait for background work: a subagent ("id"), a background shell job ("job_id"), the first to finish ("any": true), or everything ("all": true). Pass exactly one. "peek": true with "id" or "job_id" returns current status and recent output immediately instead of waiting. "timeout_seconds" bounds a wait; on expiry you get what is still running. Subagent reports also arrive on their own when they finish.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"job_id":{"type":"string"},"any":{"type":"boolean"},"all":{"type":"boolean"},"peek":{"type":"boolean"},"timeout_seconds":{"type":"integer"}},"additionalProperties":false}`),
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
		if args.JobID != "" {
			modes++
		}
		if modes != 1 {
			return registry.ToolResult{}, fmt.Errorf("%s requires exactly one of \"id\", \"any\": true, \"all\": true, or \"job_id\"", tool.Name)
		}
		if args.Peek {
			switch {
			case args.ID != 0:
				return peekSubagent(state, args.ID)
			case args.JobID != "":
				if cfg.jobs == nil {
					return registry.ToolResult{}, fmt.Errorf("%s: background jobs are not available", tool.Name)
				}
				info, tail, err := cfg.jobs.PeekJob(args.JobID, 20)
				if err != nil {
					return registry.ToolResult{}, err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "status: %s\ncommand: %s\n", info.Status, info.Command)
				if info.ExitCode != nil {
					fmt.Fprintf(&b, "exit: %d\n", *info.ExitCode)
				}
				if tail != "" {
					b.WriteString("\n" + tail)
				}
				return registry.ToolResult{Summary: fmt.Sprintf("job %s is %s", info.ID, info.Status), Content: strings.TrimRight(b.String(), "\n")}, nil
			default:
				return registry.ToolResult{}, fmt.Errorf("%s: peek needs \"id\" or \"job_id\"", tool.Name)
			}
		}
		if args.TimeoutSeconds < 0 {
			return registry.ToolResult{}, fmt.Errorf("%s: timeout_seconds must be >= 0", tool.Name)
		}
		if args.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		if args.Any {
			p := cfg.scanPending(state)
			if p.count() == 0 {
				return noOutstandingResult(), nil
			}
			state.SetActiveToolCallArgs(call.ID, awaitActiveLabel("any", p))
			// Fan out one goroutine per pending target. The arrivals channel
			// is buffered to the target count, so no loser ever blocks on
			// send; once the first arrival is consumed, the deferred cancel
			// stops the wait context and releases the remaining goroutines.
			type awaitArrival struct {
				class string
				sub   session.SubagentView
				job   AwaitJobInfo
				err   error
			}
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			arrivals := make(chan awaitArrival, p.count())
			for _, id := range p.subIDs {
				id := id
				go func() {
					v, err := state.WaitSubagent(waitCtx, id)
					arrivals <- awaitArrival{class: "subagent", sub: v, err: err}
				}()
			}
			for _, job := range p.jobs {
				job := job
				go func() {
					info, err := cfg.jobs.AwaitJob(waitCtx, job.ID)
					arrivals <- awaitArrival{class: "job", job: info, err: err}
				}()
			}
			select {
			case arr := <-arrivals:
				if arr.err != nil {
					return registry.ToolResult{}, arr.err
				}
				var line, content string
				switch arr.class {
				case "job":
					line, content = jobResultText(arr.job, cfg.jobs.JobOutputTail(arr.job.ID, jobAwaitTailLines))
				default:
					v := arr.sub
					line, content = subagentResultText(v.ID, v.Label, v.Summary, v.SalvagedReason, v.Error)
				}
				return registry.ToolResult{Summary: line, Content: content}, nil
			case <-ctx.Done():
				err := ctx.Err()
				if errors.Is(err, context.DeadlineExceeded) {
					return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, nil), nil
				}
				return registry.ToolResult{}, err
			}
		}
		if args.JobID != "" {
			if cfg.jobs == nil {
				return registry.ToolResult{}, fmt.Errorf("agent.await: no background-job source is wired in this session")
			}
			info, err := cfg.jobs.AwaitJob(ctx, args.JobID)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, nil), nil
				}
				return registry.ToolResult{}, err
			}
			line, content := jobResultText(info, cfg.jobs.JobOutputTail(info.ID, jobAwaitTailLines))
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
				if errors.Is(err, context.DeadlineExceeded) {
					return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, nil), nil
				}
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
		jobSeen := make(map[string]bool)
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
			p := cfg.scanPending(state)
			var jobs []AwaitJobInfo
			for _, job := range p.jobs {
				if !jobSeen[job.ID] {
					jobs = append(jobs, job)
				}
			}
			if len(pending) == 0 && len(jobs) == 0 {
				break
			}
			for _, id := range pending {
				state.SetActiveToolCallArgs(call.ID, awaitActiveLabel("all", cfg.scanPending(state)))
				v, err := state.WaitSubagent(ctx, id)
				if err != nil {
					// Preserve partial results: a cancelled batch must not
					// discard the siblings that already finished.
					if errors.Is(err, context.DeadlineExceeded) {
						return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, lines), nil
					}
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
			for _, job := range jobs {
				state.SetActiveToolCallArgs(call.ID, awaitActiveLabel("all", cfg.scanPending(state)))
				info, err := cfg.jobs.AwaitJob(ctx, job.ID)
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, lines), nil
					}
					if len(lines) > 0 {
						return registry.ToolResult{
							Summary: strings.Join(lines, "\n"),
							Content: strings.Join(bodies, "\n\n"),
						}, err
					}
					return registry.ToolResult{}, err
				}
				jobSeen[job.ID] = true
				line, content := jobResultText(info, cfg.jobs.JobOutputTail(info.ID, jobAwaitTailLines))
				lines = append(lines, line)
				bodies = append(bodies, content)
			}
		}
		if len(lines) == 0 {
			return noOutstandingResult(), nil
		}
		return registry.ToolResult{
			Summary: strings.Join(lines, "\n"),
			Content: strings.Join(bodies, "\n\n"),
		}, nil
	}
	return tool
}

// awaitPending is one scan of everything outstanding across the awaitable
// classes. Subagents use the same alreadyFinished snapshot rule as today;
// jobs come from their source when wired.
type awaitPending struct {
	subIDs []int64
	jobs   []AwaitJobInfo
}

func (a awaitOptions) scanPending(state *session.State) awaitPending {
	p := awaitPending{}
	// Subagents: the existing two-pass alreadyFinished/pending pattern
	// (Child != nil, pipeline cards excluded).
	alreadyFinished := make(map[int64]bool)
	for _, v := range state.Subagents() {
		if v.Child != nil && v.Status != session.SubagentRunning {
			alreadyFinished[v.ID] = true
		}
	}
	for _, v := range state.Subagents() {
		if v.Child != nil && !alreadyFinished[v.ID] {
			p.subIDs = append(p.subIDs, v.ID)
		}
	}
	if a.jobs != nil {
		p.jobs = a.jobs.OutstandingJobs()
	}
	return p
}

// count totals the outstanding targets across classes.
func (p awaitPending) count() int { return len(p.subIDs) + len(p.jobs) }

// awaitActiveLabel builds the active-tool label for an await call. With only
// subagents pending it produces today's exact strings ("any (2 running)" /
// "all (2 running)"), which TestAwaitAllUpdatesActiveToolCallArgs pins; with
// jobs pending as well it appends their count.
func awaitActiveLabel(mode string, p awaitPending) string {
	if len(p.jobs) == 0 {
		return fmt.Sprintf("%s (%d running)", mode, len(p.subIDs))
	}
	parts := make([]string, 0, 2)
	if len(p.subIDs) > 0 {
		parts = append(parts, fmt.Sprintf("%d subagents", len(p.subIDs)))
	}
	parts = append(parts, fmt.Sprintf("%d job(s) running", len(p.jobs)))
	return fmt.Sprintf("%s (%s)", mode, strings.Join(parts, ", "))
}

// noOutstandingResult is the empty-scan result shared by any and all.
func noOutstandingResult() registry.ToolResult {
	return registry.ToolResult{
		Summary: "no outstanding background work",
		Content: "No background subagents or jobs are currently outstanding.",
	}
}

// awaitTimeoutResult renders the normal (non-error) result for an await that
// expired via timeout_seconds. It rescans pending so the "still running"
// count is current; partial lines collected by an all-wait are appended.
func (a awaitOptions) awaitTimeoutResult(state *session.State, timeoutSeconds int, partial []string) registry.ToolResult {
	p := a.scanPending(state)
	summary := fmt.Sprintf("timed out after %ds; %d target(s) still running", timeoutSeconds, p.count())
	content := summary
	if len(partial) > 0 {
		content += "\n\n" + strings.Join(partial, "\n")
	}
	return registry.ToolResult{Summary: summary, Content: content}
}

// jobAwaitTailLines bounds the output tail included in job await results.
const jobAwaitTailLines = 40

// jobResultText renders a finished job's await result. exit renders as
// "n/a" when the job died without an exit code (killed/timeout).
func jobResultText(info AwaitJobInfo, tail string) (summaryLine, content string) {
	exit := "n/a"
	if info.ExitCode != nil {
		exit = fmt.Sprintf("%d", *info.ExitCode)
	}
	summaryLine = fmt.Sprintf("job %s %s (exit %s): %s", info.ID, info.Status, exit, info.Command)
	content = summaryLine
	if tail != "" {
		content += "\n\noutput tail:\n" + tail
	}
	return summaryLine, content
}

// peekSubagent is agent.await's non-blocking "peek": true path for a subagent
// target: its status, its final report once finished (or the failure text),
// and a short tail of recent activity while running.
func peekSubagent(state *session.State, id int64) (registry.ToolResult, error) {
	v, ok := state.Subagent(id)
	if !ok {
		return registry.ToolResult{}, fmt.Errorf("agent.await: unknown subagent id %d", id)
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
		if tail := subagentActivityTail(v.Child, 5); len(tail) > 0 {
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

type agentKillArgs struct {
	ID    int64  `json:"id"`
	JobID string `json:"job_id"`
}

// NewSubagentKillTool returns the registry.Tool entry for agent.kill, the
// parent-agent counterpart to the TUI's per-child interrupt (keypress.go:398
// calls State.CancelSubagent with the drilled-in card's ID). It cancels the
// child's context; the child's own completion goroutine then finishes the
// view (FinishSubagent marks it failed with the cancellation error) and
// delivers the report like any other completion, so the parent observes the
// terminal state with agent.await rather than getting a synchronous
// completion here. It also kills a background shell job ("job_id").
func NewSubagentKillTool(state *session.State, opts ...AwaitOption) registry.Tool {
	var cfg awaitOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	tool := registry.Tool{
		Name:        "agent.kill",
		Description: `Cancel a background subagent ("id") or shell job ("job_id"). Kills are immediate but asynchronous: the child's context is cancelled and its completion path marks it failed with "context canceled". A killed subagent's report arrives marked failed; observe a terminal state with agent.await (add "peek": true to check without waiting).`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"job_id":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args agentKillArgs
		if len(call.Args) > 0 {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
			}
		}
		if (args.ID == 0) == (args.JobID == "") {
			return registry.ToolResult{}, fmt.Errorf("%s requires exactly one of \"id\" or \"job_id\"", tool.Name)
		}
		if args.JobID != "" {
			if cfg.jobs == nil {
				return registry.ToolResult{}, fmt.Errorf("%s: background jobs are not available", tool.Name)
			}
			if err := cfg.jobs.KillJob(args.JobID); err != nil {
				return registry.ToolResult{}, err
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("killed job %s", args.JobID),
				Content: fmt.Sprintf("job_id: %s killed", args.JobID),
			}, nil
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
				Content: fmt.Sprintf("Subagent %d (%s) is running but no cancel handle is stored for this card (it may be a pipeline/SDD card sharing this session, or a card whose cancel was already consumed), so it cannot be killed from here.", args.ID, v.Label),
			}, nil
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("killed subagent %d", args.ID),
			Content: fmt.Sprintf("Subagent %d (%s) cancelled. Its completion path will mark it failed (\"context canceled\"). If it was started by agent.run, a [subagent %d failed] report will be delivered; otherwise (a pipeline/SDD card) no report is pushed — use agent.await with \"id\": %d and \"peek\": true to observe its terminal state.", args.ID, v.Label, args.ID, args.ID),
		}, nil
	}
	return tool
}

type agentSteerArgs struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

// NewSubagentSteerTool builds the parent-side steering affordance: it turns an
// instruction into a steering message on the LIVE child's own state, so the
// child sees it at its next loop-top exactly like a user steering message.
// Reusing PushSteering means the instruction rides the existing, already-tested
// steering path rather than a new delivery mechanism.
func NewSubagentSteerTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name: "agent.steer",
		Description: "Send an instruction to a running subagent; it arrives as steering at the child's next step " +
			"(e.g. \"wrap up and report now\", an environment note, a scope change). A finished subagent cannot be steered.",
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"The subagent id shown on its card, e.g. 3 for \"subagent 3\"."},"message":{"type":"string","description":"The instruction to deliver, phrased as an instruction to the subagent."}},"required":["id","message"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}
	tool.Handler = func(_ context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args agentSteerArgs
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode agent.steer arguments: %w", err)
		}
		message := strings.TrimSpace(args.Message)
		if message == "" {
			return registry.ToolResult{}, errors.New("message must not be empty")
		}
		view, ok := state.Subagent(args.ID)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("unknown subagent %d", args.ID)
		}
		if view.Child == nil {
			return registry.ToolResult{}, fmt.Errorf("subagent %d has no live session to steer", args.ID)
		}
		// A finished child no longer drains its steering queue, so accepting
		// the instruction would silently drop it and leave the caller
		// believing it landed.
		if view.Status != session.SubagentRunning {
			return registry.ToolResult{}, fmt.Errorf("subagent %d is no longer running; a message cannot reach a finished subagent", args.ID)
		}
		view.Child.PushSteering("[steering from parent] " + message)
		return registry.ToolResult{
			Summary: fmt.Sprintf("steered subagent %d", args.ID),
			Content: fmt.Sprintf("Pushed the instruction to subagent %d (%s); it will see it at its next step.", args.ID, view.Label),
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
// agent.await's peek and the TUI card read the same tail source and cannot
// drift.
func subagentActivityTail(child *session.State, n int) []string {
	if child == nil {
		return nil
	}
	return child.SubagentActivityTail(n)
}
