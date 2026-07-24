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
	"marshal/internal/tools/policy"
)

// RunnerFactory builds a role-specific agent.Runner. Implementations must
// return a fresh Runner per call (same contract as swarm.RunnerFactory —
// this is the same type).
type RunnerFactory = swarm.RunnerFactory

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

// SetApprovalMode is a no-op for SDD; approval mode is inherited from the
// parent agent runner factory.
func (o *Orchestrator) SetApprovalMode(policy.ApprovalMode) {}

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
	expectedBranch := o.gitCurrentBranch(workingDir)
	if o.Cfg.AutoWorktree {
		branchName := "sdd/" + planSlug(plan.Title)
		wt, err = CreateWorktree(workingDir, branchName)
		if err == nil {
			workingDir = wt.Path
			expectedBranch = branchName
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
				expectedBranch = o.gitCurrentBranch(workingDir)
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

	mainDir := o.State.WorkingDir

	// Pre-flight plan review: scan for hazards before dispatching Task 1.
	o.preflightPlanReview(plan)

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
	var deviations []string

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

		if err := o.runTask(ctx, ws, ledger, plan, task, i, total, &minorFindings, &deviations, workingDir, mainDir, expectedBranch); err != nil {
			return err
		}
	}

	// Surface accumulated deviations at the final checkpoint before branch review.
	if len(deviations) > 0 {
		o.announce("⚠ Deviations from the brief were detected during the run:")
		for _, d := range deviations {
			o.announce("  - " + d)
		}
		o.announce("Review these deviations before merging. If any are rejected, re-run the affected tasks.")
	}

	// Branch review — the whole-branch merge gate. Skipped (loudly) when
	// the review package can't be written or there is no branch diff:
	// gitMergeBase falls back to HEAD on failure, and base == head would
	// hand the reviewer an empty diff to grade "ready to merge".
	o.State.UpdateSDDBranchReview(session.SDDPhaseActive)
	o.announce("Branch review dispatched.")
	mergeBase := o.gitMergeBase(workingDir)
	headSHA := o.gitHead(workingDir)
	diffPath, err := ws.WriteReviewPackage(mergeBase, headSHA)
	switch {
	case err != nil:
		o.State.UpdateSDDBranchReview(session.SDDPhaseFailed)
		o.announce("Branch review skipped — could not write review package: " + err.Error())
	case mergeBase == "" || headSHA == "" || mergeBase == headSHA:
		o.State.UpdateSDDBranchReview(session.SDDPhaseSkipped)
		o.announce("Branch review skipped — no branch diff (merge-base equals HEAD).")
	default:
		o.runBranchReview(ctx, planPath, plan, ws, workingDir, mainDir, mergeBase, headSHA, diffPath, minorFindings, deviations)
	}

	o.announce(fmt.Sprintf("▸ SDD complete. %d/%d tasks done. Use /merge or /pr to finish.", total, total))
	return nil
}

