package tui

import (
	"fmt"
	"os"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/diffview"
	"marshal/internal/worktree"
)

// sddResumeHint is the single wording for how to continue a stopped run.
// It appears in the run panel, the side rail, the gate panel's stop path,
// and the outcome doc; three divergent phrasings previously disagreed about
// whether bare /sdd resumes (it does not — it opens the plan picker).
const sddResumeHint = "Run /sdd and pick the same plan to resume from the ledger."

// diffContextLines is the context width for the branch diff.
const diffContextLines = 3

// runOutcomeDoc describes a finished plan run: what it produced, where the
// work is, and what to do next.
//
// It exists because a successful run deletes its worktree (correctly — see
// controller.go CleanupWorktrees) and leaves only a branch name, while a
// failed run collapsed to an 80-character truncated error with no route to
// the ledger or the per-task reports.
func runOutcomeDoc(p session.SDDProgress, git worktree.GitOps, repoRoot string, width int) commands.Doc {
	if !p.Finished && !p.Active {
		return commands.Doc{
			Title:  "Plan run",
			Rows:   []commands.Row{{Text: "No plan run in this session."}},
			Footer: "Run /sdd to start one.",
		}
	}

	var rows []commands.Row

	status := "stopped"
	if p.Succeeded {
		status = "completed"
	}
	rows = append(rows, commands.Row{Header: "Run"})
	rows = append(rows, commands.Row{
		Text:   p.PlanName,
		Detail: fmt.Sprintf("%s · %d/%d tasks · %s", status, p.DoneTasks, p.TotalTasks, formatElapsed(p.EndedAt.Sub(p.StartedAt))),
	})
	if p.Branch != "" {
		rows = append(rows, commands.Row{Text: "branch", Detail: p.Branch})
	}
	if p.Error != "" {
		// The full reason, not the 80-character panel truncation.
		rows = append(rows, commands.Row{Text: "reason", Detail: p.Error})
	}

	rows = append(rows, commands.Row{Header: "Inspect"})
	if p.Branch != "" && p.BaseRef != "" {
		rows = append(rows, commands.Row{
			Text:        "branch diff",
			Detail:      p.BaseRef + ".." + p.Branch,
			ActionLabel: "view",
			Action: func(state *session.State) commands.Result {
				return commands.Result{Text: branchDiff(git, repoRoot, p.BaseRef, p.Branch, width)}
			},
		})
	}
	if p.LedgerPath != "" {
		rows = append(rows, commands.Row{
			Text:        "ledger",
			Detail:      p.LedgerPath,
			ActionLabel: "read",
			Action: func(state *session.State) commands.Result {
				return commands.Result{Text: readFileOrExplain(p.LedgerPath)}
			},
		})
	}
	if p.ArtifactsDir != "" {
		rows = append(rows, commands.Row{
			Text:   "artifacts",
			Detail: p.ArtifactsDir,
			Desc:   "per-task briefs, implementer reports, and review verdicts",
		})
	}

	footer := "Review the branch and merge it yourself when you are satisfied."
	if !p.Succeeded {
		footer = sddResumeHint
	}
	return commands.Doc{Title: "Plan run — " + p.PlanName, Rows: rows, Footer: footer}
}

// branchDiff renders the run branch's changes against its base. It is
// strictly read-only: no checkout, no merge, no branch switch.
func branchDiff(git worktree.GitOps, repoRoot, base, branch string, width int) string {
	raw, err := git.Diff(repoRoot, base+".."+branch, diffContextLines)
	if err != nil {
		return fmt.Sprintf("Could not read the diff for %s..%s: %v", base, branch, err)
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Sprintf("No changes between %s and %s.", base, branch)
	}
	return diffview.Render(raw, diffview.Options{
		Width:     width,
		Mode:      diffview.ModeAuto,
		Highlight: true,
	})
}

// readFileOrExplain returns a file's contents, or a plain explanation when
// it is gone — a deleted ledger is an empty state, not an error banner.
func readFileOrExplain(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Could not read %s: %v", path, err)
	}
	if len(b) == 0 {
		return path + " is empty."
	}
	return string(b)
}
