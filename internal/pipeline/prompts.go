package pipeline

import (
	"fmt"
	"strings"
	"text/template"
)

// reportContract is appended to every implementer and fixer prompt. Its
// format is what ParseImplementerReport accepts; the two must not drift.
const reportContract = `
## Report

Write your full report to {{.ReportPath}} — what you changed, why, the
commands you ran, and their output. Then return only these lines:

    STATUS: DONE | DONE_WITH_CONCERNS | NEEDS_CONTEXT | BLOCKED
    TESTS: <the test command you ran and its result, one line>
    CONCERNS: <one line, only if STATUS is DONE_WITH_CONCERNS>
    QUESTION: <one line, required if STATUS is NEEDS_CONTEXT or BLOCKED>

Use NEEDS_CONTEXT when you need information nobody gave you, and BLOCKED
when the task cannot be completed as written. Do not guess and do not
invent requirements; asking costs one round trip, guessing costs the task.
`

const noCommitRule = `
## Git

Leave every change uncommitted in the working tree. do NOT commit; do NOT run
` + "`git commit`, `git add`, `git reset`, `git checkout`, `git stash`," + `
or any other command that writes git state. The controller inspects your
working tree, runs the build and test gate, and commits for you. Reading
git state (` + "`git status`, `git diff`" + `) is fine.
`

var implementerTmpl = template.Must(template.New("implementer").Parse(
	`You are implementing Task {{.TaskN}}: {{.Title}}

## Where this fits

{{.Placement}}

## Requirements

Read {{.BriefPath}} first — it is your requirements, with the exact values
to use verbatim. It is the full text of your task from the plan. Implement
exactly what it specifies: nothing missing, nothing extra.
{{if .Interfaces}}
## Interfaces from earlier tasks

{{.Interfaces}}
{{end}}{{if .Answer}}
## Answer to your earlier question

{{.Answer}}
{{end}}
## How to work

Work from: {{.WorkDir}}

1. Implement exactly what the brief specifies, following the repository's
   existing patterns.
2. Write the tests the brief calls for, in the style the brief shows.
3. While iterating, run the focused test for what you are changing. Run the
   broader suite once when you believe you are done.
4. Self-review your own diff before reporting: did you build what the brief
   asked for, and only that?

If a file you are creating grows well beyond the brief's intent, stop and
report DONE_WITH_CONCERNS rather than restructuring on your own.
` + noCommitRule + reportContract))

var fixTmpl = template.Must(template.New("fix").Parse(
	`You are fixing Task {{.TaskN}}. The work is already in the working tree at
{{.WorkDir}}; {{.Reason}}.

## Original requirements

Read {{.BriefPath}} — the task's requirements are unchanged.

## What was reported before

Read {{.ReportPath}} for what the previous pass claims it built.

## What must change

{{range .Findings}}- [{{.Severity}}] {{.Text}}
{{end}}
Fix every item above. Do not make unrelated changes.
{{if .CoveringTests}}
## Verification

Re-run the tests covering your change ({{.CoveringTests}}) and report the
command and its output.
{{end}}` + noCommitRule + reportContract))

