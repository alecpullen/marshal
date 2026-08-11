package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/strutil"
)

// runReviewSubagent dispatches a read-only reviewer subagent over the
// current working-tree changes, or over the user-supplied focus if non-empty.
// The review prompt is constructed here in the app layer, not in the TUI.
func runReviewSubagent(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, focus, model, reviewRange string) error {
	// Resolve and validate range before registering any subagent card so
	// a bad argument leaves no UI residue.
	resolvedRange, err := resolveReviewRange(state.WorkingDir, reviewRange)
	if err != nil {
		return err
	}

	req := agent.SubagentRequest{Role: routing.RoleReviewer}
	if model != "" {
		req.Model = model
	}
	child, childState, err := factory(req)
	if err != nil {
		return fmt.Errorf("review: build subagent: %w", err)
	}

	label := "current changes"
	if focus != "" {
		label = focus
	}
	if resolvedRange != "" {
		label = label + " · " + resolvedRange
	}

	meta := session.SubagentMeta{
		Role: routing.RoleReviewer,
	}
	if child.Model != "" {
		meta.Model = child.Model
	}
	if child.Provider != nil {
		meta.Provider = child.Provider.Name()
	}
	view := state.RegisterSubagentWithMeta("review · "+label, childState, meta)

	prompt := buildReviewPrompt(state.WorkingDir, focus, resolvedRange)
	task, err := child.RunTask(ctx, prompt)
	var summary string
	if task != nil {
		summary = task.Summary
	}
	if task != nil && task.SalvagedReason != "" {
		state.SetSubagentSalvaged(view.ID, task.SalvagedReason)
	}
	state.FinishSubagent(view.ID, summary, err)
	if err != nil {
		return fmt.Errorf("review: subagent run failed: %w", err)
	}
	if summary != "" {
		if task != nil && task.SalvagedReason != "" {
			// Salvaged reports are partial: the TUI shows the salvage
			// marker and history replay (history.go:62-63) excludes them,
			// so the next turn does not treat a truncated review as
			// complete.
			state.AddMessageSalvaged(session.RoleAssistant, summary, session.ContentTypeMarkdown, task.SalvagedReason)
		} else {
			// Final so the next turn's history replay includes the review;
			// plain AddMessage (Final=false) left it TUI-only.
			state.AddMessageFinal(session.RoleAssistant, summary, session.ContentTypeMarkdown)
		}
	}
	return nil
}

// resolveReviewRange validates an explicit range or resolves a --base ref
// to a merge-base..HEAD range. Empty input means working-tree mode.
func resolveReviewRange(workingDir, reviewRange string) (string, error) {
	if reviewRange == "" {
		return "", nil
	}

	// Explicit range like main...HEAD or main..HEAD.
	if strings.Contains(reviewRange, "..") {
		if strings.Contains(reviewRange, " ") {
			return "", fmt.Errorf("review: range must not contain spaces")
		}
		// Validate both endpoints exist.
		sep := ".."
		if strings.Contains(reviewRange, "...") {
			sep = "..."
		}
		parts := strings.SplitN(reviewRange, sep, 2)
		for _, endpoint := range parts {
			if endpoint == "" {
				continue
			}
			if _, err := gitOutput(workingDir, "rev-parse", "--verify", endpoint); err != nil {
				return "", fmt.Errorf("review: invalid range endpoint %q: %w", endpoint, err)
			}
		}
		return reviewRange, nil
	}

	// --base <ref> is passed through as the ref itself; resolve to merge-base.
	base, err := gitOutput(workingDir, "merge-base", reviewRange, "HEAD")
	if err != nil {
		return "", fmt.Errorf("review: cannot find merge-base for %q: %w", reviewRange, err)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("review: empty merge-base for %q", reviewRange)
	}
	return base + "..HEAD", nil
}

func buildReviewPrompt(workingDir, focus, reviewRange string) string {
	var b strings.Builder
	b.WriteString("Review the following code or changes and report findings ordered by severity. " +
		"For each issue, include a file:line reference if applicable. Do not make any edits; this is a read-only review.\n\n")

	if focus != "" {
		b.WriteString("Focus area: ")
		b.WriteString(focus)
		b.WriteString("\n\n")
	}

	if reviewRange != "" {
		b.WriteString("Review the changes in range ")
		b.WriteString(reviewRange)
		b.WriteString(".\n\n")
		if log, err := gitOutput(workingDir, "log", "--oneline", reviewRange); err == nil && log != "" {
			b.WriteString("Commits:\n")
			b.WriteString(strutil.Truncate(log, 20000, true))
			b.WriteString("\n\n")
		}
		if stat, err := gitOutput(workingDir, "diff", "--stat", reviewRange); err == nil && stat != "" {
			b.WriteString("Diff stat:\n")
			b.WriteString(strutil.Truncate(stat, 20000, true))
			b.WriteString("\n\n")
		}
		if diff, err := gitOutput(workingDir, "diff", reviewRange); err == nil && diff != "" {
			b.WriteString("Diff:\n")
			b.WriteString(strutil.Truncate(diff, 100000, true))
			b.WriteString("\n\n")
		}
		// A dirty tree means the range under-represents what the user is
		// about to commit — include the working-tree diff too.
		if status, err := gitOutput(workingDir, "status", "--porcelain"); err == nil && status != "" {
			b.WriteString("Working tree also has uncommitted changes:\n")
			b.WriteString(status)
			b.WriteString("\n\n")
			if wtdiff, err := gitOutput(workingDir, "diff", "HEAD"); err == nil && wtdiff != "" {
				b.WriteString("Working-tree diff:\n")
				b.WriteString(strutil.Truncate(wtdiff, 100000, true))
				b.WriteString("\n\n")
			}
		}
	} else {
		b.WriteString("Review the working-tree changes (uncommitted modifications) in this repository.\n\n")
		if status, err := gitOutput(workingDir, "status", "--porcelain"); err == nil && status != "" {
			b.WriteString("Git status:\n")
			b.WriteString(status)
			b.WriteString("\n\n")
		}
		base := defaultBranch(workingDir)
		if diff, err := gitOutput(workingDir, "diff", base); err == nil && diff != "" {
			b.WriteString("Diff against ")
			b.WriteString(base)
			b.WriteString(":\n")
			b.WriteString(diff)
			b.WriteString("\n\n")
		} else if diff, err := gitOutput(workingDir, "diff", "HEAD"); err == nil && diff != "" {
			b.WriteString("Diff against HEAD:\n")
			b.WriteString(diff)
			b.WriteString("\n\n")
		}
	}

	b.WriteString("Conclude with a short summary of the most important action items.")
	return b.String()
}

func gitOutput(workingDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func defaultBranch(workingDir string) string {
	if out, err := gitOutput(workingDir, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		out = strings.TrimSpace(out)
		if strings.HasPrefix(out, "refs/remotes/origin/") {
			return strings.TrimPrefix(out, "refs/remotes/origin/")
		}
		return out
	}
	return "HEAD"
}
