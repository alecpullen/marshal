package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"marshal/internal/llm/routing"
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
)

// Event is one progress update. The controller emits events; the app layer
// maps them onto session state for the TUI. The pipeline package knows
// nothing about the TUI.
type Event struct {
	TaskN        int
	TotalTasks   int
	FixRound     int
	MaxFixRounds int
	Phase        string
	Detail       string
}

// Observer receives progress events. A nil Observer is valid — the
// controller runs headless.
type Observer interface {
	Event(ev Event)
}

// ErrHumanGateRequired is returned by Run when the controller needs the
// human to answer a subagent's question. The caller renders the question,
// collects the answer, calls Answer, and calls Run again; the controller
// resumes from its saved position.
var ErrHumanGateRequired = errors.New("pipeline: human gate required")

// ControllerOpts are the constructor's inputs.
type ControllerOpts struct {
	PlanPath     string
	RepoRoot     string
	Git          GitOps
	Dispatch     Dispatcher
	Verifier     Verifier
	MaxFixRounds int
	AutoEscalate bool
	Observer     Observer
	TargetBranch string
}

// Controller executes a plan task-by-task. It is single-threaded: exactly
// one subagent runs at a time.
type Controller struct {
	Plan         *Plan
	Paths        Paths
	Ledger       Ledger
	Git          GitOps
	Dispatch     Dispatcher
	Verifier     Verifier
	Worktree     Worktree

	RepoRoot     string
	MaxFixRounds int
	// AutoEscalate mirrors the session's auto approval mode: retry a
	// blocked task once on the reviewer tier before opening a human gate.
	AutoEscalate bool
	Observer     Observer
	TargetBranch string

	// pendingQuestion and pendingAnswer carry a human gate across Run calls.
	pendingQuestion string
	pendingAnswer   string
	// escalated records that the current task already had its automatic
	// retry on the stronger model.
	escalated bool
	// nextTask is the plan task number to resume at.
	nextTask int

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
	if opts.TargetBranch == "" {
		opts.TargetBranch = "main"
	}
	return &Controller{
		Plan:         plan,
		Paths:        paths,
		Ledger:       Ledger{Path: paths.Ledger()},
		Git:          opts.Git,
		Dispatch:     opts.Dispatch,
		Verifier:     opts.Verifier,
		RepoRoot:     opts.RepoRoot,
		MaxFixRounds: opts.MaxFixRounds,
		AutoEscalate: opts.AutoEscalate,
		Observer:     opts.Observer,
		TargetBranch: opts.TargetBranch,
	}, nil
}

// emit sends one progress event, tolerating a nil Observer.
func (c *Controller) emit(taskN, fixRound int, phase, detail string) {
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

// taskResult is one completed task's commit range and final report.
type taskResult struct {
	Base   string
	Head   string
	Report ImplementerReport
}

// runTask implements one task and gets it committed: brief, implementer,
// gate (with fix rounds), commit. Review is Task 10's loop; it runs after
// this returns.
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

	prompt, err := RenderImplementer(ImplementerPrompt{
		TaskN:      t.N,
		Title:      t.Title,
		Placement:  fmt.Sprintf("Task %d of %d in the plan %q.", t.N, len(c.Plan.Tasks), c.Plan.Slug),
		BriefPath:  briefPath,
		ReportPath: c.Paths.Report(t.N),
		WorkDir:    dir,
		Interfaces: c.interfacesBefore(t.N),
		Answer:     c.takeAnswer(),
	})
	if err != nil {
		return taskResult{}, err
	}

	c.emit(t.N, 0, PhaseImplementing, "")
	role := routing.RoleSDDImplementer
	if c.escalated {
		role = routing.RoleSDDReviewer
	}
	report, err := c.Dispatch.Implement(ctx, role, prompt)
	if err != nil {
		return taskResult{}, fmt.Errorf("pipeline: task %d implementer: %w", t.N, err)
	}
	if report.NeedsHuman() {
		return taskResult{Report: report}, c.openGate(t.N, report.Question)
	}
	if report.Status == StatusDoneWithConcerns && report.Concerns != "" {
		_ = c.Ledger.Note("Task %d: implementer concern: %s", t.N, report.Concerns)
	}

	// Gate, with fix rounds. Nothing is committed while the gate fails.
	for round := 0; ; round++ {
		c.emit(t.N, round, PhaseVerifying, "")
		res, err := c.Verifier.Run(ctx, dir)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate: %w", t.N, err)
		}
		if res.Skipped {
			_ = c.Ledger.Note("Task %d: gate skipped (no build or test command configured)", t.N)
			break
		}
		if res.OK {
			break
		}
		if round >= c.MaxFixRounds {
			return taskResult{Report: report}, fmt.Errorf("pipeline: task %d still fails `%s` after %d fix rounds", t.N, res.FailedCommand, c.MaxFixRounds)
		}
		c.emit(t.N, round+1, PhaseFixing, res.FailedCommand)
		fixPrompt, err := RenderFix(FixPrompt{
			TaskN:      t.N,
			BriefPath:  briefPath,
			ReportPath: c.Paths.Report(t.N),
			WorkDir:    dir,
			Reason:     fmt.Sprintf("the build and test gate failed on `%s`", res.FailedCommand),
			Findings: []Finding{{
				Severity: SeverityCritical,
				Text:     fmt.Sprintf("`%s` fails:\n\n%s", res.FailedCommand, res.Output),
			}},
			CoveringTests: report.Tests,
		})
		if err != nil {
			return taskResult{}, err
		}
		report, err = c.Dispatch.Implement(ctx, routing.RoleSDDImplementer, fixPrompt)
		if err != nil {
			return taskResult{}, fmt.Errorf("pipeline: task %d gate fixer: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return taskResult{Report: report}, c.openGate(t.N, report.Question)
		}
	}

	head, err := c.commit(t, report)
	if err != nil {
		return taskResult{}, err
	}
	return taskResult{Base: base, Head: head, Report: report}, nil
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
	return head, nil
}

