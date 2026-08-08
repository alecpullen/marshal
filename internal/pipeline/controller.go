package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"marshal/internal/agent"
	"marshal/internal/llm/routing"
	"marshal/internal/worktree"
)

// Phase names, surfaced to the Observer and rendered by the TUI panel.
const (
	PhaseImplementing = "implementing"
	PhaseVerifying    = "verifying"
	PhaseCommitting   = "committing"
	PhaseReviewing    = "reviewing"
	PhaseFixing       = "fixing"
	PhaseBranchReview = "branch review"
	PhaseDone         = "done"
	PhaseBlocked      = "blocked"

	PhaseApplying      = "applying"
	PhaseAsserting     = "asserting"
	PhaseAgentFallback = "agent fallback"
)

// ErrHumanGateRequired is returned by Run when the controller needs the
// human to answer a subagent's question. The caller renders the question,
// collects the answer, calls Answer, and calls Run again; the controller
// resumes from its saved position.
var ErrHumanGateRequired = errors.New("pipeline: human gate required")

// ControllerOpts are the constructor's inputs.
type ControllerOpts struct {
	PlanPath           string
	RepoRoot           string
	Git                worktree.GitOps
	Dispatch           Dispatcher
	Verifier           Verifier
	MaxFixRounds       int
	MaxDispatchRetries int
	AutoEscalate       bool
	Observer           Observer
	TargetBranch       string
	// Strategy selects how the controller executes tasks. StrategyAgent
	// (the zero value) preserves the current model-led behavior.
	Strategy     Strategy
	MaxTokensCfg int

	// Sleep is the delay implementation used between dispatch retries.
	// When nil, sleepCtx is used. Tests override this to avoid real time.
	Sleep func(context.Context, time.Duration) error
}

// Controller executes a plan task-by-task. It is single-threaded: exactly
// one subagent runs at a time.
type Controller struct {
	Plan     *Plan
	Paths    Paths
	Ledger   Ledger
	Git      worktree.GitOps
	Dispatch Dispatcher
	Verifier Verifier
	Worktree worktree.Worktree

	RepoRoot           string
	MaxFixRounds       int
	MaxDispatchRetries int
	// AutoEscalate mirrors the session's auto approval mode: retry a
	// blocked task once on the reviewer tier before opening a human gate.
	AutoEscalate bool
	Observer     Observer
	TargetBranch string

	// Strategy controls deterministic vs agent execution.
	Strategy Strategy
	// PlanIR is the compiled plan, populated when marshal.* blocks are
	// present. Nil for legacy prose-only plans.
	PlanIR *PlanIR

	// Sleep is the delay implementation used between dispatch retries.
	// When nil, sleepCtx is used. Tests override this to avoid real time.
	Sleep func(context.Context, time.Duration) error

	// pendingQuestion and pendingAnswer carry a human gate across Run calls.
	pendingQuestion string
	pendingAnswer   string
	// gateTaskTitle and gateReport carry the context of the open gate: the
	// asking task's title and the implementer's full report, so the human can
	// see why the subagent is asking.
	gateTaskTitle string
	gateReport    string
	// escalated records that the current task already had its automatic
	// retry on the stronger model.
	escalated bool
	// nextTask is the plan task number to resume at.
	nextTask int

	// RunStore is the durable manifest/checkpoint store. When nil, the
	// controller runs without checkpointing (used by tests and callers that
	// opt out of crash recovery).
	RunStore *RunStore

	// runID, lastSeq, curAttempt, curFixRound, maxTokens carry the run's
	// durable identity and progress counters across Run calls and crashes.
	runID       string
	lastSeq     int
	curAttempt  int
	curFixRound int
	maxTokens   int
	// MaxTokensCfg is the configured total-token budget for the run. It is
	// set on the controller from ControllerOpts.MaxTokensCfg and written to
	// the manifest on the first Run.
	MaxTokensCfg int

	UsageTokens int
}

