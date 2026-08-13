package acp

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// IsolationParams is the optional `isolation` object on session/new.
type IsolationParams struct {
	Branch  string `json:"branch,omitempty"`
	BaseRef string `json:"baseRef,omitempty"`
}

// WorkspaceInfo describes an isolated session's workspace. Returned on
// session/new when isolation was requested; omitted otherwise.
type WorkspaceInfo struct {
	ActiveRoot string `json:"activeRoot"`
	Branch     string `json:"branch"`
	BaseSha    string `json:"baseSha"`
	// TargetBranch is the project's branch at isolation time — the branch a
	// later merge targets. It cannot be derived later: EnsureWorktree returns
	// a SHA, and merging into a SHA is meaningless, while merging into
	// whatever the project is on at merge time would silently pick the wrong
	// branch.
	TargetBranch string `json:"targetBranch"`
}

// slugifyBranch turns a display name into a branch-safe slug.
func slugifyBranch(s string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}

// isolateSession creates the session's worktree and moves the session's
// active root into it. It performs exactly the two steps the
// workspace.worktree agent tool performs, so there is one mechanism with
// three entry points (agent tool, TUI, ACP).
func isolateSession(git worktree.GitOps, st *session.State, projectRoot string, p IsolationParams, fallbackName string) (WorkspaceInfo, error) {
	branch := strings.TrimSpace(p.Branch)
	if branch == "" {
		branch = "marshal/" + slugifyBranch(fallbackName)
	}
	baseRef := strings.TrimSpace(p.BaseRef)
	if baseRef == "" {
		baseRef = "HEAD"
	}

	// Capture the merge target BEFORE creating the worktree: this is the
	// project's current branch, and it is the only chance to record it.
	target, err := git.RevParse(projectRoot, "--abbrev-ref HEAD")
	if err != nil {
		return WorkspaceInfo{}, fmt.Errorf("resolve project branch: %w", err)
	}

	dir, err := worktree.AgentDir(projectRoot)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	wt, err := worktree.EnsureWorktree(git, projectRoot, dir, branch, baseRef)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	st.SetWorkspace(session.Workspace{
		ProjectRoot: projectRoot, ActiveRoot: wt.Path, Branch: wt.Branch, BaseSha: wt.Base,
	})
	return WorkspaceInfo{
		ActiveRoot:   wt.Path,
		Branch:       wt.Branch,
		BaseSha:      wt.Base,
		TargetBranch: strings.TrimSpace(target),
	}, nil
}
