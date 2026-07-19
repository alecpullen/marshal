package sdd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// RunnerFactory builds a role-specific agent.Runner. Implementations must
// return a fresh Runner per call (same contract as swarm.RunnerFactory).
type RunnerFactory func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error)

// Orchestrator drives the SDD pipeline: parse plan -> per task
// (implementer -> reviewer -> fix loop) -> branch review. It satisfies
// the TUI's AgentRunner interface.
type Orchestrator struct {
	State     *session.State
	NewRunner RunnerFactory
	Cfg       config.SDDConfig

	MaxFixRounds int
}

// New creates an Orchestrator with the given state, factory, and config.
// MaxFixRounds defaults to cfg.MaxFixRounds, or 3 if less than 1.
func New(state *session.State, factory RunnerFactory, cfg config.SDDConfig) *Orchestrator {
	maxRounds := cfg.MaxFixRounds
	if maxRounds < 1 {
		maxRounds = 3
	}
	return &Orchestrator{
		State:        state,
		NewRunner:    factory,
		Cfg:          cfg,
		MaxFixRounds: maxRounds,
	}
}

// SetForceClass is a no-op for SDD; roles fix their own task classes.
func (o *Orchestrator) SetForceClass(string) {}

// SetPolicyRules is a no-op for SDD; policy is inherited from the parent
// agent runner factory.
func (o *Orchestrator) SetPolicyRules([]config.PermissionRule) {}

// Run executes the plan at planPath. Satisfies tui.AgentRunner.
func (o *Orchestrator) Run(ctx context.Context, planPath string) error {
	plan, err := ParsePlan(planPath)
	if err != nil {
		o.announce(fmt.Sprintf("SDD failed to parse plan: %v", err))
		return err
	}

	workingDir := o.State.WorkingDir
	ws, err := NewWorkspace(workingDir)
	if err != nil {
		return err
	}
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	ledger := NewLedger(ws)
	completed := ledger.CompletedTasks()

	// Optionally create a worktree for isolation.
	var wt *Worktree
	if o.Cfg.AutoWorktree {
		branchName := "sdd/" + planSlug(plan.Title)
		wt, err = CreateWorktree(workingDir, branchName)
		if err == nil {
			workingDir = wt.Path
			ws, _ = NewWorkspace(workingDir)
			if _, werr := ws.Ensure(); werr != nil {
				// Surface the failure instead of silently running on a
				// half-built workspace; fall back to the original directory.
				_ = wt.Remove()
				wt = nil
				workingDir = o.State.WorkingDir
				o.announce("SDD worktree setup failed (" + werr.Error() + ") — continuing in the original directory")
				ws, _ = NewWorkspace(workingDir)
				if _, err := ws.Ensure(); err != nil {
					return err
				}
			}
			ledger = NewLedger(ws)
			completed = ledger.CompletedTasks()
		}
		// If worktree creation fails, fall back to the original directory.
	}
	// Clean up the worktree when done, regardless of success or failure.
	defer func() {
		if wt != nil {
			_ = wt.Remove()
		}
	}()

	tasks := plan.Tasks
	total := len(tasks)
	o.State.SetSDDProgress(session.SDDProgress{
		Active:       true,
		PlanName:     plan.Title,
		PlanPath:     planPath,
		TotalTasks:   total,
		Tasks:        buildInitialTaskStatuses(tasks, completed, o.MaxFixRounds),
		BranchReview: session.SDDPhasePending,
	})
	defer o.State.ClearSDDProgress()

	o.announce(fmt.Sprintf("▸ SDD started: %s (%d tasks)", plan.Title, total))

	var minorFindings []string

	for i, task := range tasks {
		if ctx.Err() != nil {
			o.announce(fmt.Sprintf("SDD cancelled at task %d/%d — /sdd <plan> to resume.", task.Number, total))
			return ctx.Err()
		}
		if completed[task.Number] {
			o.announce(fmt.Sprintf("Task %d/%d: skipped (ledger marks complete)", task.Number, total))
			o.State.UpdateSDDTask(i, func(ts *session.SDDTaskStatus) {
				ts.Phase = session.SDDPhaseSkipped
				ts.Implementer = session.SDDPhaseSkipped
				ts.Reviewer = session.SDDPhaseSkipped
			})
			continue
		}

		if err := o.runTask(ctx, ws, ledger, plan, task, i, total, &minorFindings, workingDir); err != nil {
			return err
		}
	}

	// Branch review — the whole-branch merge gate. Skipped (loudly) when
	// the review package can't be written or there is no branch diff:
	// gitMergeBase falls back to HEAD on failure, and base == head would
	// hand the reviewer an empty diff to grade "ready to merge".
	o.State.UpdateSDDBranchReview(session.SDDPhaseActive)
	o.announce("Branch review dispatched.")
	mergeBase := o.gitMergeBase(workingDir)
	headSHA := o.gitHead(workingDir)
	diffPath, err := ws.WriteBranchReviewPackage(mergeBase, headSHA)
	switch {
	case err != nil:
		o.State.UpdateSDDBranchReview(session.SDDPhaseFailed)
		o.announce("Branch review skipped — could not write review package: " + err.Error())
	case mergeBase == "" || headSHA == "" || mergeBase == headSHA:
		o.State.UpdateSDDBranchReview(session.SDDPhaseSkipped)
		o.announce("Branch review skipped — no branch diff (merge-base equals HEAD).")
	default:
		o.runBranchReview(ctx, planPath, plan, ws, workingDir, mergeBase, headSHA, diffPath, minorFindings)
	}

	o.announce(fmt.Sprintf("▸ SDD complete. %d/%d tasks done. Use /merge or /pr to finish.", total, total))
	return nil
}