// NewController parses the plan, prepares the run directory, and returns a
// controller ready to Run. It does not touch git.
func NewController(opts ControllerOpts) (*Controller, error) {
	plan, err := ParsePlan(opts.PlanPath)
	if err != nil {
		return nil, err
	}
	paths, err := NewPaths(opts.RepoRoot, plan.Slug)
	if err != nil {
		return nil, err
	}
	if opts.MaxFixRounds <= 0 {
		opts.MaxFixRounds = 3
	}
	if opts.MaxDispatchRetries <= 0 {
		opts.MaxDispatchRetries = 2
	}
	if opts.TargetBranch == "" {
		opts.TargetBranch = "main"
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	return &Controller{
		Plan:               plan,
		Paths:              paths,
		Ledger:             Ledger{Path: paths.Ledger()},
		Git:                opts.Git,
		Dispatch:           opts.Dispatch,
		Verifier:           opts.Verifier,
		RepoRoot:           opts.RepoRoot,
		MaxFixRounds:       opts.MaxFixRounds,
		MaxDispatchRetries: opts.MaxDispatchRetries,
		Sleep:              opts.Sleep,
		AutoEscalate:       opts.AutoEscalate,
		Observer:           opts.Observer,
		TargetBranch:       opts.TargetBranch,
		Strategy:           opts.Strategy,
		MaxTokensCfg:       opts.MaxTokensCfg,
	}, nil
}

// overBudget reports whether the run has consumed its total-token budget.
// maxTokens is the runtime budget (set from MaxTokensCfg or the manifest on
// resume); 0 means unlimited.
func (c *Controller) overBudget() bool {
	return c.maxTokens > 0 && c.UsageTokens >= c.maxTokens
}

// CompletedCount returns the number of tasks the ledger marks complete.
func (c *Controller) CompletedCount() (int, error) {
	done, err := c.Ledger.CompletedTasks()
	if err != nil {
		return 0, err
	}
	return len(done), nil
}

// implementWithRetry dispatches an implementer, retrying transient failures
// with fresh subagents.
func (c *Controller) implementWithRetry(ctx context.Context, taskN int, phase string, role agent.AgentRole, label, prompt string) (ImplementerReport, error) {
	return dispatchWithRetry(ctx, c, taskN, phase, role, prompt, func(ctx context.Context, role agent.AgentRole, prompt string) (ImplementerReport, error) {
		return c.Dispatch.Implement(ctx, role, label, prompt)
	})
}

// reviewWithRetry dispatches a reviewer, retrying transient failures with
// fresh subagents.
func (c *Controller) reviewWithRetry(ctx context.Context, taskN int, phase string, role agent.AgentRole, label, prompt string) (ReviewReport, error) {
	return dispatchWithRetry(ctx, c, taskN, phase, role, prompt, func(ctx context.Context, role agent.AgentRole, prompt string) (ReviewReport, error) {
		return c.Dispatch.Review(ctx, role, label, prompt)
	})
}

// dispatchWithRetry runs fn until it succeeds, the error is not transient,
// or the retry budget is exhausted. It emits progress events and ledger
// notes for each retry.
func dispatchWithRetry[R any](ctx context.Context, c *Controller, taskN int, phase string, role agent.AgentRole, prompt string, fn func(context.Context, agent.AgentRole, string) (R, error)) (R, error) {
	var zero R
	for attempt := 0; attempt <= c.MaxDispatchRetries; attempt++ {
		res, err := fn(ctx, role, prompt)
		if err == nil {
			return res, nil
		}
		if attempt == c.MaxDispatchRetries || !isTransientDispatchError(ctx, err) {
			return zero, err
		}
		c.emitPayload(taskN, 0, phase,
			fmt.Sprintf("retry %d/%d · %s", attempt+1, c.MaxDispatchRetries, shortErr(err)),
			RetryPayload{Attempt: attempt + 1, MaxAttempts: c.MaxDispatchRetries, Err: shortErr(err)})
		_ = c.Ledger.Note("Task %d: retry %d/%d after: %v", taskN, attempt+1, c.MaxDispatchRetries, err)
		if sleepErr := c.Sleep(ctx, retryBackoff(attempt)); sleepErr != nil {
			return zero, sleepErr
		}
	}
	return zero, fmt.Errorf("dispatch retry budget exhausted")
}

// emit sends one progress event with no payload, tolerating a nil Observer.
func (c *Controller) emit(taskN, fixRound int, phase, detail string) {
	c.emitPayload(taskN, fixRound, phase, detail, nil)
}

// emitPayload sends one progress event carrying detail data, tolerating a
// nil Observer. payload is plain data; the app layer decides how to render
// it (or ignores an unrecognised type).
func (c *Controller) emitPayload(taskN, fixRound int, phase, detail string, payload any) {
	if c.Observer == nil {
		return
	}
	c.Observer.Event(Event{
		TaskN:        taskN,
		TotalTasks:   len(c.Plan.Tasks),
		FixRound:     fixRound,
		MaxFixRounds: c.MaxFixRounds,
		Phase:        phase,
		Detail:       detail,
		Payload:      payload,
	})
}

// workDir is where subagents work: the run's worktree once created, the
// repository root before that (and in tests).
func (c *Controller) workDir() string {
	if c.Worktree.Path != "" {
		return c.Worktree.Path
	}
	return c.RepoRoot
}

// checkpoint appends one durable state transition to the run store. It is a
// no-op when no run store is configured. extra lets the caller attach
// transition-specific data (SHAs, gate questions, paths).
func (c *Controller) checkpoint(phase string, taskN int, extra func(*Checkpoint)) {
	if c.RunStore == nil {
		return
	}
	cp := Checkpoint{
		Seq:        c.nextSeq(),
		RunID:      c.runID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Phase:      phase,
		TaskN:      taskN,
		Attempt:    c.curAttempt,
		FixRound:   c.curFixRound,
		TokensUsed: c.UsageTokens,
		TokensMax:  c.maxTokens,
	}
	if extra != nil {
		extra(&cp)
	}
	_ = c.RunStore.AppendCheckpoint(cp)
}

// nextSeq returns the next checkpoint sequence number, starting from the
// last replayed value on a resumed run.
func (c *Controller) nextSeq() int {
	c.lastSeq++
	return c.lastSeq
}

// planHash returns the hex SHA-256 of the plan file, used to detect a plan
// that changed between a crashed run and its resume.
func planHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ledgerHasTask reports whether the ledger already marks task n complete.
func ledgerHasTask(l Ledger, n int) bool {
	done, err := l.CompletedTasks()
	if err != nil {
		return false
	}
	return done[n]
}

// taskResult is one completed task's commit range and final report.
type taskResult struct {
	Base     string
	Head     string
	Report   ImplementerReport
	ExecType ExecType
}

// runTask implements one task and gets it committed. Under agent strategy
// it dispatches an implementer, runs the gate, and commits — the current
// behavior. Under adaptive strategy it applies deterministic operations
// first, runs assertions, then dispatches an agent only for unresolved
// work. Under strict strategy it applies deterministic operations only and
// fails if any work is unresolved.
func (c *Controller) runTask(ctx context.Context, t TaskSpec) (taskResult, error) {
	dir := c.workDir()
	base, err := c.Git.RevParse(dir, "HEAD")
	if err != nil {
		return taskResult{}, fmt.Errorf("pipeline: resolve base for task %d: %w", t.N, err)
	}

	briefPath := c.Paths.Brief(t.N)
	if err := WriteBrief(briefPath, t); err != nil {
		return taskResult{}, err
	}
	if dirty, _ := c.Git.IsDirty(dir); dirty {
		if _, err := os.Stat(c.Paths.Report(t.N)); err == nil {
			if f, err := os.OpenFile(briefPath, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprint(f, "\n## Previous attempt\n\nA previous attempt at this task was interrupted and may have left partial uncommitted changes in the worktree. Review them, keep what is correct, and finish the task.\n")
				f.Close()
			}
		}
	}

	// Determine the task IR for this task, if the plan was compiled.
	var taskIR *TaskIR
	if c.PlanIR != nil {
		for i := range c.PlanIR.Tasks {
			if c.PlanIR.Tasks[i].TaskN == t.N {
				taskIR = &c.PlanIR.Tasks[i]
				break
			}
		}
	}

	// If the strategy is agent or there is no IR, use the legacy path.
	if c.Strategy == StrategyAgent || taskIR == nil || len(taskIR.Operations) == 0 {
		return c.runTaskAgent(ctx, t, briefPath, dir, base)
	}

	// Adaptive or strict: apply deterministic operations.
	return c.runTaskDeterministic(ctx, t, taskIR, briefPath, dir, base)
}

// runTaskAgent is the legacy model-led path, preserved for the agent
// strategy and for tasks with no executable operations.
func (c *Controller) runTaskAgent(ctx context.Context, t TaskSpec, briefPath, dir, base string) (taskResult, error) {
	prompt, err := RenderImplementer(ImplementerPrompt{
		TaskN:          t.N,
		Title:          t.Title,
		Placement:      fmt.Sprintf("Task %d of %d in the plan %q.", t.N, len(c.Plan.Tasks), c.Plan.Slug),
		BriefPath:      briefPath,
		BriefBasename:  filepath.Base(briefPath),
		ReportPath:     c.Paths.Report(t.N),
		ReportBasename: filepath.Base(c.Paths.Report(t.N)),
		WorkDir:        dir,
		Interfaces:     c.interfacesBefore(t.N),
		Answer:         c.takeAnswer(),
	})
	if err != nil {
		return taskResult{}, err
	}

	c.emit(t.N, 0, PhaseImplementing, "")
	c.checkpoint(PhaseImplementing, t.N, func(cp *Checkpoint) {
		cp.BaseSHA = base
		cp.BriefPath = briefPath
	})
	role := routing.RoleSDDImplementer
	if c.escalated {
		role = routing.RoleSDDReviewer
	}
	label := fmt.Sprintf("task %d — %s", t.N, t.Title)
	report, err := c.implementWithRetry(ctx, t.N, PhaseImplementing, role, label, prompt)
	if err != nil {
		return taskResult{}, fmt.Errorf("pipeline: task %d implementer: %w", t.N, err)
	}
	if report.NeedsHuman() {
		return taskResult{Report: report}, c.openGateWithContext(t.N, report.Question, report)
	}
	if report.Status == StatusDoneWithConcerns && report.Concerns != "" {
		_ = c.Ledger.Note("Task %d: implementer concern: %s", t.N, report.Concerns)
		c.emitPayload(t.N, 0, PhaseImplementing, "", ConcernPayload{Text: report.Concerns})
	}
	c.checkpoint("dispatch_finished", t.N, func(cp *Checkpoint) {
		cp.BaseSHA = base
		cp.ReportPath = c.Paths.Report(t.N)
		cp.RequestedRole = string(role)
	})

	// Gate, with fix rounds. Nothing is committed while the gate fails.
	for round := 0; ; round++ {
		c.emit(t.N, round, PhaseVerifying, "")
		c.checkpoint(PhaseVerifying, t.N, func(cp *Checkpoint) {
			cp.BaseSHA = base
			cp.FixRound = round
		})
		res, err := c.Verifier.Run(ctx, dir)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate: %w", t.N, err)
		}
		if res.Skipped {
			const reason = "no build or test command configured"
			_ = c.Ledger.Note("Task %d: gate skipped (%s)", t.N, reason)
			c.emitPayload(t.N, round, PhaseVerifying, "skipped", GateSkippedPayload{Reason: reason})
			break
		}
		if res.OK {
			break
		}
		if round >= c.MaxFixRounds {
			return taskResult{Report: report}, fmt.Errorf("pipeline: task %d still fails `%s` after %d fix rounds", t.N, res.FailedCommand, c.MaxFixRounds)
		}
		c.emitPayload(t.N, round+1, PhaseFixing, res.FailedCommand, VerifyFailedPayload{
			Command:   res.FailedCommand,
			Output:    res.Output,
			Round:     round + 1,
			MaxRounds: c.MaxFixRounds,
		})
		c.checkpoint(PhaseFixing, t.N, func(cp *Checkpoint) {
			cp.BaseSHA = base
			cp.FixRound = round + 1
		})
		fixPrompt, err := RenderFix(FixPrompt{
			TaskN:          t.N,
			BriefPath:      briefPath,
			BriefBasename:  filepath.Base(briefPath),
			ReportPath:     c.Paths.Report(t.N),
			ReportBasename: filepath.Base(c.Paths.Report(t.N)),
			WorkDir:        dir,
			Reason:         fmt.Sprintf("the build and test gate failed on `%s`", res.FailedCommand),
			Findings: []Finding{{
				Severity: SeverityCritical,
				Text:     fmt.Sprintf("`%s` fails:\n\n%s", res.FailedCommand, res.Output),
			}},
			CoveringTests: report.Tests,
		})
		if err != nil {
			return taskResult{}, err
		}
		label := fmt.Sprintf("task %d fix — %s", t.N, t.Title)
		report, err = c.implementWithRetry(ctx, t.N, PhaseFixing, routing.RoleSDDImplementer, label, fixPrompt)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate fixer: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return taskResult{Report: report}, c.openGateWithContext(t.N, report.Question, report)
		}
	}

	head, err := c.commit(t, report)
	if err != nil {
		return taskResult{}, err
	}
	return taskResult{Base: base, Head: head, Report: report, ExecType: ExecAgent}, nil
}

