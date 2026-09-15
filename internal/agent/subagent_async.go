package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

type agentAwaitArgs struct {
	ID             int64  `json:"id"`
	All            bool   `json:"all"`
	Any            bool   `json:"any"`
	JobID          string `json:"job_id"`
	WatchID        string `json:"watch_id"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type awaitOptions struct {
	jobs    JobAwaitSource
	watches WatchAwaitSource
}

// AwaitOption customizes the agent.await tool's awaitable classes.
type AwaitOption func(*awaitOptions)

// WithAwaitJobs wires the background-job await source. Nil (the default)
// disables job awaiting: job_id errors clearly and any/all skip jobs.
func WithAwaitJobs(src JobAwaitSource) AwaitOption {
	return func(o *awaitOptions) { o.jobs = src }
}

// WithAwaitWatches wires the watch await source. Nil (the default) disables
// watch awaiting: watch_id errors clearly and any/all skip watches.
func WithAwaitWatches(src WatchAwaitSource) AwaitOption {
	return func(o *awaitOptions) { o.watches = src }
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
		Description: `Wait for background work started earlier: subagents (agent.run), background jobs (shell.run with background: true), or watches (watch.start). Pass "id" for one subagent, "job_id" for one background job, or "watch_id" for one watch; "any": true returns as soon as the first outstanding subagent/job/watch finishes; "all": true waits for everything outstanding. Exactly one selection. Repeat-mode watches never finish, so "any"/"all" skip them — await a repeat watch with its watch_id ("next fire"). "timeout_seconds" (default 0 = no timeout) bounds the whole wait; on expiry you get a normal result listing what is still running, so you can re-await or move on. Blocks until the target(s) finish or the turn is cancelled. Subagent reports are also delivered automatically when they finish.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"any":{"type":"boolean","description":"Return as soon as the first outstanding subagent, job, or watch finishes."},"all":{"type":"boolean","description":"Wait for all outstanding subagents, jobs, and once-mode watches."},"job_id":{"type":"string","description":"Background job ID from the shell.run start message."},"watch_id":{"type":"string","description":"Watch ID from the watch.start result."},"timeout_seconds":{"type":"integer","description":"Optional bound on the whole wait; on expiry returns a normal result listing what is still running."}},"additionalProperties":false}`),
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
		if args.WatchID != "" {
			modes++
		}
		if modes != 1 {
			return registry.ToolResult{}, fmt.Errorf("%s requires exactly one of \"id\", \"any\": true, \"all\": true, \"job_id\", or \"watch_id\"", tool.Name)
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
			state.SetActiveToolCallArgs(awaitActiveLabel("any", p))
			// Fan out one goroutine per pending target. The arrivals channel
			// is buffered to the target count, so no loser ever blocks on
			// send; once the first arrival is consumed, the deferred cancel
			// stops the wait context and releases the remaining goroutines.
			type awaitArrival struct {
				class string
				sub   session.SubagentView
				job   AwaitJobInfo
				watch AwaitWatchInfo
				// watchScan is the outstanding-scan snapshot for a watch
				// arrival, kept so a raced watch-gone error can synthesize a
				// terminal entry from fields the error result lacks.
				watchScan AwaitWatchInfo
				err       error
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
			for _, w := range p.watches {
				w := w
				go func() {
					info, err := cfg.watches.AwaitWatch(waitCtx, w.ID)
					arrivals <- awaitArrival{class: "watch", watch: info, watchScan: w, err: err}
				}()
			}
			select {
			case arr := <-arrivals:
				if arr.err != nil {
					// A once-mode watch can fire (or be stopped) between the
					// outstanding scan and this goroutine's WaitFire
					// registration; WaitFire then reports the watch gone.
					// That arrival IS the event this await was waiting for, so
					// synthesize a terminal finisher from the scan snapshot and
					// return it like any other winner. Job-class unknown-job
					// errors are genuine and keep the hard-error path.
					if arr.class == "watch" && isRacedWatchGone(arr.err) {
						line, content := watchResultText(synthesizeRacedWatch(arr.watchScan))
						return registry.ToolResult{Summary: line, Content: content}, nil
					}
					return registry.ToolResult{}, arr.err
				}
				var line, content string
				switch arr.class {
				case "job":
					line, content = jobResultText(arr.job, cfg.jobs.JobOutputTail(arr.job.ID, jobAwaitTailLines))
				case "watch":
					line, content = watchResultText(arr.watch)
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
		if args.WatchID != "" {
			if cfg.watches == nil {
				return registry.ToolResult{}, fmt.Errorf("agent.await: no watch source is wired in this session")
			}
			info, err := cfg.watches.AwaitWatch(ctx, args.WatchID)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, nil), nil
				}
				return registry.ToolResult{}, err
			}
			line, content := watchResultText(info)
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
		watchSeen := make(map[string]bool)
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
			var watches []AwaitWatchInfo
			for _, w := range p.watches {
				if !watchSeen[w.ID] {
					watches = append(watches, w)
				}
			}
			if len(pending) == 0 && len(jobs) == 0 && len(watches) == 0 {
				break
			}
			for _, id := range pending {
				state.SetActiveToolCallArgs(awaitActiveLabel("all", cfg.scanPending(state)))
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
				state.SetActiveToolCallArgs(awaitActiveLabel("all", cfg.scanPending(state)))
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
			for _, w := range watches {
				state.SetActiveToolCallArgs(awaitActiveLabel("all", cfg.scanPending(state)))
				info, err := cfg.watches.AwaitWatch(ctx, w.ID)
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						return cfg.awaitTimeoutResult(state, args.TimeoutSeconds, lines), nil
					}
					// Raced once-watch: it fired or stopped during the
					// scan-to-wait window, so WaitFire reports it gone.
					// Synthesize an honest terminal line (exact transition
					// unavailable) and keep collecting the remaining targets
					// instead of failing the whole call.
					if isRacedWatchGone(err) {
						watchSeen[w.ID] = true
						line, content := watchResultText(synthesizeRacedWatch(w))
						lines = append(lines, line)
						bodies = append(bodies, content)
						continue
					}
					if len(lines) > 0 {
						return registry.ToolResult{
							Summary: strings.Join(lines, "\n"),
							Content: strings.Join(bodies, "\n\n"),
						}, err
					}
					return registry.ToolResult{}, err
				}
				watchSeen[w.ID] = true
				line, content := watchResultText(info)
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

// awaitPending is one scan of everything outstanding across the three
// awaitable classes. Subagents use the same alreadyFinished snapshot rule as
// today; jobs and watches come from their sources when wired.
type awaitPending struct {
	subIDs  []int64
	jobs    []AwaitJobInfo
	watches []AwaitWatchInfo
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
	if a.watches != nil {
		p.watches = a.watches.OutstandingWatches()
	}
	return p
}

// count totals the outstanding targets across classes.
func (p awaitPending) count() int { return len(p.subIDs) + len(p.jobs) + len(p.watches) }

// awaitActiveLabel builds the active-tool label for an await call. With only
// subagents pending it produces today's exact strings ("any (2 running)" /
// "all (2 running)"), which TestAwaitAllUpdatesActiveToolCallArgs pins; with
// other classes pending it appends their counts.
func awaitActiveLabel(mode string, p awaitPending) string {
	if len(p.jobs) == 0 && len(p.watches) == 0 {
		return fmt.Sprintf("%s (%d running)", mode, len(p.subIDs))
	}
	parts := make([]string, 0, 3)
	if len(p.subIDs) > 0 {
		parts = append(parts, fmt.Sprintf("%d subagents", len(p.subIDs)))
	}
	if len(p.jobs) > 0 {
		parts = append(parts, fmt.Sprintf("%d job(s) running", len(p.jobs)))
	}
	if len(p.watches) > 0 {
		parts = append(parts, fmt.Sprintf("%d watch(es) pending", len(p.watches)))
	}
	return fmt.Sprintf("%s (%s)", mode, strings.Join(parts, ", "))
}

// noOutstandingResult is the empty-scan result shared by any and all.
func noOutstandingResult() registry.ToolResult {
	return registry.ToolResult{
		Summary: "no outstanding background work",
		Content: "No background subagents, jobs, or once-mode watches are currently outstanding.",
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

// watchGoneSentinel is the stable substring of the error watch.Manager.WaitFire
// returns for a watch that is no longer registered ("already fired or
// stopped") — the shape a once-mode watch produces when it fires between
// agent.await's outstanding scan and the WaitFire registration. internal/agent
// cannot import internal/watch (the app package wires both; importing would
// close a cycle), so the match is textual and MUST stay in sync with the
// not-found message in watch.go.
const watchGoneSentinel = "not found (already fired or stopped)"

// racedWatchState is the synthesized State of a raced watch-gone finisher:
// the watch left the outstanding set (fired or stopped) during the
// scan-to-wait window, so its exact terminal transition is unavailable.
// WatchAwaitSource adapters never produce it; watchResultText renders it.
const racedWatchState = "raced"

// isRacedWatchGone reports whether err is watch.Manager.WaitFire's not-found
// error for a watch that reached a terminal state during the scan window.
func isRacedWatchGone(err error) bool {
	return err != nil && strings.Contains(err.Error(), watchGoneSentinel)
}

// synthesizeRacedWatch builds the terminal entry for a raced watch-gone
// arrival from the outstanding-scan snapshot: Name/Kind/Condition/Mode are
// real, the terminal state is unknown (racedWatchState), and the fire count
// stays zero because it is unknowable here.
func synthesizeRacedWatch(scan AwaitWatchInfo) AwaitWatchInfo {
	scan.State = racedWatchState
	return scan
}

// watchResultText renders a watch await result: fired/stopped/error per the
// watch's terminal transition, with the same fields watch.status shows.
// racedWatchState entries are raced finishers synthesized by agent.await.
func watchResultText(info AwaitWatchInfo) (summaryLine, content string) {
	switch info.State {
	case "fired":
		summaryLine = fmt.Sprintf("watch %s fired (fire %d)", info.Name, info.FireCount)
	case "stopped":
		summaryLine = fmt.Sprintf("watch %s stopped", info.Name)
	case racedWatchState:
		summaryLine = fmt.Sprintf("watch %s raced to a finish: fired or stopped during the await scan window (terminal state unavailable)", info.Name)
	default:
		summaryLine = fmt.Sprintf("watch %s is %s", info.Name, info.State)
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "watch_id: %s\nname: %s\nkind: %s\n", info.ID, info.Name, info.Kind)
	if info.State == racedWatchState {
		fmt.Fprintf(b, "state: raced (fired or stopped; exact terminal state unavailable)\nfires: unknown\n")
	} else {
		fmt.Fprintf(b, "state: %s\nfires: %d\n", info.State, info.FireCount)
	}
	if info.Condition != "" {
		fmt.Fprintf(b, "condition: %s\n", info.Condition)
	}
	if info.LastSample != "" {
		fmt.Fprintf(b, "last_sample: %s\n", strutil.Truncate(info.LastSample, 800, true))
	}
	if info.LastError != "" {
		fmt.Fprintf(b, "last_error: %s\n", info.LastError)
	}
	return summaryLine, strings.TrimRight(b.String(), "\n")
}

type agentOutputArgs struct {
	ID         int64 `json:"id"`
	TailLines  int   `json:"tail_lines"`
	Transcript bool  `json:"transcript"`
}

// NewSubagentOutputTool returns the registry.Tool entry for agent.output,
// the non-blocking peek at a background subagent — mirroring job.output
// (internal/tools/native/jobs.go).
func NewSubagentOutputTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "agent.output",
		Description: `Peek at a background subagent started by agent.run without waiting for it: returns its status (running/finished/failed), its final report once finished (or the failure text), and a short tail of its recent activity while running. Use agent.await to block until a subagent finishes. Pass "transcript": true to also receive a bounded transcript of the subagent's committed messages — it can be large, so use it only when the status, report, or activity tail is insufficient.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"Subagent ID from the agent.run start message."},"tail_lines":{"type":"integer","description":"Number of recent activity lines to include while running (default 5)."},"transcript":{"type":"boolean","description":"Include a bounded transcript of the subagent's committed messages (role + truncated content per message). Can be large — use only when the summary is insufficient."}},"required":["id"],"additionalProperties":false}`),
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
		if args.Transcript {
			if t := subagentTranscriptText(v.Child); t != "" {
				b.WriteString("\ntranscript:\n")
				b.WriteString(t)
				b.WriteString("\n")
			}
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
		Description: `Cancel a background subagent. Kills are immediate but asynchronous: the child's context is cancelled and its normal completion path marks it failed with "context canceled". For a child started by agent.run, a [subagent N failed] report is delivered; a pipeline/SDD card that shares this session may also be killable when it carries a cancel handle, but it delivers no [subagent N failed] report — use agent.output to observe its terminal state. Returns "killed", "already finished", or an error for an unknown id. A card with no cancel handle cannot be killed from here.`,
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
				Content: fmt.Sprintf("Subagent %d (%s) is running but no cancel handle is stored for this card (it may be a pipeline/SDD card sharing this session, or a card whose cancel was already consumed), so it cannot be killed from here.", args.ID, v.Label),
			}, nil
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("killed subagent %d", args.ID),
			Content: fmt.Sprintf("Subagent %d (%s) cancelled. Its completion path will mark it failed (\"context canceled\"). If it was started by agent.run, a [subagent %d failed] report will be delivered; otherwise (a pipeline/SDD card) no report is pushed — use agent.output with \"id\": %d to observe its terminal state.", args.ID, v.Label, args.ID, args.ID),
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

