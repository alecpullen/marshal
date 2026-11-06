package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/diffview"
)

// sddResumeHint is the single wording for how to continue a stopped run.
// It appears in the run panel, the side rail, the gate panel's stop path,
// and the outcome doc; three divergent phrasings previously disagreed about
// whether bare /sdd resumes (it does not — it opens the plan picker).
const sddResumeHint = "Run /sdd and pick the same plan to resume from the ledger."

// The two row actions a run outcome doc carries. The TUI builds the doc, and
// both reads go through the session: session/diff and session/file.
const (
	outcomeDiffAction   = "diff"
	outcomeLedgerAction = "ledger"
)

// runOutcomeDoc describes a finished plan run: what it produced, where the
// work is, and what to do next.
//
// It exists because a successful run deletes its worktree (correctly — see
// controller.go CleanupWorktrees) and leaves only a branch name, while a
// failed run collapsed to an 80-character truncated error with no route to
// the ledger or the per-task reports.
func runOutcomeDoc(p acpwire.SDDProgress, now time.Time) acpwire.WireDoc {
	if !p.Finished && !p.Active {
		return acpwire.WireDoc{
			Title:  "Plan run",
			Rows:   []acpwire.WireRow{{Text: "No plan run in this session."}},
			Footer: "Run /sdd to start one.",
		}
	}

	live := p.Active && !p.Finished

	// A live run has no EndedAt; measuring against it produced an
	// overflowed negative duration.
	end := acpwire.Time(p.EndedAt)
	if end.IsZero() {
		end = now
	}
	status := "stopped"
	switch {
	case live:
		status = "running"
	case p.Succeeded:
		status = "completed"
	}

	var rows []acpwire.WireRow
	rows = append(rows, acpwire.WireRow{Header: "Run"})
	rows = append(rows, acpwire.WireRow{
		Text:   p.PlanName,
		Detail: fmt.Sprintf("%s · %d/%d tasks · %s", status, p.DoneTasks, p.TotalTasks, formatElapsed(end.Sub(acpwire.Time(p.StartedAt)))),
	})
	if p.Branch != "" {
		rows = append(rows, acpwire.WireRow{Text: "branch", Detail: p.Branch})
	}
	if p.Error != "" {
		// The full reason, not the 80-character panel truncation.
		rows = append(rows, acpwire.WireRow{Text: "reason", Detail: p.Error})
	}

	if live {
		progress := fmt.Sprintf("task %d/%d", p.CurrentTask, p.TotalTasks)
		if p.TotalTasks > 0 {
			progress += " · " + formatPercent(p.DoneTasks, p.TotalTasks)
		}
		if p.Phase != "" {
			progress += " · " + p.Phase
		}
		if low, high, openEnded, ok := estimateRemaining(p, now); ok {
			progress += " · " + formatETA(low, high, openEnded)
		}
		rows = append(rows, acpwire.WireRow{Header: "Progress"})
		rows = append(rows, acpwire.WireRow{Text: progress})
	}
	if len(p.Tasks) > 0 {
		rows = append(rows, acpwire.WireRow{Header: "Plan"})
		for i, title := range p.Tasks {
			rows = append(rows, acpwire.WireRow{
				Text:   fmt.Sprintf("%s %d %s", taskGlyph(i, p), i+1, title),
				Detail: taskDuration(i, p, now),
			})
			if len(p.TaskExecTypes) > i && p.TaskExecTypes[i] != "" {
				if rows[len(rows)-1].Detail != "" {
					rows[len(rows)-1].Detail += "   " + p.TaskExecTypes[i]
				} else {
					rows[len(rows)-1].Detail = p.TaskExecTypes[i]
				}
			}
		}
	}

	rows = append(rows, acpwire.WireRow{Header: "Inspect"})
	if p.Branch != "" && p.BaseRef != "" {
		rows = append(rows, acpwire.WireRow{
			Text:        "branch diff",
			Detail:      p.BaseRef + ".." + p.Branch,
			ActionLabel: "view",
			ActionID:    outcomeDiffAction,
		})
	}
	if p.LedgerPath != "" {
		rows = append(rows, acpwire.WireRow{
			Text:        "ledger",
			Detail:      p.LedgerPath,
			ActionLabel: "read",
			ActionID:    outcomeLedgerAction,
		})
	}
	if p.ArtifactsDir != "" {
		rows = append(rows, acpwire.WireRow{
			Text:   "artifacts",
			Detail: p.ArtifactsDir,
			Desc:   "per-task briefs, implementer reports, and review verdicts",
		})
	}

	footer := "Review the branch and merge it yourself when you are satisfied."
	if !p.Succeeded {
		footer = sddResumeHint
	}
	return acpwire.WireDoc{Title: "Plan run — " + p.PlanName, Rows: rows, Footer: footer}
}