// runTaskDeterministic applies deterministic operations, runs assertions,
// then optionally dispatches an agent for unresolved work.
func (c *Controller) runTaskDeterministic(ctx context.Context, t TaskSpec, taskIR *TaskIR, briefPath, dir, base string) (taskResult, error) {
	var report ImplementerReport
	needsAgent := false

	// 1. Apply mutations (patches and files).
	c.emit(t.N, 0, PhaseApplying, "")
	c.checkpoint(PhaseApplying, t.N, func(cp *Checkpoint) {
		cp.BaseSHA = base
		cp.BriefPath = briefPath
	})
	for _, op := range taskIR.Operations {
		switch o := op.(type) {
		case *PatchOp:
			if o.Status == OpExecutable {
				if err := applyPatchOp(dir, o); err != nil {
					return taskResult{}, fmt.Errorf("pipeline: task %d patch: %w", t.N, err)
				}
			} else {
				needsAgent = true
			}
		case *FileOp:
			if o.Status == OpExecutable {
				if err := applyFileOp(dir, o); err != nil {
					return taskResult{}, fmt.Errorf("pipeline: task %d file: %w", t.N, err)
				}
			} else {
				needsAgent = true
			}
		}
	}

	// 2. Run prepare-phase commands.
	for _, op := range taskIR.Operations {
		if r, ok := op.(*RunOp); ok && r.Status == OpExecutable && r.Phase == "prepare" {
			if err := runRunOp(ctx, dir, c.Verifier.Runner, r); err != nil {
				return taskResult{}, fmt.Errorf("pipeline: task %d prepare: %w", t.N, err)
			}
		}
	}

	// 3. Run assertions.
	c.emit(t.N, 0, PhaseAsserting, "")
	c.checkpoint(PhaseAsserting, t.N, func(cp *Checkpoint) {
		cp.BaseSHA = base
	})
	for _, op := range taskIR.Operations {
		if a, ok := op.(*AssertOp); ok && a.Status == OpExecutable {
			if err := checkAssert(ctx, dir, c.Verifier.Runner, a); err != nil {
				return taskResult{}, fmt.Errorf("pipeline: task %d assert: %w", t.N, err)
			}
		}
	}

	// 4. Agent fallback for unresolved work.
	hasAgentOp := false
	for _, op := range taskIR.Operations {
		if op.OpKind() == "agent" || op.OpStatus() == OpBlocked {
			hasAgentOp = true
			break
		}
	}

	execType := ExecDeterministic
	if needsAgent || hasAgentOp {
		if c.Strategy == StrategyStrict {
			return taskResult{}, fmt.Errorf("pipeline: task %d has unresolved work and strategy is strict", t.N)
		}
		// Adaptive: dispatch one agent for the remaining work.
		c.emit(t.N, 0, PhaseAgentFallback, "")
		c.checkpoint(PhaseAgentFallback, t.N, func(cp *Checkpoint) {
			cp.BaseSHA = base
			cp.BriefPath = briefPath
		})
		prompt, err := RenderImplementer(ImplementerPrompt{
			TaskN:          t.N,
			Title:          t.Title,
			Placement:      fmt.Sprintf("Task %d of %d in the plan %q.", t.N, len(c.Plan.Tasks), c.Plan.Slug),
			BriefPath:      briefPath,
			BriefBasename:  filepath.Base(briefPath),
			ReportPath:     c.Paths.Report(t.N),
			ReportBasename: filepath.Base(c.Paths.Report(t.N)),
			WorkDir:        dir,
			Interfaces:     c.interfacesBefore(t.N),
			Answer:         c.takeAnswer(),
		})
		if err != nil {
			return taskResult{}, err
		}
		label := fmt.Sprintf("task %d — %s (agent fallback)", t.N, t.Title)
		report, err = c.implementWithRetry(ctx, t.N, PhaseAgentFallback, routing.RoleSDDImplementer, label, prompt)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d agent fallback: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return taskResult{Report: report}, c.openGateWithContext(t.N, report.Question, report)
		}
		execType = ExecMixed
	}

	// 5. Verify gate.
	for round := 0; ; round++ {
		c.emit(t.N, round, PhaseVerifying, "")
		c.checkpoint(PhaseVerifying, t.N, func(cp *Checkpoint) {
			cp.BaseSHA = base
			cp.FixRound = round
		})
		res, err := c.Verifier.Run(ctx, dir)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate: %w", t.N, err)
		}
		if res.Skipped {
			break
		}
		if res.OK {
			break
		}
		if c.Strategy == StrategyStrict {
			return taskResult{Report: report}, fmt.Errorf("pipeline: task %d gate failed in strict mode: `%s`:\n%s", t.N, res.FailedCommand, res.Output)
		}
		if round >= c.MaxFixRounds {
			return taskResult{Report: report}, fmt.Errorf("pipeline: task %d still fails `%s` after %d fix rounds", t.N, res.FailedCommand, c.MaxFixRounds)
		}
		c.emitPayload(t.N, round+1, PhaseFixing, res.FailedCommand, VerifyFailedPayload{
			Command:   res.FailedCommand,
			Output:    res.Output,
			Round:     round + 1,
			MaxRounds: c.MaxFixRounds,
		})
		c.checkpoint(PhaseFixing, t.N, func(cp *Checkpoint) {
			cp.BaseSHA = base
			cp.FixRound = round + 1
		})
		fixPrompt, err := RenderFix(FixPrompt{
			TaskN:          t.N,
			BriefPath:      briefPath,
			BriefBasename:  filepath.Base(briefPath),
			ReportPath:     c.Paths.Report(t.N),
			ReportBasename: filepath.Base(c.Paths.Report(t.N)),
			WorkDir:        dir,
			Reason:         fmt.Sprintf("the build and test gate failed on `%s`", res.FailedCommand),
			Findings: []Finding{{
				Severity: SeverityCritical,
				Text:     fmt.Sprintf("`%s` fails:\n\n%s", res.FailedCommand, res.Output),
			}},
			CoveringTests: report.Tests,
		})
		if err != nil {
			return taskResult{}, err
		}
		label := fmt.Sprintf("task %d fix — %s", t.N, t.Title)
		report, err = c.implementWithRetry(ctx, t.N, PhaseFixing, routing.RoleSDDImplementer, label, fixPrompt)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate fixer: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return taskResult{Report: report}, c.openGateWithContext(t.N, report.Question, report)
		}
		execType = ExecMixed
	}

	// 6. Commit.
	head, err := c.commit(t, report)
	if err != nil {
		return taskResult{}, err
	}
	return taskResult{Base: base, Head: head, Report: report, ExecType: execType}, nil
}

