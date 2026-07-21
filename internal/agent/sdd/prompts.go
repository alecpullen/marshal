package sdd

import (
	"fmt"
	"strings"
)

// BuildImplementerPrompt constructs the dispatch prompt for an SDD
// implementer subagent.
func BuildImplementerPrompt(task PlanTask, briefPath, reportPath, worktreeDir, context string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are implementing Task %d: %s\n\n", task.Number, task.Title)
	fmt.Fprintf(&b, "## Task Description\n\n")
	fmt.Fprintf(&b, "Read your task brief first: %s\n", briefPath)
	b.WriteString("It contains the full task text from the plan.\n\n")
	fmt.Fprintf(&b, "## Context\n\n%s\n\n", context)

	b.WriteString("## Before You Begin\n\n")
	b.WriteString("If you have questions about the requirements, the approach, dependencies, assumptions, or anything unclear in the task description, **ask them now.** Raise any concerns before starting work.\n\n")

	b.WriteString("## Your Job\n\n")
	b.WriteString("Once you're clear on requirements:\n")
	b.WriteString("1. Implement exactly what the task specifies\n")
	b.WriteString("2. Write tests (following TDD if the task says to)\n")
	b.WriteString("3. Verify the implementation works\n")
	b.WriteString("4. Commit your work\n")
	b.WriteString("5. Self-review your work\n")
	b.WriteString("6. Report back\n\n")

	b.WriteString("## Worktree Discipline (CRITICAL)\n\n")
	fmt.Fprintf(&b, "Work from: %s\n\n", worktreeDir)
	b.WriteString("All `git` commands in this task MUST run from that directory. Use absolute paths: ")
	fmt.Fprintf(&b, "`git -C %s <command>` or `cd %s && git <command>`. ", worktreeDir, worktreeDir)
	b.WriteString("Do not run `git` from your current working directory unless you have first verified with `pwd` that you are in the worktree path.\n\n")
	b.WriteString("Before every `git commit`, run `git rev-parse --abbrev-ref HEAD` and confirm it matches the expected branch name. ")
	b.WriteString("If it doesn't, STOP and report BLOCKED — do not commit.\n\n")
	b.WriteString("Do not use `git reset --hard` for any reason. If you need to undo, use `git reset --soft HEAD~1` (keeps changes staged) ")
	b.WriteString("or `git restore --staged <file>` (unstages only). Report the situation in your report and ask if you are unsure.\n\n")
	b.WriteString("**While you work:** If you encounter something unexpected or unclear, **ask questions**. ")
	b.WriteString("It's always OK to pause and clarify. Don't guess or make assumptions.\n\n")
	b.WriteString("While iterating, run the focused test for what you're changing; run the full suite once before committing, not after every edit.\n\n")

	b.WriteString("## Code Organization\n\n")
	b.WriteString("- Follow the file structure defined in the plan.\n")
	b.WriteString("- Each file should have one clear responsibility with a well-defined interface.\n")
	b.WriteString("- If a file you're creating is growing beyond the plan's intent, stop and report it as DONE_WITH_CONCERNS — don't split files on your own without plan guidance.\n")
	b.WriteString("- If an existing file you're modifying is already large or tangled, work carefully and note it as a concern in your report.\n")
	b.WriteString("- In existing codebases, follow established patterns. Improve code you're touching the way a good developer would, but don't restructure things outside your task.\n\n")

	b.WriteString("## When You're in Over Your Head\n\n")
	b.WriteString("It is always OK to stop and say \"this is too hard for me.\" Bad work is worse than no work. You will not be penalized for escalating.\n\n")
	b.WriteString("**STOP and escalate when:**\n")
	b.WriteString("- The task requires architectural decisions with multiple valid approaches\n")
	b.WriteString("- You need to understand code beyond what was provided and can't find clarity\n")
	b.WriteString("- You feel uncertain about whether your approach is correct\n")
	b.WriteString("- The task involves restructuring existing code in ways the plan didn't anticipate\n")
	b.WriteString("- You've been reading file after file trying to understand the system without progress\n\n")
	b.WriteString("**How to escalate:** Report back with status BLOCKED or NEEDS_CONTEXT. Describe specifically what you're stuck on, what you've tried, and what kind of help you need. The controller can provide more context, re-dispatch with a more capable model, or break the task into smaller pieces.\n\n")

	b.WriteString("## Self-review Limits\n\n")
	b.WriteString("Cosmetic self-reviews (rename, refactor a small thing) are fine. If you are considering a structural self-review (re-do the work, change the approach), report it as BLOCKED or NEEDS_CONTEXT so the controller can confirm before you proceed. Do not silently re-implement or discard work.\n\n")

	b.WriteString("## Before Reporting Back: Self-Review\n\n")
	b.WriteString("Review your work with fresh eyes.\n\n")
	b.WriteString("**Completeness:**\n")
	b.WriteString("- Did I fully implement everything in the spec?\n")
	b.WriteString("- Did I miss any requirements?\n")
	b.WriteString("- Are there edge cases I didn't handle?\n\n")
	b.WriteString("**Quality:**\n")
	b.WriteString("- Is this my best work?\n")
	b.WriteString("- Are names clear and accurate (match what things do, not how they work)?\n")
	b.WriteString("- Is the code clean and maintainable?\n\n")
	b.WriteString("**Discipline:**\n")
	b.WriteString("- Did I avoid overbuilding (YAGNI)?\n")
	b.WriteString("- Did I only build what was requested?\n")
	b.WriteString("- Did I follow existing patterns in the codebase?\n\n")
	b.WriteString("**Testing:**\n")
	b.WriteString("- Do tests actually verify behavior (not just mock behavior)?\n")
	b.WriteString("- Did I follow TDD if required?\n")
	b.WriteString("- Are tests comprehensive?\n")
	b.WriteString("- Is the test output pristine (no stray warnings or noise)?\n\n")
	b.WriteString("If you find issues during self-review, fix them now before reporting.\n\n")

	b.WriteString("## After Review Findings\n\n")
	b.WriteString("If a reviewer finds issues and you fix them, re-run the tests that cover the amended code and append the results to your report file. Reviewers will not re-run tests for you — your report is the test evidence.\n\n")

	fmt.Fprintf(&b, "## Report Format\n\n")
	fmt.Fprintf(&b, "Write your full report to %s:\n", reportPath)
	b.WriteString("- What you implemented (or what you attempted, if blocked)\n")
	b.WriteString("- What you tested and test results\n")
	b.WriteString("- **TDD Evidence** (if TDD was required for this task):\n")
	b.WriteString("  - RED: command run, relevant failing output before implementation, and why the failure was expected\n")
	b.WriteString("  - GREEN: command run and relevant passing output after implementation\n")
	b.WriteString("- Files changed\n")
	b.WriteString("- Self-review findings (if any)\n")
	b.WriteString("- Any issues or concerns\n\n")
	b.WriteString("Then report back with ONLY (under 15 lines — the detail lives in the report file):\n")
	b.WriteString("- **Status:** DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT\n")
	b.WriteString("- Commits created (short SHA + subject + branch name, with `git rev-parse --abbrev-ref HEAD` output)\n")
	b.WriteString("- One-line test summary (e.g. \"14/14 passing, output pristine\")\n")
	b.WriteString("- Your concerns, if any\n")
	b.WriteString("- The report file path\n\n")
	b.WriteString("If BLOCKED or NEEDS_CONTEXT, put the specifics in the final message itself — the controller acts on it directly.\n\n")
	b.WriteString("Use DONE_WITH_CONCERNS if you completed the work but have doubts about correctness. ")
	b.WriteString("Use BLOCKED if you cannot complete the task. Use NEEDS_CONTEXT if you need information that wasn't provided. ")
	b.WriteString("Never silently produce work you're unsure about.")
	return b.String()
}