// runOutcomeActions returns the actions the outcome doc's action rows run: the
// branch diff and the ledger read, both through the session so the TUI makes
// no git or file read of its own.
func runOutcomeActions(ctx context.Context, sess acpclient.Session, p acpwire.SDDProgress, workingDir string, width int) map[string]func() acpwire.CommandResult {
	acts := make(map[string]func() acpwire.CommandResult, 2)
	if p.Branch != "" && p.BaseRef != "" {
		acts[outcomeDiffAction] = func() acpwire.CommandResult {
			return acpwire.CommandResult{Text: branchDiff(ctx, sess, p.BaseRef, p.Branch, width)}
		}
	}
	if p.LedgerPath != "" {
		acts[outcomeLedgerAction] = func() acpwire.CommandResult {
			return acpwire.CommandResult{Text: readLedger(ctx, sess, workingDir, p.LedgerPath)}
		}
	}
	return acts
}

// taskGlyph marks a task done, in progress, or pending — the same status
// derivation the side rail uses.
func taskGlyph(i int, p acpwire.SDDProgress) string {
	switch {
	case i < p.DoneTasks:
		return glyph.OK
	case i == p.CurrentTask-1 && p.Active:
		return glyph.Running
	default:
		return glyph.Ambient
	}
}

// taskDuration renders a task's elapsed time: its full duration once
// complete, its running time while in flight, and nothing when pending.
func taskDuration(i int, p acpwire.SDDProgress, now time.Time) string {
	if i < 0 || i >= len(p.TaskTimings) {
		return ""
	}
	t := p.TaskTimings[i]
	started := acpwire.Time(t.StartedAt)
	if started.IsZero() {
		return ""
	}
	end := acpwire.Time(t.EndedAt)
	if end.IsZero() {
		end = now
	}
	if d := end.Sub(started); d > 0 {
		return formatElapsed(d)
	}
	return ""
}

// branchDiff renders the run branch's changes against its base, read through
// the session's diff. It is strictly read-only: no checkout, no merge, no
// branch switch.
func branchDiff(ctx context.Context, sess acpclient.Session, base, branch string, width int) string {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := sess.Diff(ctx, acpwire.DiffQuery{Base: base, Head: branch})
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

// readLedger returns the run's ledger, read through the session. session/file
// takes a path relative to the working directory, so the ledger's own path is
// made relative first. A path outside the working directory is a refusal, not
// a read: the ledger belongs to the run's repository.
func readLedger(ctx context.Context, sess acpclient.Session, workingDir, ledgerPath string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	rel, ok := ledgerRel(workingDir, ledgerPath)
	if !ok {
		return fmt.Sprintf("Could not read %s: it is outside the working directory", ledgerPath)
	}
	file, err := sess.ReadFile(ctx, rel)
	if err != nil {
		return fmt.Sprintf("Could not read %s: %v", ledgerPath, err)
	}
	if file.Binary {
		return ledgerPath + " is not a text file."
	}
	if file.Size == 0 {
		return ledgerPath + " is empty."
	}
	content := file.Content
	if file.Truncated {
		// session/file caps what it returns, so say so rather than presenting
		// a cut ledger as the whole one.
		content += "\n\n… (truncated: the ledger is larger than the session's read limit)"
	}
	return content
}

// ledgerRel makes the ledger's path relative to the working directory, which
// is the form session/file takes. ok is false when the ledger is outside it.
func ledgerRel(workingDir, ledgerPath string) (string, bool) {
	if rel, ok := relInside(workingDir, ledgerPath); ok {
		return rel, true
	}
	// A working directory reached through a symlink spells the same place two
	// ways (/tmp and /private/tmp on macOS), and a lexical Rel calls the
	// ledger outside it. Resolve both before refusing: session/file's own
	// containment check still decides whether the read happens.
	resolvedDir, dirErr := filepath.EvalSymlinks(workingDir)
	resolvedLedger, ledgerErr := filepath.EvalSymlinks(ledgerPath)
	if dirErr != nil || ledgerErr != nil {
		return "", false
	}
	return relInside(resolvedDir, resolvedLedger)
}

// relInside is the lexical half of ledgerRel: the path relative to dir, and
// whether it stays inside it.
func relInside(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