var reviewTmpl = template.Must(template.New("review").Parse(
	`You are reviewing one task's implementation: first whether it matches its
requirements, then whether it is well-built. This is a task-scoped gate,
not a merge review — a whole-branch review happens separately after all
tasks are complete.

## What was requested

Read the task brief: {{.BriefPath}}

Task {{.TaskN}}: {{.Title}}

Global constraints from the plan that bind this task:

{{.GlobalConstraints}}

## What the implementer claims they built

Read the implementer's report: {{.ReportPath}}

## The change under review

Read {{.PackagePath}} once — it contains the commit list, the stat summary,
and the full diff with surrounding context, and it is your view of the
change. The diff's context lines ARE the changed files: do not read a
changed file separately unless a hunk you must judge is cut off
mid-function, and say so in your report if you do. Do not re-run git
commands. Do not crawl the broader codebase; inspect code outside the diff
only to evaluate a concrete risk you can name, one focused check per named
risk, naming both in your report.

Your review is read-only. Do not modify the working tree.

## Judge

1. Spec compliance: is every requirement in the brief met, with nothing
   extra that the brief did not ask for?
2. Quality: is it correct, tested, and maintainable? Are the tests real
   tests — do they assert behaviour that would fail if the code were wrong?

## Report

Write your reasoning to {{.ReviewPath}}, then return only these lines:

    SPEC: PASS | FAIL
    QUALITY: APPROVED | CHANGES_REQUESTED
    FINDINGS:
    - [Critical] <one line>
    - [Important] <one line>
    - [Minor] <one line>

Both verdicts are required. Use Critical for defects that break behaviour,
Important for defects that will cause real problems, and Minor for polish.
If you have no findings, write ` + "`FINDINGS:`" + ` followed by ` + "`- none`" + `.
`))

var branchReviewTmpl = template.Must(template.New("branch").Parse(
	`You are the final review before this branch merges. Every task in the plan
has been implemented, gated on build and tests, and reviewed individually.
Your job is what per-task review could not see: cross-task integration,
whole-plan coverage, and architecture.

## The plan

Read {{.PlanPath}} — the full plan this branch implements.

## The change

Read {{.PackagePath}} — the commit list, stat summary, and full diff for
{{.Range}}.
{{if .Minors}}
## Minor findings recorded during the run

These were raised by per-task reviews and deferred to you. Triage them:
say which must be fixed before merge and which can stand.

{{range .Minors}}- {{.}}
{{end}}{{end}}
## Judge

1. Coverage: does the branch implement everything the plan required?
2. Integration: do the tasks fit together — consistent interfaces, no
   duplicated or contradictory logic across tasks, no dead code left by an
   earlier task that a later one replaced?
3. Architecture: is this a sound shape for the codebase it lives in?

Your review is read-only. Do not modify the working tree.

## Report

Write your reasoning to {{.ReviewPath}}, then return only these lines:

    SPEC: PASS | FAIL
    QUALITY: APPROVED | CHANGES_REQUESTED
    FINDINGS:
    - [Critical] <one line>
    - [Important] <one line>
    - [Minor] <one line>

Both verdicts are required. If you have no findings, write
` + "`FINDINGS:`" + ` followed by ` + "`- none`" + `.
`))

// ImplementerPrompt is the input to RenderImplementer. Answer is set only
// when re-dispatching after a human resolved the implementer's question.
type ImplementerPrompt struct {
	TaskN      int
	Title      string
	Placement  string
	BriefPath  string
	ReportPath string
	WorkDir    string
	Interfaces string
	Answer     string
}

// FixPrompt is the input to RenderFix. Findings carries every item to fix
// in one dispatch; the controller never dispatches one fixer per finding.
type FixPrompt struct {
	TaskN         int
	BriefPath     string
	ReportPath    string
	WorkDir       string
	Reason        string
	Findings      []Finding
	CoveringTests string
}

// ReviewPrompt is the input to RenderReview.
type ReviewPrompt struct {
	TaskN             int
	Title             string
	BriefPath         string
	ReportPath        string
	PackagePath       string
	ReviewPath        string
	GlobalConstraints string
}

// BranchReviewPrompt is the input to RenderBranchReview.
type BranchReviewPrompt struct {
	PlanPath    string
	PackagePath string
	ReviewPath  string
	Range       string
	Minors      []string
}

func render(t *template.Template, data any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("pipeline prompts: render %s: %w", t.Name(), err)
	}
	return b.String(), nil
}

func RenderImplementer(p ImplementerPrompt) (string, error) { return render(implementerTmpl, p) }
func RenderFix(p FixPrompt) (string, error)                 { return render(fixTmpl, p) }
func RenderReview(p ReviewPrompt) (string, error)           { return render(reviewTmpl, p) }
func RenderBranchReview(p BranchReviewPrompt) (string, error) {
	return render(branchReviewTmpl, p)
}
