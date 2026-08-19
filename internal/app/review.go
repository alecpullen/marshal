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

func runReviewSubagent(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, focus, model, reviewRange string) error {
	resolvedRange, err := resolveReviewRange(ctx, state.WorkingDir, reviewRange)
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
		label += " · " + resolvedRange
	}
	meta := session.SubagentMeta{Role: routing.RoleReviewer}
	if child.Model != "" {
		meta.Model = child.Model
	}
	if child.Provider != nil {
		meta.Provider = child.Provider.Name()
	}
	view := state.RegisterSubagentWithMeta("review · "+label, childState, meta)
	task, err := child.RunTask(ctx, buildReviewPrompt(ctx, state.WorkingDir, focus, resolvedRange))
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
			state.AddMessageSalvaged(session.RoleAssistant, summary, session.ContentTypeMarkdown, task.SalvagedReason)
		} else {
			state.AddMessageFinal(session.RoleAssistant, summary, session.ContentTypeMarkdown)
		}
	}
	return nil
}

func resolveReviewRange(ctx context.Context, workingDir, reviewRange string) (string, error) {
	if reviewRange == "" {
		return "", nil
	}
	if strings.HasPrefix(reviewRange, "base:") {
		ref := strings.TrimPrefix(reviewRange, "base:")
		base, err := gitOutput(ctx, workingDir, "merge-base", ref, "HEAD")
		if err != nil {
			return "", fmt.Errorf("review: cannot find merge-base for %q: %w", ref, err)
		}
		base = strings.TrimSpace(base)
		if base == "" {
			return "", fmt.Errorf("review: empty merge-base for %q", ref)
		}
		return base + "..HEAD", nil
	}
	if !strings.Contains(reviewRange, "..") {
		return "", fmt.Errorf("review: invalid range %q", reviewRange)
	}
	if strings.Contains(reviewRange, " ") {
		return "", fmt.Errorf("review: range must not contain spaces")
	}
	sep := ".."
	if strings.Contains(reviewRange, "...") {
		sep = "..."
	}
	for _, endpoint := range strings.SplitN(reviewRange, sep, 2) {
		if endpoint == "" {
			continue
		}
		if _, err := gitOutput(ctx, workingDir, "rev-parse", "--verify", endpoint); err != nil {
			return "", fmt.Errorf("review: invalid range endpoint %q: %w", endpoint, err)
		}
	}
	return reviewRange, nil
}

func buildReviewPrompt(ctx context.Context, workingDir, focus, reviewRange string) string {
	var b strings.Builder
	b.WriteString("Review the following code or changes and report findings ordered by severity. For each issue, include a file:line reference if applicable. Do not make any edits; this is a read-only review.\n\n")
	if focus != "" {
		b.WriteString("Focus area: " + focus + "\n\n")
	}
	if reviewRange != "" {
		b.WriteString("Review the changes in range " + reviewRange + ".\n\n")
		if log, err := gitOutput(ctx, workingDir, "log", "--oneline", reviewRange); err == nil && log != "" {
			b.WriteString("Commits:\n" + strutil.Truncate(log, 20000, true) + "\n\n")
		}
		if stat, err := gitOutput(ctx, workingDir, "diff", "--stat", reviewRange); err == nil && stat != "" {
			b.WriteString("Diff stat:\n" + strutil.Truncate(stat, 20000, true) + "\n\n")
		}
		if diff, err := gitOutput(ctx, workingDir, "diff", reviewRange); err == nil && diff != "" {
			b.WriteString("Diff:\n" + strutil.Truncate(diff, 100000, true) + "\n\n")
		}
		if status, err := gitOutput(ctx, workingDir, "status", "--porcelain"); err == nil && status != "" {
			b.WriteString("Working tree also has uncommitted changes:\n" + status + "\n\n")
			if diff, err := gitOutput(ctx, workingDir, "diff", "HEAD"); err == nil && diff != "" {
				b.WriteString("Working-tree diff:\n" + strutil.Truncate(diff, 100000, true) + "\n\n")
			}
		}
	} else {
		b.WriteString("Review the working-tree changes (uncommitted modifications) in this repository.\n\n")
		if status, err := gitOutput(ctx, workingDir, "status", "--porcelain"); err == nil && status != "" {
			b.WriteString("Git status:\n" + status + "\n\n")
		}
		base := defaultBranch(ctx, workingDir)
		if diff, err := gitOutput(ctx, workingDir, "diff", base); err == nil && diff != "" {
			b.WriteString("Diff against " + base + ":\n" + diff + "\n\n")
		} else if diff, err := gitOutput(ctx, workingDir, "diff", "HEAD"); err == nil && diff != "" {
			b.WriteString("Diff against HEAD:\n" + diff + "\n\n")
		}
	}
	b.WriteString("Conclude with a short summary of the most important action items.")
	return b.String()
}

func gitOutput(ctx context.Context, workingDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func defaultBranch(ctx context.Context, workingDir string) string {
	if out, err := gitOutput(ctx, workingDir, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		out = strings.TrimSpace(out)
		if strings.HasPrefix(out, "refs/remotes/origin/") {
			return strings.TrimPrefix(out, "refs/remotes/origin/")
		}
		return out
	}
	return "HEAD"
}
