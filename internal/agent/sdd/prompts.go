package sdd

import (
	"fmt"
	"strings"
)

// BuildImplementerPrompt constructs the dispatch prompt for an SDD
// implementer subagent.
func BuildImplementerPrompt(task PlanTask, briefPath, reportPath, context string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are implementing Task %d: %s\n\n", task.Number, task.Title)
	fmt.Fprintf(&b, "## Task Description\n\nRead your task brief first: %s\nIt contains the full task text from the plan.\n\n", briefPath)
	fmt.Fprintf(&b, "## Context\n\n%s\n\n", context)
	b.WriteString("## Your Job\n\n")
	b.WriteString("1. Implement exactly what the task specifies\n")
	b.WriteString("2. Write tests (following TDD if the task says to)\n")
	b.WriteString("3. Verify implementation works\n")
	b.WriteString("4. Commit your work\n")
	b.WriteString("5. Self-review your work\n")
	b.WriteString("6. Report back\n\n")
	fmt.Fprintf(&b, "## Report Format\n\nWrite your full report to %s:\n", reportPath)
	b.WriteString("- What you implemented\n- What you tested and test results\n")
	b.WriteString("- Files changed\n- Self-review findings\n- Any issues or concerns\n\n")
	b.WriteString("Then report back with ONLY:\n")
	b.WriteString("- Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT\n")
	b.WriteString("- Commits created (short SHA + subject)\n")
	b.WriteString("- One-line test summary\n")
	b.WriteString("- Your concerns, if any\n")
	return b.String()
}

// BuildTaskReviewerPrompt constructs the dispatch prompt for an SDD task
// reviewer subagent.
func BuildTaskReviewerPrompt(briefPath, reportPath, diffPath, globalConstraints, baseSHA, headSHA string) string {
	var b strings.Builder
	b.WriteString("You are reviewing one task's implementation: spec compliance then code quality.\n\n")
	fmt.Fprintf(&b, "## What Was Requested\n\nRead the task brief: %s\n\n", briefPath)
	fmt.Fprintf(&b, "Global constraints from the spec/design that bind this task:\n%s\n\n", globalConstraints)
	fmt.Fprintf(&b, "## What the Implementer Claims They Built\n\nRead the implementer's report: %s\n\n", reportPath)
	b.WriteString("## Diff Under Review\n\n")
	fmt.Fprintf(&b, "**Base:** %s\n**Head:** %s\n**Diff file:** %s\n\n", baseSHA, headSHA, diffPath)
	b.WriteString("Read the diff file once. Do not re-run git commands.\n\n")
	b.WriteString("## Part 1: Spec Compliance\n\n")
	b.WriteString("Compare the diff against What Was Requested:\n")
	b.WriteString("- Missing: requirements they skipped or missed\n")
	b.WriteString("- Extra: features that weren't requested\n")
	b.WriteString("- Misunderstood: right feature built the wrong way\n\n")
	b.WriteString("## Part 2: Code Quality\n\n")
	b.WriteString("- Clean separation of concerns?\n- Proper error handling?\n")
	b.WriteString("- Edge cases handled?\n- Tests verify real behavior?\n\n")
	b.WriteString("## Output Format\n\n### Spec Compliance\n\n- ✅ Spec compliant | ❌ Issues found | ⚠️ Cannot verify from diff\n\n")
	b.WriteString("### Strengths\n[What's well done?]\n\n### Issues\n\n")
	b.WriteString("#### Critical (Must Fix)\n#### Important (Should Fix)\n#### Minor (Nice to Have)\n\n")
	b.WriteString("### Assessment\n\n**Task quality:** Approved | Needs fixes\n")
	return b.String()
}

// BuildBranchReviewerPrompt constructs the dispatch prompt for the final
// whole-branch merge-gate review.
func BuildBranchReviewerPrompt(planPath, reportsDir, diffPath, globalConstraints, mergeBase, headSHA string, minorFindings []string) string {
	var b strings.Builder
	b.WriteString("You are the SDD branch reviewer — the merge gate. You see the full branch diff plus the full plan.\n\n")
	fmt.Fprintf(&b, "## What Was Planned\n\nRead the full plan: %s\n\n", planPath)
	fmt.Fprintf(&b, "Global constraints:\n%s\n\n", globalConstraints)
	fmt.Fprintf(&b, "## Per-Task Record\n\nRead the per-task reports: %s\n\n", reportsDir)
	b.WriteString("## Diff Under Review (Whole Branch)\n\n")
	fmt.Fprintf(&b, "**Base (merge base):** %s\n**Head:** %s\n**Diff file:** %s\n\n", mergeBase, headSHA, diffPath)
	b.WriteString("## Focus Areas\n\n")
	b.WriteString("1. Whole-plan spec compliance\n2. Cross-task integration\n3. Architecture and design\n4. Plan-mandated quality\n5. Net Minor findings triage\n\n")
	if len(minorFindings) > 0 {
		b.WriteString("## Accumulated Minor Findings\n\n")
		for _, f := range minorFindings {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Output Format\n\n### Branch Verdict\n\n- ✅ Ready to merge | ❌ Not ready\n\n")
	b.WriteString("### Whole-Plan Coverage\n### Cross-Task Integration\n### Architecture\n\n")
	b.WriteString("### Issues\n\n#### Critical (Must Fix)\n#### Important (Should Fix)\n#### Minor (Nice to Have)\n\n")
	b.WriteString("### Assessment\n\n**Branch quality:** Ready to merge | Needs fixes\n")
	return b.String()
}

// BuildBranchFixPrompt constructs a fix dispatch prompt for an implementer
// subagent addressing branch-reviewer findings (the whole-branch fix wave).
func BuildBranchFixPrompt(findings string) string {
	var b strings.Builder
	b.WriteString("You are fixing issues found by the branch reviewer across the whole branch.\n\n")
	b.WriteString("## Findings to Fix\n\n")
	b.WriteString(findings)
	b.WriteString("\n\n## Your Job\n\n1. Fix each finding\n2. Re-run the tests that cover your changes\n")
	b.WriteString("3. Report back with status and one-line test summary\n")
	return b.String()
}

// BuildFixPrompt constructs a fix dispatch prompt for an implementer
// subagent addressing reviewer findings.
func BuildFixPrompt(reportPath, findings string, task PlanTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are fixing issues found by the reviewer for Task %d: %s\n\n", task.Number, task.Title)
	fmt.Fprintf(&b, "Read your previous report: %s\n\n", reportPath)
	b.WriteString("## Findings to Fix\n\n")
	b.WriteString(findings)
	b.WriteString("\n\n## Your Job\n\n1. Fix each finding\n2. Re-run the tests that cover your change\n")
	b.WriteString("3. Append your fix report (with test results) to the same report file\n")
	b.WriteString("4. Report back with status and one-line test summary\n")
	return b.String()
}