// openGate records a subagent's question and returns ErrHumanGateRequired.
func (c *Controller) openGate(taskN int, question string) error {
	c.pendingQuestion = question
	c.nextTask = taskN
	c.emit(taskN, 0, PhaseBlocked, question)
	_ = c.Ledger.Note("Task %d: gate opened: %s", taskN, question)
	return ErrHumanGateRequired
}

// Question returns the unanswered subagent question, if any.
func (c *Controller) Question() string { return c.pendingQuestion }

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

// reviewTask reviews one task's committed work and loops on fixes until
// the review is clean or the fix budget runs out. Every blocking finding
// from one review goes to a single fix dispatch: per-finding fixers each
// rebuild context and re-run suites, and cost more than the task itself.
func (c *Controller) reviewTask(ctx context.Context, t TaskSpec, res taskResult) (taskResult, error) {
	dir := c.workDir()
	for round := 0; ; round++ {
		pkgPath := c.Paths.Package(t.N, round)
		rng := res.Base + ".." + res.Head
		if err := WriteReviewPackage(c.Git, dir, rng, pkgPath); err != nil {
			return res, err
		}
		prompt, err := RenderReview(ReviewPrompt{
			TaskN:             t.N,
			Title:             t.Title,
			BriefPath:         c.Paths.Brief(t.N),
			ReportPath:        c.Paths.Report(t.N),
			PackagePath:       pkgPath,
			ReviewPath:        strings.TrimSuffix(pkgPath, ".md") + "-verdict.md",
			GlobalConstraints: c.Plan.GlobalConstraints,
		})
		if err != nil {
			return res, err
		}
		c.emit(t.N, round, PhaseReviewing, "")
		review, err := c.Dispatch.Review(ctx, routing.RoleSDDReviewer, prompt)
		if err != nil {
			return res, fmt.Errorf("pipeline: task %d review: %w", t.N, err)
		}
		for _, f := range review.Minors() {
			_ = c.Ledger.RecordMinor(t.N, f.Text)
		}
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
			TaskN:         t.N,
			BriefPath:     c.Paths.Brief(t.N),
			ReportPath:    c.Paths.Report(t.N),
			WorkDir:       dir,
			Reason:        "the task reviewer requested changes",
			Findings:      findings,
			CoveringTests: res.Report.Tests,
		})
		if err != nil {
			return res, err
		}
		report, err := c.Dispatch.Implement(ctx, routing.RoleSDDImplementer, fixPrompt)
		if err != nil {
			return res, fmt.Errorf("pipeline: task %d review fixer: %w", t.N, err)
		}
		if report.NeedsHuman() {
			return res, c.openGate(t.N, report.Question)
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