// BuildTaskReviewerPrompt constructs the dispatch prompt for an SDD task
// reviewer subagent.
func BuildTaskReviewerPrompt(briefPath, reportPath, diffPath, globalConstraints, baseSHA, headSHA string) string {
	var b strings.Builder
	b.WriteString("You are reviewing one task's implementation: first whether it matches its requirements, then whether it is well-built. ")
	b.WriteString("This is a task-scoped gate, not a merge review — a broad whole-branch review happens separately after all tasks are complete.\n\n")

	fmt.Fprintf(&b, "## What Was Requested\n\nRead the task brief: %s\n\n", briefPath)
	fmt.Fprintf(&b, "Global constraints from the spec/design that bind this task:\n%s\n\n", globalConstraints)

	fmt.Fprintf(&b, "## What the Implementer Claims They Built\n\nRead the implementer's report: %s\n\n", reportPath)

	b.WriteString("## Diff Under Review\n\n")
	fmt.Fprintf(&b, "**Base:** %s\n**Head:** %s\n**Diff file:** %s\n\n", baseSHA, headSHA, diffPath)
	b.WriteString("Read the diff file once — it contains the commit list, a stat summary, and the full diff with surrounding context, and it is your view of the change. ")
	b.WriteString("The diff's context lines ARE the changed files: do not Read a changed file separately unless a hunk you must judge is cut off mid-function — and say so in your report. ")
	b.WriteString("Do not re-run git commands. If the diff file is missing, fetch the diff yourself: ")
	fmt.Fprintf(&b, "`git diff --stat %s..%s` and `git diff %s..%s`.\n", baseSHA, headSHA, baseSHA, headSHA)
	b.WriteString("Do not crawl the broader codebase. Inspect code outside the diff only to evaluate a concrete risk you can name — one focused check per named risk, and name both the risk and what you checked in your report. ")
	b.WriteString("Cross-cutting changes are legitimate named risks: if the diff changes lock ordering, a function or API contract, or shared mutable state, checking the call sites is the right method.\n\n")
	b.WriteString("Your review is read-only on this checkout. Do not mutate the working tree, the index, HEAD, or branch state in any way.\n\n")

	b.WriteString("## Do Not Trust the Report\n\n")
	b.WriteString("Treat the implementer's report as unverified claims about the code. It may be incomplete, inaccurate, or optimistic. ")
	b.WriteString("Verify the claims against the diff. Design rationales in the report are claims too: \"left it per YAGNI,\" \"kept it simple deliberately,\" or any other justification is the implementer grading their own work. ")
	b.WriteString("Judge the code on its merits — a stated rationale never downgrades a finding's severity.\n\n")

	b.WriteString("## Tests\n\n")
	b.WriteString("The implementer already ran the tests and reported results with TDD evidence for exactly this code. Do not re-run the suite to confirm their report. ")
	b.WriteString("Run a test only when reading the code raises a specific doubt that no existing run answers — and then a focused test, never a package-wide suite, race detector run, or repeated/high-count loop. ")
	b.WriteString("If heavy validation seems warranted, recommend it in your report instead of running it. If you cannot run commands in this environment, name the test you would run.\n\n")
	b.WriteString("Warnings or other noise in the implementer's reported test output are findings — test output should be pristine.\n\n")

	b.WriteString("## Part 1: Spec Compliance\n\n")
	b.WriteString("Compare the diff against What Was Requested:\n\n")
	b.WriteString("- **Missing:** requirements they skipped, missed, or claimed without implementing\n")
	b.WriteString("- **Extra:** features that weren't requested, over-engineering, unneeded \"nice to haves\"\n")
	b.WriteString("- **Misunderstood:** right feature built the wrong way, wrong problem solved\n")
	b.WriteString("- **Deviated:** any difference between the brief and the implemented code (changed assertions, renamed identifiers, different behavior, etc.). ")
	b.WriteString("Report every deviation as a finding, even if you think it is reasonable. Do not silently absorb a deviation because the implementer explained it.\n\n")
	b.WriteString("If a requirement cannot be verified from this diff alone (it lives in unchanged code or spans tasks), report it as a ⚠️ item instead of broadening your search.\n\n")

	b.WriteString("## Part 2: Code Quality\n\n")
	b.WriteString("**Code quality:**\n")
	b.WriteString("- Clean separation of concerns?\n")
	b.WriteString("- Proper error handling?\n")
	b.WriteString("- DRY without premature abstraction?\n")
	b.WriteString("- Edge cases handled?\n\n")
	b.WriteString("**Tests:**\n")
	b.WriteString("- Do the new and changed tests verify real behavior, not mocks?\n")
	b.WriteString("- Are the task's edge cases covered?\n\n")
	b.WriteString("**Structure:**\n")
	b.WriteString("- Does each file have one clear responsibility with a well-defined interface?\n")
	b.WriteString("- Are units decomposed so they can be understood and tested independently?\n")
	b.WriteString("- Is the implementation following the file structure from the plan?\n")
	b.WriteString("- Did this change create new files that are already large, or significantly grow existing files? (Don't flag pre-existing file sizes — focus on what this change contributed.)\n\n")
	b.WriteString("Your report should point at evidence: file:line references for every finding and for any check you would otherwise answer with a bare \"yes.\" ")
	b.WriteString("A tight report that cites lines gives the controller everything it needs.\n\n")
	b.WriteString("Your final message is the report itself: begin directly with the spec-compliance verdict. Every line is a verdict, a finding with file:line, or a check you ran — no preamble, no process narration, no closing summary.\n\n")

	b.WriteString("## Calibration\n\n")
	b.WriteString("Categorize issues by actual severity. Not everything is Critical. ")
	b.WriteString("Important means this task cannot be trusted until it is fixed: incorrect or fragile behavior, a missed requirement, or maintainability damage you would block a merge over — verbatim duplication of a logic block, swallowed errors, tests that assert nothing. ")
	b.WriteString("\"Coverage could be broader\" and polish suggestions are Minor.\n\n")
	b.WriteString("If the plan or brief explicitly mandates something this rubric calls a defect (a test that asserts nothing, verbatim duplication of a logic block), that IS a finding — report it as Important, labeled plan-mandated. ")
	b.WriteString("The plan's authorship does not grade its own work; the human decides.\n")
	b.WriteString("Acknowledge what was done well before listing issues — accurate praise helps the implementer trust the rest of the feedback.\n\n")

	b.WriteString("## Output Format\n\n")
	b.WriteString("### Spec Compliance\n\n")
	b.WriteString("- ✅ Spec compliant | ❌ Issues found: [what's missing/extra/misunderstood, with file:line references]\n")
	b.WriteString("- ⚠️ Cannot verify from diff: [requirements you could not verify from the diff alone, and what the controller should check — report alongside the ✅/❌ verdict for everything you could verify]\n\n")
	b.WriteString("### Strengths\n[What's well done? Be specific.]\n\n")
	b.WriteString("### Issues\n\n")
	b.WriteString("#### Critical (Must Fix)\n#### Important (Should Fix)\n#### Minor (Nice to Have)\n\n")
	b.WriteString("For each issue: file:line, what's wrong, why it matters, how to fix (if not obvious).\n\n")
	b.WriteString("### Assessment\n\n")
	b.WriteString("**Task quality:** [Approved | Needs fixes]\n\n")
	b.WriteString("**Reasoning:** [1-2 sentence technical assessment]")
	return b.String()
}

