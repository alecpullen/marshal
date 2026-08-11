package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
)

// runReviewSubagent dispatches a read-only reviewer subagent over the
// current working-tree changes, or over the user-supplied focus if non-empty.
// The review prompt is constructed here in the app layer, not in the TUI.
func runReviewSubagent(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, focus, model string) error {
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

	prompt := buildReviewPrompt(state.WorkingDir, focus)
	task, err := child.RunTask(ctx, prompt)
	var summary string
	if task != nil {
		summary = task.Summary
	}
	state.FinishSubagent(view.ID, summary, err)
	if err != nil {
		return fmt.Errorf("review: subagent run failed: %w", err)
	}
	if summary != "" {
		state.AddMessage(session.RoleAssistant, summary, session.ContentTypePlain)
	}
	return nil
}

func buildReviewPrompt(workingDir, focus string) string {
	var b strings.Builder
	b.WriteString("Review the following code or changes and report findings ordered by severity. " +
		"For each issue, include a file:line reference if applicable. Do not make any edits; this is a read-only review.\n\n")

	if focus != "" {
		b.WriteString("Focus area: ")
		b.WriteString(focus)
		b.WriteString("\n\n")
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