// commit records the task's work. The controller — never a subagent —
// writes git history. A clean tree here means the implementer changed
// nothing, which is a failed task, not a silent success.
func (c *Controller) commit(t TaskSpec, report ImplementerReport) (string, error) {
	dir := c.workDir()
	dirty, err := c.Git.IsDirty(dir)
	if err != nil {
		return "", fmt.Errorf("pipeline: task %d status: %w", t.N, err)
	}
	if !dirty {
		return "", fmt.Errorf("pipeline: task %d left the working tree unchanged", t.N)
	}
	c.emit(t.N, 0, PhaseCommitting, "")
	msg := fmt.Sprintf("%s: task %d — %s", c.Plan.Slug, t.N, t.Title)
	if report.Tests != "" {
		msg += "\n\n" + report.Tests
	}
	head, err := c.Git.CommitAll(dir, msg)
	if err != nil {
		return "", fmt.Errorf("pipeline: task %d commit: %w", t.N, err)
	}
	c.checkpoint("commit_created", t.N, func(cp *Checkpoint) {
		cp.HeadSHA = head
		cp.RequestedRole = string(routing.RoleSDDImplementer)
	})
	subject := msg
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = subject[:i]
	}
	c.emitPayload(t.N, 0, PhaseCommitting, "", CommitPayload{SHA: short(head), Subject: subject})
	return head, nil
}

// openGate records a subagent's question and returns ErrHumanGateRequired.
func (c *Controller) openGate(taskN int, question string) error {
	c.pendingQuestion = question
	c.nextTask = taskN
	c.emit(taskN, 0, PhaseBlocked, question)
	c.checkpoint(PhaseBlocked, taskN, func(cp *Checkpoint) {
		cp.GateQuestion = question
	})
	_ = c.Ledger.Note("Task %d: gate opened: %s", taskN, question)
	return ErrHumanGateRequired
}