// runBranchReview dispatches the whole-branch merge-gate review and, on
// findings, one fix wave plus a single re-review.
func (o *Orchestrator) runBranchReview(ctx context.Context, planPath string, plan *Plan, ws *Workspace, workingDir, mainDir, mergeBase, headSHA, diffPath string, minorFindings, deviations []string) {
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
	fixPrompt := BuildBranchFixPrompt(branchTask.Summary, workingDir)
	if _, err := o.runRole(ctx, agent.RoleSDDImplementer, swarm.ScopeFull, fixPrompt); err != nil {
		o.announce("Branch fix wave failed: " + err.Error())
	}

	// Re-review once.
	headSHA = o.gitHead(workingDir)
	mergeBase = o.gitMergeBase(workingDir)
	diffPath, err = ws.WriteReviewPackage(mergeBase, headSHA)
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
// differ from o.State.WorkingDir when AutoWorktree is enabled). mainDir is
// the original working directory, used to check that commits do not leak to
// main. expectedBranch is the branch the implementer should commit to.
func (o *Orchestrator) runTask(ctx context.Context, ws *Workspace, ledger *Ledger, plan *Plan, task PlanTask, index, total int, minorFindings, deviations *[]string, workingDir, mainDir, expectedBranch string) error {
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
	beforeBranch := o.gitCurrentBranch(workingDir)
	implPrompt := BuildImplementerPrompt(task, briefPath, reportPath, workingDir, "See the task brief for full details.")
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

	reportedCommit := parseImplementerCommit(implTask.Summary)
	if err := o.verifyBranchState(workingDir, mainDir, expectedBranch, beforeBranch, reportedCommit); err != nil {
		o.State.UpdateSDDTask(index, func(ts *session.SDDTaskStatus) {
			ts.Phase = session.SDDPhaseFailed
			ts.Detail = "wrong-branch incident"
		})
		o.announce(fmt.Sprintf("Task %d/%d: wrong-branch incident — %v", task.Number, total, err))
		o.announce("Recover manually with git update-ref / git reset; do not dispatch a fix subagent for this.")
		return fmt.Errorf("task %d wrong-branch incident: %w", task.Number, err)
	}

	headSHA := o.gitHead(workingDir)
	o.announce(fmt.Sprintf("Task %d/%d: implementer DONE (commits %s..%s, branch %s)", task.Number, total, shortSHA(baseSHA), shortSHA(headSHA), expectedBranch))

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
		// Track deviations separately.
		for _, d := range verdict.Deviations {
			*deviations = append(*deviations, fmt.Sprintf("task-%d: %s", task.Number, d))
		}
		if !verdict.HasBlockingFindings() {
			o.announce(fmt.Sprintf("Task %d/%d: review clean — spec ✅, quality approved", task.Number, total))
			// Post-review state sweep before marking the task complete.
			o.postReviewStateSweep(workingDir, mainDir)
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
		fixPrompt := BuildFixPrompt(task, reportPath, workingDir, reviewTask.Summary)
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

// verifyBranchState checks that the worktree is still on the expected branch
// and that the reported commit did not leak to main. beforeBranch is the
// branch recorded before dispatching the implementer; it must match the
// expected branch. reportedCommit may be empty if the implementer did not
// report one, in which case the SHA check is skipped.
func (o *Orchestrator) verifyBranchState(workingDir, mainDir, expectedBranch, beforeBranch, reportedCommit string) error {
	if beforeBranch != expectedBranch {
		return fmt.Errorf("branch before dispatch was %q, expected %q", beforeBranch, expectedBranch)
	}
	currentBranch := o.gitCurrentBranch(workingDir)
	if currentBranch != expectedBranch {
		return fmt.Errorf("worktree is on branch %q, expected %q", currentBranch, expectedBranch)
	}
	if reportedCommit != "" {
		headSHA := o.gitHead(workingDir)
		// Allow either the reported commit or HEAD to be a prefix of the other
		// so short-SHA reports still match.
		if !strings.HasPrefix(headSHA, reportedCommit) && !strings.HasPrefix(reportedCommit, headSHA) {
			return fmt.Errorf("implementer reported commit %q but worktree HEAD is %q", reportedCommit, headSHA)
		}
		mainLog := o.gitLogOneline(mainDir, "main", 10)
		short := reportedCommit
		if len(short) > 7 {
			short = short[:7]
		}
		if strings.Contains(mainLog, short) {
			return fmt.Errorf("reported commit %q appears in main log", reportedCommit)
		}
	}
	return nil
}

// postReviewStateSweep confirms the worktree and main branches are in the
// expected state before the next task dispatch.
func (o *Orchestrator) postReviewStateSweep(workingDir, mainDir string) {
	o.announce("State sweep:")
	if status := o.gitStatus(workingDir); status != "" {
		o.announce("  worktree uncommitted changes:\n" + status)
	} else {
		o.announce("  worktree clean")
	}
	if wtLog := o.gitLogOneline(workingDir, "HEAD", 3); wtLog != "" {
		o.announce("  worktree log: " + wtLog)
	}
	if mainLog := o.gitLogOneline(mainDir, "main", 3); mainLog != "" {
		o.announce("  main log: " + mainLog)
	}
}

// preflightPlanReview scans the plan for common hazards and announces them
// before Task 1 is dispatched. It does not block execution; the human decides
// whether to proceed.
func (o *Orchestrator) preflightPlanReview(plan *Plan) {
	var text strings.Builder
	text.WriteString(plan.Title)
	text.WriteString("\n")
	text.WriteString(plan.GlobalConstraints)
	for _, t := range plan.Tasks {
		text.WriteString("\n")
		text.WriteString(t.Body)
	}
	lower := strings.ToLower(text.String())

	var findings []string
	if strings.Contains(lower, "git reset --hard") {
		findings = append(findings, "plan contains `git reset --hard` — destructive resets are forbidden")
	}
	if strings.Contains(lower, "git checkout") && !strings.Contains(lower, "git checkout -b") {
		findings = append(findings, "plan contains `git checkout` without `-b` — may be destructive or ambiguous")
	}
	if strings.Contains(lower, "npx ") && !strings.Contains(lower, "npx --yes") && !strings.Contains(lower, "npx -y") {
		findings = append(findings, "plan contains `npx` invocation without `--yes` or `-y` — may be interactive")
	}
	if strings.Contains(lower, "package.json") && !strings.Contains(lower, "package-lock.json") {
		findings = append(findings, "plan mentions package.json but not package-lock.json — ensure dependency lockfile changes are staged")
	}

	if len(findings) == 0 {
		return
	}
	o.announce("⚠ Pre-flight plan review findings:")
	for _, f := range findings {
		o.announce("  - " + f)
	}
	o.announce("Review these findings before proceeding. The SDD run will continue, but some steps may need explicit approval.")
}

// gitHead returns the current HEAD SHA of the given working directory.
func (o *Orchestrator) gitHead(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitCurrentBranch returns the current branch name of the working directory.
func (o *Orchestrator) gitCurrentBranch(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
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

// gitLogOneline returns the recent oneline log of the given ref.
func (o *Orchestrator) gitLogOneline(workingDir, ref string, n int) string {
	out, err := exec.Command("git", "-C", workingDir, "log", "--oneline", "-n", fmt.Sprintf("%d", n), ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitStatus returns the short status of the working directory.
func (o *Orchestrator) gitStatus(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "status", "--short").Output()
	if err != nil {
		return ""
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

// parseImplementerCommit extracts the first commit-like hex token from the
// implementer's summary. It looks for hex strings of at least 7 characters
// on lines that mention commits. Returns an empty string if none is found.
func parseImplementerCommit(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "commit") {
			continue
		}
		for _, f := range strings.Fields(line) {
			clean := strings.TrimFunc(f, func(r rune) bool {
				return r == '(' || r == ')' || r == ':' || r == ',' || r == '.' || r == ';'
			})
			if isHex(clean) && len(clean) >= 7 {
				return clean
			}
		}
	}
	return ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
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