// runBranchReview dispatches the whole-branch merge-gate review and, on
// findings, one fix wave plus a single re-review.
func (o *Orchestrator) runBranchReview(ctx context.Context, planPath string, plan *Plan, ws *Workspace, workingDir, mergeBase, headSHA, diffPath string, minorFindings []string) {
	branchPrompt := BuildBranchReviewerPrompt(
		planPath, ws.ReportsDir(), diffPath,
		plan.GlobalConstraints, mergeBase, headSHA, minorFindings,
	)
	branchTask, err := o.runRole(ctx, agent.RoleSDDBranchReviewer, swarm.ScopeReadOnly, branchPrompt)
	if err != nil {
		o.State.UpdateSDDBranchReview(session.SDDPhaseFailed)
		o.announce("Branch review failed: " + err.Error())
		return
	}
	verdict := ParseBranchVerdict(branchTask.Summary)
	o.State.UpdateSDDBranchReview(session.SDDPhaseDone)
	if verdict.Ready {
		o.announce("Branch review: ✅ ready to merge")
		return
	}
	o.announce("⚠ Branch review: findings — fix wave dispatched, re-reviewing")
	fixPrompt := BuildBranchFixPrompt(branchTask.Summary)
	if _, err := o.runRole(ctx, agent.RoleSDDImplementer, swarm.ScopeFull, fixPrompt); err != nil {
		o.announce("Branch fix wave failed: " + err.Error())
	}

	// Re-review once.
	headSHA = o.gitHead(workingDir)
	mergeBase = o.gitMergeBase(workingDir)
	diffPath, err = ws.WriteBranchReviewPackage(mergeBase, headSHA)
	if err != nil {
		o.announce("Branch re-review skipped — could not write review package: " + err.Error())
		return
	}
	reReviewPrompt := BuildBranchReviewerPrompt(
		planPath, ws.ReportsDir(), diffPath,
		plan.GlobalConstraints, mergeBase, headSHA, minorFindings,
	)
	reTask, err := o.runRole(ctx, agent.RoleSDDBranchReviewer, swarm.ScopeReadOnly, reReviewPrompt)
	if err != nil {
		o.announce("Branch re-review failed: " + err.Error())
		return
	}
	reVerdict := ParseBranchVerdict(reTask.Summary)
	if reVerdict.Ready {
		o.announce("Branch re-review: ✅ ready to merge")
	} else {
		o.announce("Branch re-review: ❌ still not ready — manual intervention needed")
	}
}