// openGateWithContext records a subagent's question along with the context
// needed to answer it: the asking task's title and the implementer's full
// report. openGate remains for callers with no report in hand.
func (c *Controller) openGateWithContext(taskN int, question string, report ImplementerReport) error {
	c.gateTaskTitle = ""
	for _, t := range c.Plan.Tasks {
		if t.N == taskN {
			c.gateTaskTitle = t.Title
			break
		}
	}
	c.gateReport = report.Raw
	return c.openGate(taskN, question)
}

// Question returns the unanswered subagent question, if any.
func (c *Controller) Question() string { return c.pendingQuestion }

// QuestionContext returns the asking task's title and the implementer's
// report for the open gate. Both are empty when there is no gate or when
// the gate is branch-level rather than task-level.
func (c *Controller) QuestionContext() (string, string) {
	return c.gateTaskTitle, c.gateReport
}

// Answer supplies the human's answer to the open question. It is appended
// to the task's brief so the re-dispatched implementer reads it as part of
// its requirements, and passed in the prompt.
func (c *Controller) Answer(text string) {
	if c.pendingQuestion == "" {
		return
	}
	if c.nextTask > 0 {
		path := c.Paths.Brief(c.nextTask)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "\n## Answer from the human\n\nQ: %s\nA: %s\n", c.pendingQuestion, text)
			f.Close()
		}
	}
	_ = c.Ledger.Note("Task %d: gate answered: %s", c.nextTask, text)
	c.checkpoint("gate_answered", c.nextTask, func(cp *Checkpoint) {
		cp.GateQuestion = c.pendingQuestion
		cp.GateAnswer = text
	})
	c.pendingAnswer = text
	c.pendingQuestion = ""
}

// takeAnswer consumes any pending human answer.
func (c *Controller) takeAnswer() string {
	a := c.pendingAnswer
	c.pendingAnswer = ""
	return a
}

var producesRe = regexp.MustCompile(`(?m)^-\s*Produces:\s*(.+)$`)

// interfacesBefore collects the "Produces:" lines from every task before n.
// A fresh implementer cannot read earlier tasks, but it must know the names
// and signatures they created.
func (c *Controller) interfacesBefore(n int) string {
	var out []string
	for _, t := range c.Plan.Tasks {
		if t.N >= n {
			continue
		}
		for _, m := range producesRe.FindAllStringSubmatch(t.Body, -1) {
			out = append(out, fmt.Sprintf("- Task %d produced: %s", t.N, strings.TrimSpace(m[1])))
		}
	}
	return strings.Join(out, "\n")
}

// preflightReviewInputs verifies the inputs a reviewer reads are present
// before dispatching it. When any input is missing (e.g. a crash lost the
// report), the run stops instead of sending a reviewer that cannot see its
// materials.
func (c *Controller) preflightReviewInputs(t TaskSpec, pkgPath, verdictPath string) error {
	briefPath := c.Paths.Brief(t.N)
	reportPath := c.Paths.Report(t.N)
	for _, p := range []string{briefPath, reportPath, pkgPath} {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("pipeline: task %d review input %s is inaccessible", t.N, p)
		}
	}
	return nil
}

