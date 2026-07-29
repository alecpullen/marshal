package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

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
	Git          worktree.GitOps
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
	Plan     *Plan
	Paths    Paths
	Ledger   Ledger
	Git      worktree.GitOps
	Dispatch Dispatcher
	Verifier Verifier
	Worktree worktree.Worktree

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
	done, err := c.Ledger.CompletedTasks()
	if err != nil {
		return err
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
		res, err = c.reviewTask(ctx, t, res)
		if err != nil {
			return err
		}
		if err := c.Ledger.MarkComplete(t.N, res.Base, res.Head); err != nil {
			return err
		}
		c.emit(t.N, 0, PhaseDone, "")
	}
	return c.branchReview(ctx)
}

// renderBranchReviewPrompt writes the review package and renders the branch
// review prompt. It is a helper shared by the initial review and the re-review.
func (c *Controller) renderBranchReviewPrompt(ctx context.Context, rng string, minors []string) (string, error) {
	dir := c.workDir()
	if err := WriteReviewPackage(c.Git, dir, rng, c.Paths.BranchPackage()); err != nil {
		return "", err
	}
	prompt, err := RenderBranchReview(BranchReviewPrompt{
		PlanPath:    c.Plan.Path,
		PackagePath: c.Paths.BranchPackage(),
		ReviewPath:  strings.TrimSuffix(c.Paths.BranchPackage(), ".md") + "-verdict.md",
		Range:       rng,
		Minors:      minors,
	})
	if err != nil {
		return "", err
	}
	return prompt, nil
}

// branchReview is the merge gate: one review over the whole branch, with
// at most one fix dispatch carrying every blocking finding.
func (c *Controller) branchReview(ctx context.Context) error {
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
	review, err := c.Dispatch.Review(ctx, routing.RoleSDDBranchReviewer, prompt)
	if err != nil {
		return fmt.Errorf("pipeline: branch review: %w", err)
	}
	if review.Clean() {
		_ = c.Ledger.Note("Branch review clean (%s)", rng)
		return nil
	}

	findings := review.Blocking()
	if len(findings) == 0 {
		findings = []Finding{{Severity: SeverityImportant, Text: review.Raw}}
	}
	c.emit(0, 1, PhaseFixing, "branch review findings")
	fixPrompt, err := RenderFix(FixPrompt{
		TaskN:      0,
		BriefPath:  c.Plan.Path,
		ReportPath: c.Paths.BranchPackage(),
		WorkDir:    dir,
		Reason:     "the final branch review requested changes before merge",
		Findings:   findings,
	})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	report, err := c.Dispatch.Implement(ctx, routing.RoleSDDImplementer, fixPrompt)
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
	review, err = c.Dispatch.Review(ctx, routing.RoleSDDBranchReviewer, prompt)
	if err != nil {
		return fmt.Errorf("pipeline: branch re-review: %w", err)
	}
	if review.Clean() {
		_ = c.Ledger.Note("Branch review clean after one fix (%s)", rng)
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