// runTask executes one task: implementer -> review loop (up to MaxFixRounds
// times) -> ledger append. workingDir is the git working directory (may
// differ from o.State.WorkingDir when AutoWorktree is enabled).
func (o *Orchestrator) runTask(ctx context.Context, ws *Workspace, ledger *Ledger, plan *Plan, task PlanTask, index, total int, minorFindings *[]string, workingDir string) error {
	briefPath, err := ws.WriteTaskBrief(task.Number, task.Body)
	if err != nil {
		return fmt.Errorf("task %d brief: %w", task.Number, err)
	}
	reportPath := ws.ReportPath(task.Number)

	o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
		ts.Phase = session.SDDPhaseActive
		ts.Implementer = session.SDDPhaseActive
		ts.Detail = "implementer dispatched"
	})
	o.announce(fmt.Sprintf("Task %d/%d: %s — implementer dispatched", task.Number, total, task.Title))

	baseSHA := o.gitHead(workingDir)
	implPrompt := BuildImplementerPrompt(task, briefPath, reportPath, "See the task brief for full details.")
	implTask, err := o.runRole(ctx, agent.RoleSDDImplementer, swarm.ScopeFull, implPrompt)
	if err != nil {
		o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
			ts.Phase = session.SDDPhaseFailed
			ts.Implementer = session.SDDPhaseFailed
		})
		return fmt.Errorf("task %d implementer failed: %w", task.Number, err)
	}
	status := parseImplementerStatus(implTask.Summary)
	if status == "BLOCKED" || status == "NEEDS_CONTEXT" {
		o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
			ts.Phase = session.SDDPhaseFailed
			ts.Detail = status
		})
		o.announce(fmt.Sprintf("Task %d/%d: implementer %s — escalating", task.Number, total, status))
		return fmt.Errorf("task %d implementer %s", task.Number, status)
	}

	headSHA := o.gitHead(workingDir)
	o.announce(fmt.Sprintf("Task %d/%d: implementer DONE (commits %s..%s)", task.Number, total, shortSHA(baseSHA), shortSHA(headSHA)))

	o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
		ts.Implementer = session.SDDPhaseDone
		ts.Reviewer = session.SDDPhaseActive
		ts.Detail = "reviewer dispatched"
	})

	// Review loop with bounded fix rounds.
	for round := 0; round < o.MaxFixRounds; round++ {
		diffPath, err := ws.WriteReviewPackage(baseSHA, headSHA)
		if err != nil {
			o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
				ts.Reviewer = session.SDDPhaseFailed
				ts.Detail = "review package failed"
			})
			return fmt.Errorf("task %d review package: %w", task.Number, err)
		}
		reviewPrompt := BuildTaskReviewerPrompt(
			briefPath, reportPath, diffPath,
			plan.GlobalConstraints, baseSHA, headSHA,
		)
		reviewTask, err := o.runRole(ctx, agent.RoleSDDReviewer, swarm.ScopeReadOnly, reviewPrompt)
		if err != nil {
			o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
				ts.Reviewer = session.SDDPhaseFailed
				ts.Detail = "reviewer failed"
			})
			return fmt.Errorf("task %d reviewer failed: %w", task.Number, err)
		}
		verdict := ParseTaskVerdict(reviewTask.Summary)
		// Accumulate minor findings for the branch review.
		for _, f := range verdict.Findings {
			tag := fmt.Sprintf("task-%d: %s: %s", task.Number, f.Severity, f.Text)
			*minorFindings = append(*minorFindings, tag)
		}
		if !verdict.HasBlockingFindings() {
			o.announce(fmt.Sprintf("Task %d/%d: review clean — spec ✅, quality approved", task.Number, total))
			o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
				ts.Reviewer = session.SDDPhaseDone
				ts.Phase = session.SDDPhaseDone
				ts.Detail = ""
			})
			_ = ledger.Append(LedgerEntry{
				TaskNumber: task.Number,
				BaseSHA:    baseSHA,
				HeadSHA:    headSHA,
			})
			o.announce(fmt.Sprintf("✔ Task %d complete", task.Number))
			return nil
		}
		o.announce(fmt.Sprintf("Task %d/%d: review found issues — fix round %d/%d", task.Number, total, round+1, o.MaxFixRounds))
		o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
			ts.Reviewer = session.SDDPhaseActive
			ts.FixRound = round + 1
			ts.Detail = fmt.Sprintf("fix %d/%d", round+1, o.MaxFixRounds)
		})
		fixPrompt := BuildFixPrompt(reportPath, reviewTask.Summary, task)
		_, err = o.runRole(ctx, agent.RoleSDDImplementer, swarm.ScopeFull, fixPrompt)
		if err != nil {
			return fmt.Errorf("task %d fix failed: %w", task.Number, err)
		}
		headSHA = o.gitHead(workingDir)
	}
	o.announce(fmt.Sprintf("⚠ Task %d/%d: fix budget exhausted — escalating", task.Number, total))
	o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
		ts.Phase = session.SDDPhaseFailed
		ts.Detail = "fix budget exhausted"
	})
	return fmt.Errorf("task %d fix budget exhausted", task.Number)
}