// maxAgentOutputTranscriptMsgChars truncates each message's content in the
// transcript block; maxAgentOutputTranscriptChars caps the whole block so an
// agent.output result carrying a transcript never approaches the runner's
// DefaultMaxToolResultChars (8000) and gets bluntly re-truncated.
const (
	maxAgentOutputTranscriptMsgChars = 240
	maxAgentOutputTranscriptChars    = 6000
	transcriptTruncationMarker       = "\n[transcript truncated — %d message(s) omitted]"
)

// subagentTranscriptText renders a child's committed message log as
// "N. role: content" lines. Only committed messages are read
// (State.Messages()); the in-flight reasoning buffer is intentionally not
// exposed beyond what SubagentActivityTail already returns. Narration and
// loaded-skill bodies are skipped: narration duplicates the activity tail,
// and skill bodies are reference dumps with no decision value for the
// parent.
func subagentTranscriptText(child *session.State) string {
	if child == nil {
		return ""
	}
	msgs := child.Messages()
	var b strings.Builder
	truncatedCount := 0
	for i, m := range msgs {
		if m.ContentType == session.ContentTypeNarration || m.ContentType == session.ContentTypeSkillBody {
			continue
		}
		if b.Len() >= maxAgentOutputTranscriptChars {
			truncatedCount = len(msgs) - i
			break
		}
		fmt.Fprintf(&b, "%d. %s: %s\n", i, m.Role, strutil.Truncate(m.Content, maxAgentOutputTranscriptMsgChars, true))
	}
	if truncatedCount > 0 {
		fmt.Fprintf(&b, transcriptTruncationMarker, truncatedCount)
	}
	return strings.TrimRight(b.String(), "\n")
}

// subagentActivityTail delegates to the shared session implementation so
// agent.output and the TUI card read the same tail source and cannot drift.
func subagentActivityTail(child *session.State, n int) []string {
	if child == nil {
		return nil
	}
	return child.SubagentActivityTail(n)
}
