package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// WorktreeRuntime is the per-session state the worktree handlers need.
type WorktreeRuntime struct {
	State       *session.State
	ProjectRoot string
}

// WorktreeManagerConfig wires the manager to the session registry and git.
type WorktreeManagerConfig struct {
	Lookup func(sessionID string) (*WorktreeRuntime, bool)
	// Git defaults to CLIGitOps when nil. Tests inject FakeGitOps.
	Git worktree.GitOps
}

// WorktreeManager serves the isolation-related ACP methods.
type WorktreeManager struct {
	lookup func(string) (*WorktreeRuntime, bool)
	git    worktree.GitOps
}

func NewWorktreeManager(cfg WorktreeManagerConfig) *WorktreeManager {
	git := cfg.Git
	if git == nil {
		git = worktree.CLIGitOps{}
	}
	return &WorktreeManager{lookup: cfg.Lookup, git: git}
}

// DiffFile is one changed file's stat line.
type DiffFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// DiffResult is the session/diff result.
type DiffResult struct {
	Files []DiffFile `json:"files"`
	Diff  string     `json:"diff,omitempty"`
}

type diffParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path,omitempty"`
}

// isolated resolves a session and requires it to be in a worktree.
func (w *WorktreeManager) isolated(sessionID string) (*WorktreeRuntime, session.Workspace, error) {
	if w.lookup == nil {
		return nil, session.Workspace{}, serverErrorf("worktree manager has no session lookup configured")
	}
	rt, ok := w.lookup(sessionID)
	if !ok || rt == nil || rt.State == nil {
		return nil, session.Workspace{}, serverErrorf("unknown session %q", sessionID)
	}
	ws := rt.State.Workspace()
	if ws.Branch == "" || ws.ActiveRoot == ws.ProjectRoot {
		return nil, session.Workspace{}, serverErrorf("session %q is not isolated in a worktree", sessionID)
	}
	return rt, ws, nil
}

// parseNumstat turns `git diff --numstat` output into DiffFiles. Binary
// files report "-" for both counts and are kept with zero counts rather
// than dropped — the operator still needs to see that they changed.
func parseNumstat(out string) []DiffFile {
	var files []DiffFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0]) // "-" for binary → 0
		del, _ := strconv.Atoi(parts[1])
		files = append(files, DiffFile{Path: parts[2], Added: add, Removed: del})
	}
	return files
}

// Diff handles session/diff.
//
// The range is the recorded base SHA with no right-hand side, which git reads
// as "base versus the working tree" — so it covers both work committed on the
// branch and changes still uncommitted, which is what the operator is judging.
//
// The base comes from the workspace, not from a merge-base call: inside a
// worktree HEAD *is* the branch tip, so MergeBase(worktree, "HEAD", branch)
// returns the tip and the diff would always be empty.
func (w *WorktreeManager) Diff(_ context.Context, params json.RawMessage) (any, error) {
	var p diffParams
	if err := decodeParams(params, &p, "session/diff"); err != nil {
		return nil, err
	}
	_, ws, err := w.isolated(p.SessionID)
	if err != nil {
		return nil, err
	}
	rng := ws.BaseSha
	if rng == "" {
		// A resumed session has no in-memory base; recover it from the
		// project side, where the branch and its target still differ.
		base, berr := w.git.MergeBase(ws.ProjectRoot, ws.Branch, "HEAD")
		if berr != nil {
			return nil, serverErrorf("resolve diff base: %v", berr)
		}
		rng = base
	}

	stat, err := w.git.DiffNumstat(ws.ActiveRoot, rng)
	if err != nil {
		return nil, serverErrorf("diff stat: %v", err)
	}
	res := DiffResult{Files: parseNumstat(stat)}
	if p.Path != "" {
		d, derr := w.git.DiffPath(ws.ActiveRoot, rng, p.Path, 3)
		if derr != nil {
			return nil, serverErrorf("diff %s: %v", p.Path, derr)
		}
		res.Diff = d
	}
	return res, nil
}