// runRole builds a fresh Runner for the given role and dispatches a prompt.
func (o *Orchestrator) runRole(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, scope)
	if err != nil {
		return nil, err
	}
	return runner.RunTask(ctx, prompt)
}

// announce adds a system message to the session transcript.
func (o *Orchestrator) announce(text string) {
	o.State.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
}

// gitHead returns the current HEAD SHA of the given working directory.
func (o *Orchestrator) gitHead(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitMergeBase returns the merge-base of main and HEAD. Falls back to HEAD.
func (o *Orchestrator) gitMergeBase(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "merge-base", "main", "HEAD").Output()
	if err != nil {
		return o.gitHead(workingDir)
	}
	return strings.TrimSpace(string(out))
}

// buildInitialTaskStatuses creates the initial SDDTaskStatus slice for the
// progress panel. Tasks already marked complete in the ledger get the
// Skipped phase; the rest are Pending.
func buildInitialTaskStatuses(tasks []PlanTask, completed map[int]bool, maxFixes int) []session.SDDTaskStatus {
	statuses := make([]session.SDDTaskStatus, len(tasks))
	for i, task := range tasks {
		phase := session.SDDPhasePending
		if completed[task.Number] {
			phase = session.SDDPhaseSkipped
		}
		statuses[i] = session.SDDTaskStatus{
			Name:        task.Title,
			Phase:       phase,
			Implementer: phase,
			Reviewer:    phase,
			MaxFixes:    maxFixes,
		}
	}
	return statuses
}

// parseImplementerStatus extracts the top-level status keyword from an
// implementer's summary text. It prefers the "Status:" line the dispatch
// prompt mandates (prompts.go); without one it falls back to a substring
// scan ordered so failure words beat DONE (a BLOCKED summary saying "not
// done" must not read as DONE). Defaults to DONE.
func parseImplementerStatus(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		rest, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(line)), "STATUS:")
		if !ok {
			continue
		}
		status := strings.TrimSpace(rest)
		for _, known := range []string{"DONE_WITH_CONCERNS", "BLOCKED", "NEEDS_CONTEXT", "DONE"} {
			if strings.HasPrefix(status, known) {
				return known
			}
		}
	}
	upper := strings.ToUpper(summary)
	for _, status := range []string{"BLOCKED", "NEEDS_CONTEXT", "DONE_WITH_CONCERNS", "DONE"} {
		if strings.Contains(upper, status) {
			return status
		}
	}
	return "DONE"
}

// planSlug converts a plan title into a filesystem-safe slug.
func planSlug(title string) string {
	s := strings.ToLower(title)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, s)
	return s
}