// reviewTask reviews one task's committed work and loops on fixes until
// the review is clean or the fix budget runs out. Every blocking finding
// from one review goes to a single fix dispatch: per-finding fixers each
// rebuild context and re-run suites, and cost more than the task itself.
func (c *Controller) reviewTask(ctx context.Context, t TaskSpec, res taskResult) (taskResult, error) {
	// Skip per-task review for deterministic tasks under adaptive.
	if c.Strategy == StrategyAdaptive && res.ExecType == ExecDeterministic {
		_ = c.Ledger.Note("Task %d: review skipped (deterministic execution)", t.N)
		return res, nil
	}
	// Skip per-task review entirely under strict.
	if c.Strategy == StrategyStrict {
		_ = c.Ledger.Note("Task %d: review skipped (strict strategy)", t.N)
		return res, nil
	}

	dir := c.workDir()
	for round := 0; ; round++ {
		pkgPath := c.Paths.Package(t.N, round)
		rng := res.Base + ".." + res.Head
		if err := WriteReviewPackage(c.Git, dir, rng, pkgPath); err != nil {
			return res, err
		}
		if err := c.preflightReviewInputs(t, pkgPath, strings.TrimSuffix(pkgPath, ".md")+"-verdict.md"); err != nil {
			return res, err
		}
		prompt, err := RenderReview(ReviewPrompt{
			TaskN:             t.N,
			Title:             t.Title,
			BriefPath:         c.Paths.Brief(t.N),
			BriefBasename:     filepath.Base(c.Paths.Brief(t.N)),
			ReportPath:        c.Paths.Report(t.N),
			ReportBasename:    filepath.Base(c.Paths.Report(t.N)),
			PackagePath:       pkgPath,
			PackageBasename:   filepath.Base(pkgPath),
			ReviewPath:        strings.TrimSuffix(pkgPath, ".md") + "-verdict.md",
			VerdictBasename:   filepath.Base(strings.TrimSuffix(pkgPath, ".md") + "-verdict.md"),
			GlobalConstraints: c.Plan.GlobalConstraints,
		})
		if err != nil {
			return res, err
		}
		c.emit(t.N, round, PhaseReviewing, "")
		label := fmt.Sprintf("task %d review", t.N)
		review, err := c.reviewWithRetry(ctx, t.N, PhaseReviewing, routing.RoleSDDReviewer, label, prompt)
		if err != nil {
			return res, fmt.Errorf("pipeline: task %d review: %w", t.N, err)
		}
		c.checkpoint("review_finished", t.N, func(cp *Checkpoint) {
			cp.BaseSHA = res.Base
			cp.HeadSHA = res.Head
			cp.PackagePath = pkgPath
			cp.RequestedRole = string(routing.RoleSDDReviewer)
		})
		for _, f := range review.Minors() {
			_ = c.Ledger.RecordMinor(t.N, f.Text)
		}
		if !review.InputsAccessible {
			return res, fmt.Errorf("pipeline: task %d review blocked — inputs inaccessible: %s", t.N, review.InputError)
		}
		c.emitPayload(t.N, round, PhaseReviewing, "",
			ReviewPayload{Findings: review.Findings, Clean: review.Clean()})
		if review.Clean() {
			return res, nil
		}
		if round >= c.MaxFixRounds {
			return res, fmt.Errorf("pipeline: task %d review still not clean after %d fix rounds", t.N, c.MaxFixRounds)
		}

		findings := review.Blocking()
		if len(findings) == 0 {
			// A failed verdict with no blocking findings still has to be
			// actionable; hand the reviewer's prose to the fixer.
			findings = []Finding{{Severity: SeverityImportant, Text: review.Raw}}
		}
		c.emit(t.N, round+1, PhaseFixing, "review findings")
		fixPrompt, err := RenderFix(FixPrompt{
			TaskN:          t.N,
			BriefPath:      c.Paths.Brief(t.N),
			BriefBasename:  filepath.Base(c.Paths.Brief(t.N)),
			ReportPath:     c.Paths.Report(t.N),
			ReportBasename: filepath.Base(c.Paths.Report(t.N)),
			WorkDir:        dir,
			Reason:         "the task reviewer requested changes",
			Findings:       findings,
			CoveringTests:  res.Report.Tests,
		})
		if err != nil {
			return res, err
		}
		var report ImplementerReport
		report, err = c.implementWithRetry(ctx, t.N, PhaseFixing, routing.RoleSDDImplementer, fmt.Sprintf("task %d review fix", t.N), fixPrompt)
		if err != nil {
			return res, fmt.Errorf("pipeline: task %d review fixer: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return res, c.openGateWithContext(t.N, report.Question, report)
		}
		res.Report = report

		// Re-gate, then commit the fix. The head advances so the next
		// review package covers the fix as well as the original work.
		c.emit(t.N, round+1, PhaseVerifying, "")
		gate, err := c.Verifier.Run(ctx, dir)
		if err != nil {
			return res, fmt.Errorf("pipeline: task %d re-gate: %w", t.N, err)
		}
		if !gate.OK && !gate.Skipped {
			return res, fmt.Errorf("pipeline: task %d review fix broke `%s`:\n%s", t.N, gate.FailedCommand, gate.Output)
		}
		head, err := c.commitFix(t, round+1, report)
		if err != nil {
			return res, err
		}
		res.Head = head
	}
}

// Run executes the plan from the first task the ledger does not mark
// complete. It returns nil when the branch is ready for the human,
// ErrHumanGateRequired when a subagent needs an answer, or an error when a
// task cannot be completed.
func (c *Controller) Run(ctx context.Context) error {
	if c.Worktree.Path == "" {
		wt, err := worktree.EnsureWorktree(c.Git, c.RepoRoot, c.Paths.WorktreesDir(), "pipeline/"+c.Plan.Slug, c.TargetBranch)
		if err != nil {
			return err
		}
		c.Worktree = wt
		_ = c.Ledger.Note("Run started on branch %s at %s", wt.Branch, wt.Path)
	}
	// Bind the dispatcher to the run's isolation context now that the
	// worktree is known: each dispatch builds a fresh registry rooted at the
	// worktree with @run aliasing the artifact root.
	c.Dispatch.ExecCtx = ExecutionContext{
		RepoRoot:      c.RepoRoot,
		WorkspaceRoot: c.Worktree.Path,
		ArtifactRoot:  c.Paths.Dir,
		ArtifactAlias: "@run",
	}
	// Initialize run store if not already set (recovery sets it).
	if c.RunStore == nil {
		c.RunStore = NewRunStore(c.Paths)
	}
	// Create manifest on first run; skip if it already exists (resume).
	if _, err := c.RunStore.Manifest(); err != nil {
		planHash, _ := planHash(c.Plan.Path)
		c.runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
		c.maxTokens = c.MaxTokensCfg
		if err := c.RunStore.CreateManifest(Manifest{
			SchemaVersion:      1,
			RunID:              c.runID,
			PlanPath:           c.Plan.Path,
			PlanHash:           planHash,
			RepoRoot:           c.RepoRoot,
			TargetBranch:       c.TargetBranch,
			PipelineBranch:     c.Worktree.Branch,
			BaseSHA:            c.Worktree.Base,
			WorktreePath:       c.Worktree.Path,
			CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
			MaxFixRounds:       c.MaxFixRounds,
			MaxDispatchRetries: c.MaxDispatchRetries,
			MaxTotalTokens:     c.maxTokens,
		}); err != nil {
			// Manifest already exists from a concurrent caller — read it.
			m, _ := c.RunStore.Manifest()
			c.runID = m.RunID
			c.maxTokens = m.MaxTotalTokens
		}
	} else {
		m, _ := c.RunStore.Manifest()
		c.runID = m.RunID
		c.maxTokens = m.MaxTotalTokens
	}
	// Acquire the run lock. A second live run on the same paths fails; a
	// stale lock from a different run is taken over after reporting it.
	hostname, _ := os.Hostname()
	newLock := func() RunLock {
		return RunLock{
			RunID:      c.runID,
			PID:        os.Getpid(),
			Host:       hostname,
			AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
	}
	if err := c.RunStore.AcquireLock(newLock()); err != nil {
		if l, ok, _ := c.RunStore.Lock(); ok && l.RunID != c.runID {
			_ = c.Ledger.Note("Taking over stale lock from run %s (pid %d)", l.RunID, l.PID)
			if err := c.RunStore.TakeoverLock(newLock()); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	defer c.RunStore.ReleaseLock()
	c.checkpoint("run_started", 0, func(cp *Checkpoint) {
		cp.BaseSHA = c.Worktree.Base
	})
	done, err := c.Ledger.CompletedTasks()
	if err != nil {
		return err
	}
	// Recovery: replay checkpoints and reconcile with Git.
	if c.RunStore != nil {
		cps, _ := c.RunStore.Replay()
		var last Checkpoint
		for _, cp := range cps {
			if cp.Phase == "task_completed" && cp.TaskN > 0 {
				// Validate the commit still exists on the branch, not just
				// that it equals the current HEAD: a crash after committing
				// more than one task must not re-dispatch earlier ones.
				if cp.HeadSHA != "" {
					exists := false
					// LogOneline(dir, "<sha>^..<sha>") returns the commit's
					// line when it exists and is an error/empty otherwise.
					if out, err := c.Git.LogOneline(c.workDir(), cp.HeadSHA+"^.."+cp.HeadSHA); err == nil && strings.TrimSpace(out) != "" {
						exists = true
					} else if head, err := c.Git.RevParse(c.workDir(), "HEAD"); err == nil && head == cp.HeadSHA {
						// Fallback for fakes without a populated log: the
						// recorded head is still the branch tip.
						exists = true
					}
					if exists {
						done[cp.TaskN] = true
						// Reconcile ledger if missing.
						if !ledgerHasTask(c.Ledger, cp.TaskN) {
							_ = c.Ledger.MarkComplete(cp.TaskN, cp.BaseSHA, cp.HeadSHA)
						}
					}
				}
			}
			c.lastSeq = cp.Seq
			last = cp
		}
		// A gate is restored only when the run actually stopped at it — the
		// last checkpoint is a blocked gate. If the run progressed past the
		// gate (the answer was given and a later checkpoint was written), the
		// gate must not be re-opened.
		if last.Phase == PhaseBlocked && last.GateQuestion != "" {
			c.pendingQuestion = last.GateQuestion
			c.nextTask = last.TaskN
		}
	}
	// A restored human gate must be answered before any task runs.
	if c.pendingQuestion != "" {
		return ErrHumanGateRequired
	}
	// Compile and preflight when strategy is adaptive or strict.
	if c.Strategy == StrategyAdaptive || c.Strategy == StrategyStrict {
		ir, diags, err := CompilePlan(c.Plan)
		if err != nil {
			return fmt.Errorf("pipeline: compile plan: %w", err)
		}
		c.PlanIR = ir

		// Check for error-severity diagnostics.
		var errDiags []Diagnostic
		for _, d := range diags {
			if d.Severity == DiagError {
				errDiags = append(errDiags, d)
			}
		}
		if len(errDiags) > 0 && c.Strategy == StrategyStrict {
			var msgs []string
			for _, d := range errDiags {
				msgs = append(msgs, fmt.Sprintf("task %d: %s", d.TaskN, d.Message))
			}
			return fmt.Errorf("pipeline: strict plan has compile errors:\n%s", strings.Join(msgs, "\n"))
		}

		// Preflight each task.
		for i := range ir.Tasks {
			taskDir := c.workDir()
			opDiags := preflightOps(taskDir, ir.Tasks[i].Operations)
			for _, d := range opDiags {
				_ = c.Ledger.Note("Task %d: preflight: %s", ir.Tasks[i].TaskN, d.Message)
			}
			// Under strict, any blocked operation fails the run.
			if c.Strategy == StrategyStrict && ir.Tasks[i].DerivedStatus() == TaskBlocked {
				return fmt.Errorf("pipeline: task %d is blocked and strategy is strict", ir.Tasks[i].TaskN)
			}
			if c.Strategy == StrategyStrict {
				for _, op := range ir.Tasks[i].Operations {
					if op.OpKind() == "agent" {
						return fmt.Errorf("pipeline: task %d has an agent operation and strategy is strict", ir.Tasks[i].TaskN)
					}
				}
			}
			ir.Tasks[i].Status = ir.Tasks[i].DerivedStatus()
		}
	}
	for _, t := range c.Plan.Tasks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if done[t.N] {
			continue
		}
		if c.overBudget() {
			return fmt.Errorf("pipeline: token budget exhausted (%d/%d) — resumable", c.UsageTokens, c.maxTokens)
		}
		res, err := c.runTask(ctx, t)
		if err != nil {
			if errors.Is(err, ErrHumanGateRequired) && c.AutoEscalate && !c.escalated {
				// One automatic retry on the stronger model with the
				// blocker recorded in the brief, before troubling the human.
				c.escalated = true
				c.Answer("(escalated automatically) " + c.pendingQuestion)
				res, err = c.runTask(ctx, t)
			}
			if err != nil {
				return err
			}
		}
		c.escalated = false
		if c.overBudget() {
			return fmt.Errorf("pipeline: token budget exhausted (%d/%d) — resumable", c.UsageTokens, c.maxTokens)
		}
		res, err = c.reviewTask(ctx, t, res)
		if err != nil {
			return err
		}
		if err := c.Ledger.MarkComplete(t.N, res.Base, res.Head); err != nil {
			return err
		}
		c.checkpoint("task_completed", t.N, func(cp *Checkpoint) {
			cp.BaseSHA = res.Base
			cp.HeadSHA = res.Head
		})
		c.emit(t.N, 0, PhaseDone, "")
	}
	if c.overBudget() {
		return fmt.Errorf("pipeline: token budget exhausted (%d/%d) — resumable", c.UsageTokens, c.maxTokens)
	}
	if err := c.branchReview(ctx); err != nil {
		return err
	}
	c.checkpoint("run_finished", 0, nil)
	return nil
}

// renderBranchReviewPrompt writes the review package and renders the branch
// review prompt. It is a helper shared by the initial review and the re-review.
func (c *Controller) renderBranchReviewPrompt(ctx context.Context, rng string, minors []string) (string, error) {
	dir := c.workDir()
	if err := WriteReviewPackage(c.Git, dir, rng, c.Paths.BranchPackage()); err != nil {
		return "", err
	}
	prompt, err := RenderBranchReview(BranchReviewPrompt{
		PlanPath:        c.Plan.Path,
		PackagePath:     c.Paths.BranchPackage(),
		PackageBasename: filepath.Base(c.Paths.BranchPackage()),
		ReviewPath:      strings.TrimSuffix(c.Paths.BranchPackage(), ".md") + "-verdict.md",
		VerdictBasename: filepath.Base(strings.TrimSuffix(c.Paths.BranchPackage(), ".md") + "-verdict.md"),
		Range:           rng,
		Minors:          minors,
	})
	if err != nil {
		return "", err
	}
	return prompt, nil
}

// branchReview is the merge gate: one review over the whole branch, with
// at most one fix dispatch carrying every blocking finding.
func (c *Controller) branchReview(ctx context.Context) error {
	// Strict strategy promises zero model calls: no branch review.
	if c.Strategy == StrategyStrict {
		_ = c.Ledger.Note("Branch review skipped (strict strategy)")
		return nil
	}
	dir := c.workDir()
	head, err := c.Git.RevParse(dir, "HEAD")
	if err != nil {
		return fmt.Errorf("pipeline: branch review head: %w", err)
	}
	base, err := c.Git.MergeBase(dir, c.TargetBranch, head)
	if err != nil {
		return fmt.Errorf("pipeline: branch review merge-base: %w", err)
	}
	rng := base + ".." + head
	minors, err := c.Ledger.Minors()
	if err != nil {
		return err
	}
	prompt, err := c.renderBranchReviewPrompt(ctx, rng, minors)
	if err != nil {
		return err
	}
	c.emit(0, 0, PhaseBranchReview, "")
	review, err := c.reviewWithRetry(ctx, 0, PhaseBranchReview, routing.RoleSDDBranchReviewer, "branch review", prompt)
	if err != nil {
		return fmt.Errorf("pipeline: branch review: %w", err)
	}
	if review.Clean() {
		_ = c.Ledger.Note("Branch review clean (%s)", rng)
		c.checkpoint("branch_review_finished", 0, nil)
		return nil
	}

	findings := review.Blocking()
	if len(findings) == 0 {
		findings = []Finding{{Severity: SeverityImportant, Text: review.Raw}}
	}
	c.emit(0, 1, PhaseFixing, "branch review findings")
	fixPrompt, err := RenderFix(FixPrompt{
		TaskN:          0,
		BriefPath:      c.Plan.Path,
		BriefBasename:  filepath.Base(c.Plan.Path),
		ReportPath:     c.Paths.BranchPackage(),
		ReportBasename: filepath.Base(c.Paths.BranchPackage()),
		WorkDir:        dir,
		Reason:         "the final branch review requested changes before merge",
		Findings:       findings,
	})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	report, err := c.implementWithRetry(ctx, 0, PhaseFixing, routing.RoleSDDImplementer, "branch review fix", fixPrompt)
	if err != nil {
		return fmt.Errorf("pipeline: branch review fixer: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	gate, err := c.Verifier.Run(ctx, dir)
	if err != nil {
		return fmt.Errorf("pipeline: branch review re-gate: %w", err)
	}
	if !gate.OK && !gate.Skipped {
		return fmt.Errorf("pipeline: branch review fix broke `%s`:\n%s", gate.FailedCommand, gate.Output)
	}
	dirty, err := c.Git.IsDirty(dir)
	if err != nil {
		return fmt.Errorf("pipeline: branch review status: %w", err)
	}
	if dirty {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msg := fmt.Sprintf("%s: branch review fix", c.Plan.Slug)
		if report.Tests != "" {
			msg += "\n\n" + report.Tests
		}
		if _, err := c.Git.CommitAll(dir, msg); err != nil {
			return fmt.Errorf("pipeline: branch review fix commit: %w", err)
		}
	}
	// One re-review, then stop either way: a branch that fails twice is the
	// human's call, not another loop.
	newHead, err := c.Git.RevParse(dir, "HEAD")
	if err != nil {
		return fmt.Errorf("pipeline: branch review head: %w", err)
	}
	rng = base + ".." + newHead
	prompt, err = c.renderBranchReviewPrompt(ctx, rng, minors)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.emit(0, 1, PhaseBranchReview, "")
	review, err = c.reviewWithRetry(ctx, 0, PhaseBranchReview, routing.RoleSDDBranchReviewer, "branch re-review", prompt)
	if err != nil {
		return fmt.Errorf("pipeline: branch re-review: %w", err)
	}
	if review.Clean() {
		_ = c.Ledger.Note("Branch review clean after one fix (%s)", rng)
		c.checkpoint("branch_review_finished", 0, nil)
		return nil
	}
	for _, f := range review.Blocking() {
		_ = c.Ledger.Note("Branch review unresolved: [%s] %s", f.Severity, f.Text)
	}
	return fmt.Errorf("pipeline: branch review still reports %d blocking findings; see %s", len(review.Blocking()), c.Paths.BranchPackage())
}

// Summary is the human-facing wrap-up printed when a run finishes. Merging
// is a human decision; the controller never merges.
func (c *Controller) Summary() string {
	done, _ := c.Ledger.CompletedTasks()
	minors, _ := c.Ledger.Minors()
	var b strings.Builder
	fmt.Fprintf(&b, "Plan %s: %d of %d tasks complete.\n", c.Plan.Slug, len(done), len(c.Plan.Tasks))
	fmt.Fprintf(&b, "Branch: %s\nWorktree: %s\n", c.Worktree.Branch, c.Worktree.Path)
	fmt.Fprintf(&b, "Artifacts: %s\n", c.Paths.Dir)
	if len(minors) > 0 {
		b.WriteString("\nOpen minor findings:\n")
		for _, m := range minors {
			fmt.Fprintf(&b, "  - %s\n", m)
		}
	}
	b.WriteString("\nReview the branch and merge it yourself when you are satisfied.\n")
	return b.String()
}

// CleanupWorktrees removes the run's git worktree after a terminal
// successful run. The worktree is scratch space; leaving it under
// .marshal/pipeline/<slug>/worktrees/ lets stale checkouts of the same
// packages accumulate inside the repo (postmortem 2026-08-06).
//
// Safe by construction: git refuses to remove a dirty worktree, and that
// refusal is returned (never --forced) so user work is preserved — the
// caller logs it and moves on. Idempotent: a no-op once Path is cleared.
// Failed and paused runs keep their worktree so a resume finds the tree
// as it was; cleanup happens when the resumed run later succeeds.
func (c *Controller) CleanupWorktrees() error {
	if c.Worktree.Path == "" {
		return nil
	}
	if err := c.Git.WorktreeRemove(c.RepoRoot, c.Worktree.Path); err != nil {
		return err
	}
	if err := c.Git.WorktreePrune(c.RepoRoot); err != nil {
		return err
	}
	c.Worktree.Path = ""
	return nil
}

// commitFix commits one review-fix round.
func (c *Controller) commitFix(t TaskSpec, round int, report ImplementerReport) (string, error) {
	dir := c.workDir()
	dirty, err := c.Git.IsDirty(dir)
	if err != nil {
		return "", fmt.Errorf("pipeline: task %d status: %w", t.N, err)
	}
	if !dirty {
		return "", fmt.Errorf("pipeline: task %d fix round %d changed nothing", t.N, round)
	}
	c.emit(t.N, round, PhaseCommitting, "")
	msg := fmt.Sprintf("%s: task %d — review fix (round %d)", c.Plan.Slug, t.N, round)
	if report.Tests != "" {
		msg += "\n\n" + report.Tests
	}
	head, err := c.Git.CommitAll(dir, msg)
	if err != nil {
		return "", fmt.Errorf("pipeline: task %d fix commit: %w", t.N, err)
	}
	return head, nil
}