// BuildBranchReviewerPrompt constructs the dispatch prompt for the final
// whole-branch merge-gate review.
func BuildBranchReviewerPrompt(planPath, reportsDir, diffPath, globalConstraints, mergeBase, headSHA string, minorFindings []string) string {
	var b strings.Builder
	b.WriteString("You are the strongest model available, reserved for the final whole-branch review. ")
	b.WriteString("This is a merge gate, not a per-task re-review. You see the full branch diff plus the full plan. ")
	b.WriteString("Trust the per-task reviews; your value is what they cannot see — cross-task integration, contract drift, and missed whole-branch requirements.\n\n")

	fmt.Fprintf(&b, "## What Was Planned\n\nRead the full plan: %s\n\n", planPath)
	fmt.Fprintf(&b, "Global constraints from the spec/design that bind the branch:\n%s\n\n", globalConstraints)

	fmt.Fprintf(&b, "## Per-Task Record\n\nRead the per-task reports and the accumulated Minor findings list the controller tracked across the branch:\n%s\n\n", reportsDir)

	b.WriteString("## Diff Under Review (Whole Branch)\n\n")
	fmt.Fprintf(&b, "**Base (merge base):** %s — the commit the branch started from (e.g. `git merge-base main HEAD`)\n", mergeBase)
	fmt.Fprintf(&b, "**Head:** %s\n", headSHA)
	fmt.Fprintf(&b, "**Diff file:** %s\n\n", diffPath)
	b.WriteString("The diff is MERGE_BASE..HEAD: the full branch in one view. The diff's context lines ARE the changed files: do not Read a changed file separately unless a hunk you must judge is cut off mid-function — and say so in your report. ")
	b.WriteString("Do not re-run git commands. Do not crawl the broader codebase outside the diff. ")
	b.WriteString("Inspect code outside the diff only to evaluate a concrete, named cross-task risk — one focused check per named risk, and name both the risk and what you checked in your report. ")
	b.WriteString("Cross-cutting changes are legitimate named risks: if the diff changes lock ordering, a function or API contract, or shared mutable state, checking the call sites is the right method.\n\n")
	b.WriteString("Your review is read-only on this checkout. Do not mutate the working tree, the index, HEAD, or branch state in any way.\n\n")

	b.WriteString("## Do Not Trust Per-Task Reports Blindly\n\n")
	b.WriteString("Per-task reviews are scoped to one task. They can pass a task that breaks the whole branch: contracts that drift between tasks, types that are compatible by accident, error handling that swallows at a boundary, plan-mandated whole-branch properties no single task owns. ")
	b.WriteString("Use the per-task record as evidence — accurate where scoped, blind beyond the task boundary.\n\n")

	b.WriteString("## Tests\n\n")
	b.WriteString("The implementers and per-task reviewers already ran the tests and reported results. Do not re-run the suite to confirm their reports. ")
	b.WriteString("Run a test only when the whole-branch view raises a specific doubt that no per-task run answers — and then a focused test, never a package-wide suite, race detector run, or repeated/high-count loop. ")
	b.WriteString("If heavy validation seems warranted, recommend it in your report instead of running it. If you cannot run commands in this environment, name the test you would run.\n\n")
	b.WriteString("Warnings or other noise in any per-task reported test output that the per-task reviewer did not resolve are findings — test output should be pristine.\n\n")

	b.WriteString("## Focus Areas (in priority order)\n\n")
	b.WriteString("1. **Whole-plan spec compliance** — walk the plan, confirm every planned capability is delivered on the branch. Missing capabilities, scope cuts, and unannounced departures are Critical.\n")
	b.WriteString("2. **Cross-task integration** — interfaces defined in one task consumed by another, shared types, shared state, lock ordering, transaction boundaries, error propagation across modules. Look for contract drift between tasks that each passed review in isolation.\n")
	b.WriteString("3. **Architecture and design** — does the branch, as a whole, have a coherent shape? Boundaries in the right places? Extensibility where the plan called for it, simplicity where it didn't?\n")
	b.WriteString("4. **Plan-mandated quality** — anything the plan explicitly required (exact values, exact formats, stated relationships between components) that the per-task reviews could not see end-to-end.\n")
	b.WriteString("5. **Net Minor findings** — the controller hands you the Minor findings accumulated across tasks; triage which must be fixed before merge vs. which a future PR can carry.\n\n")

	b.WriteString("## What You Do NOT Do\n\n")
	b.WriteString("- Do not re-flag per-task findings the task reviewers already raised and that were fixed or accepted. Trust the per-task record unless you see new evidence in the whole-branch view.\n")
	b.WriteString("- Do not re-run tests across the whole suite to confirm per-task reports; only run a focused test when the whole-branch view raises a specific doubt no per-task run answers.\n")
	b.WriteString("- Do not re-review a single task in isolation; everything is judged at the branch level.\n\n")

	b.WriteString("## Calibration\n\n")
	b.WriteString("Critical = blocks merge: missing capability, broken cross-task contract, plan-mandated requirement not met anywhere on the branch.\n\n")
	b.WriteString("Important = should fix before merge: cross-task inconsistency that works today but will bite at the next change, architectural smell that the plan's design rules prohibit, accumulated Minor findings that together are Important.\n\n")
	b.WriteString("Minor = polish a future PR can carry.\n\n")
	b.WriteString("If the plan or brief explicitly mandates something this rubric calls a defect, that IS a finding — report it as Important, labeled plan-mandated. The plan's authorship does not grade its own work; the human decides.\n\n")

	b.WriteString("## Deviation Rule\n\n")
	b.WriteString("If the per-task record shows the implementer deviated from the brief and the task reviewer accepted it as reasonable, still surface that deviation in your Accumulated Minor Triage. ")
	b.WriteString("Do not let a task reviewer's rationale silently absorb a departure from the plan. The controller decides whether to accept the deviation; the human must know it happened.\n\n")

	if len(minorFindings) > 0 {
		b.WriteString("## Accumulated Minor Findings\n\n")
		for _, f := range minorFindings {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Output Format\n\n")
	b.WriteString("### Branch Verdict\n\n")
	b.WriteString("- ✅ Ready to merge | ❌ Not ready: [one-line summary of blockers]\n\n")
	b.WriteString("### Whole-Plan Coverage\n\n")
	b.WriteString("[Walk the plan: each planned capability, where on the branch it lives, and whether it is delivered. file:line references.]\n\n")
	b.WriteString("### Cross-Task Integration\n\n")
	b.WriteString("[Contracts between tasks, shared state, error propagation. file:line references.]\n\n")
	b.WriteString("### Architecture\n\n")
	b.WriteString("[Whole-branch shape, boundaries, extensibility. file:line references.]\n\n")
	b.WriteString("### Accumulated Minor Triage\n\n")
	b.WriteString("[For each Minor finding the controller hands you: fix before merge, or carry to a future PR.]\n\n")
	b.WriteString("### Issues\n\n")
	b.WriteString("#### Critical (Must Fix)\n#### Important (Should Fix)\n#### Minor (Nice to Have)\n\n")
	b.WriteString("For each issue: file:line, what's wrong, why it matters at the branch level (not the per-task level), how to fix.\n\n")
	b.WriteString("### Assessment\n\n")
	b.WriteString("**Branch quality:** [Ready to merge | Needs fixes]\n\n")
	b.WriteString("**Reasoning:** [1-2 sentence technical assessment]\n\n")
	b.WriteString("Your final message is the report itself. Begin directly with the branch verdict. No preamble, no process narration, no closing summary.")
	return b.String()
}

// BuildBranchFixPrompt constructs a fix dispatch prompt for an implementer
// subagent addressing branch-reviewer findings (the whole-branch fix wave).
func BuildBranchFixPrompt(findings, worktreeDir string) string {
	var b strings.Builder
	b.WriteString("You are fixing issues found by the branch reviewer across the whole branch.\n\n")
	b.WriteString("## Worktree Discipline (CRITICAL)\n\n")
	fmt.Fprintf(&b, "Work from: %s\n\n", worktreeDir)
	b.WriteString("All `git` commands in this task MUST run from that directory. Use absolute paths: ")
	fmt.Fprintf(&b, "`git -C %s <command>` or `cd %s && git <command>`.\n\n", worktreeDir, worktreeDir)
	b.WriteString("Before every `git commit`, run `git rev-parse --abbrev-ref HEAD` and confirm it matches the expected branch name. ")
	b.WriteString("If it doesn't, STOP and report BLOCKED — do not commit.\n\n")
	b.WriteString("Do not use `git reset --hard` for any reason. If you need to undo, use `git reset --soft HEAD~1` (keeps changes staged) or `git restore --staged <file>` (unstages only). Report the situation and ask if you are unsure.\n\n")
	b.WriteString("## Findings to Fix\n\n")
	b.WriteString(findings)
	b.WriteString("\n\n## Your Job\n\n")
	b.WriteString("1. Fix each finding\n")
	b.WriteString("2. Re-run the tests that cover your changes\n")
	b.WriteString("3. Report back with status and one-line test summary, including the current branch name from `git rev-parse --abbrev-ref HEAD`\n")
	return b.String()
}

// BuildFixPrompt constructs a fix dispatch prompt for an implementer
// subagent addressing reviewer findings.
func BuildFixPrompt(task PlanTask, reportPath, worktreeDir, findings string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are fixing issues found by the reviewer for Task %d: %s\n\n", task.Number, task.Title)
	fmt.Fprintf(&b, "Read your previous report: %s\n\n", reportPath)

	b.WriteString("## Worktree Discipline (CRITICAL)\n\n")
	fmt.Fprintf(&b, "Work from: %s\n\n", worktreeDir)
	b.WriteString("All `git` commands in this task MUST run from that directory. Use absolute paths: ")
	fmt.Fprintf(&b, "`git -C %s <command>` or `cd %s && git <command>`.\n\n", worktreeDir, worktreeDir)
	b.WriteString("Before every `git commit`, run `git rev-parse --abbrev-ref HEAD` and confirm it matches the expected branch name. ")
	b.WriteString("If it doesn't, STOP and report BLOCKED — do not commit.\n\n")
	b.WriteString("Do not use `git reset --hard` for any reason. If you need to undo, use `git reset --soft HEAD~1` (keeps changes staged) or `git restore --staged <file>` (unstages only). Report the situation and ask if you are unsure.\n\n")

	b.WriteString("## Findings to Fix\n\n")
	b.WriteString(findings)
	b.WriteString("\n\n## Your Job\n\n")
	b.WriteString("1. Fix each finding\n")
	b.WriteString("2. Re-run the tests that cover your change\n")
	b.WriteString("3. Append your fix report (with test results) to the same report file\n")
	b.WriteString("4. Report back with status and one-line test summary, including the current branch name from `git rev-parse --abbrev-ref HEAD`\n")
	return b.String()
}
