package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	// KnownWorktrees returns the worktrees a project's live sessions claim.
	// Used by Prune to distinguish orphans from in-use worktrees.
	KnownWorktrees func(projectRoot string) []string
}

// WorktreeManager serves the isolation-related ACP methods.
type WorktreeManager struct {
	lookup func(string) (*WorktreeRuntime, bool)
	git    worktree.GitOps
	known  func(string) []string
}

func NewWorktreeManager(cfg WorktreeManagerConfig) *WorktreeManager {
	git := cfg.Git
	if git == nil {
		git = worktree.CLIGitOps{}
	}
	return &WorktreeManager{lookup: cfg.Lookup, git: git, known: cfg.KnownWorktrees}
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

// Refusal reasons returned by session/merge. The bridge maps any non-empty
// reason to HTTP 409 so a client can branch on a code, not on prose.
const (
	ReasonDirty        = "dirty"
	ReasonProjectDirty = "project_dirty"
	ReasonTargetMoved  = "target_moved"
	ReasonConflicts    = "conflicts"
)

// MergeResult is the session/merge result. Merged is false for every
// refusal, with Reason naming which.
type MergeResult struct {
	Merged    bool     `json:"merged"`
	Branch    string   `json:"branch"`
	Target    string   `json:"target"`
	Reason    string   `json:"reason,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

type mergeParams struct {
	SessionID     string `json:"sessionId"`
	TargetBranch  string `json:"targetBranch"`
	CommitMessage string `json:"commitMessage,omitempty"`
}

// conflictFiles pulls file names out of git's CONFLICT lines. Best-effort:
// the reason is what drives the UI, the list is detail.
func conflictFiles(msg string) []string {
	var out []string
	for _, line := range strings.Split(msg, "\n") {
		if !strings.Contains(line, "CONFLICT") {
			continue
		}
		if i := strings.LastIndex(line, " in "); i >= 0 {
			if f := strings.TrimSpace(line[i+4:]); f != "" {
				out = append(out, f)
			}
		}
	}
	return out
}

// Merge handles session/merge: merge the agent's branch into the branch the
// project was on when the session was isolated, then clean up. It refuses
// rather than improvising — see the four Reason constants.
func (w *WorktreeManager) Merge(_ context.Context, params json.RawMessage) (any, error) {
	var p mergeParams
	if err := decodeParams(params, &p, "session/merge"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.TargetBranch) == "" {
		return nil, invalidParamsError("targetBranch is required")
	}
	rt, ws, err := w.isolated(p.SessionID)
	if err != nil {
		return nil, err
	}
	res := MergeResult{Branch: ws.Branch, Target: p.TargetBranch}

	// 1. The worktree must be committed, or we must be told to commit it.
	dirty, err := w.git.IsDirty(ws.ActiveRoot)
	if err != nil {
		return nil, serverErrorf("check worktree: %v", err)
	}
	if dirty {
		if p.CommitMessage == "" {
			res.Reason = ReasonDirty
			return res, nil
		}
		if _, cerr := w.git.CommitAll(ws.ActiveRoot, p.CommitMessage); cerr != nil {
			return nil, serverErrorf("commit worktree: %v", cerr)
		}
	}

	// 2. Never merge into a dirty project checkout.
	projectDirty, err := w.git.IsDirty(rt.ProjectRoot)
	if err != nil {
		return nil, serverErrorf("check project: %v", err)
	}
	if projectDirty {
		res.Reason = ReasonProjectDirty
		return res, nil
	}

	// 3. The project must still be on the branch we recorded at isolation.
	current, err := w.git.RevParse(rt.ProjectRoot, "--abbrev-ref HEAD")
	if err != nil {
		return nil, serverErrorf("resolve project branch: %v", err)
	}
	if strings.TrimSpace(current) != p.TargetBranch {
		res.Reason = ReasonTargetMoved
		return res, nil
	}

	// 4. Attempt the merge. On failure abort before returning, so a refused
	// merge never leaves the project mid-merge.
	if merr := w.git.Merge(rt.ProjectRoot, ws.Branch); merr != nil {
		_ = w.git.MergeAbort(rt.ProjectRoot)
		res.Reason = ReasonConflicts
		res.Conflicts = conflictFiles(merr.Error())
		return res, nil
	}

	// Success: drop the worktree, delete the now-merged branch, and return
	// the session to the project root. Its history stays in the project DB.
	if rerr := w.git.WorktreeRemove(rt.ProjectRoot, ws.ActiveRoot); rerr != nil {
		return nil, serverErrorf("merged, but removing the worktree failed: %v", rerr)
	}
	if derr := w.git.BranchDelete(rt.ProjectRoot, ws.Branch, false); derr != nil {
		return nil, serverErrorf("merged, but deleting branch %s failed: %v", ws.Branch, derr)
	}
	rt.State.SetWorkspace(session.Workspace{ProjectRoot: rt.ProjectRoot, ActiveRoot: rt.ProjectRoot})
	res.Merged = true
	return res, nil
}

type discardParams struct {
	SessionID string `json:"sessionId"`
}

// Discard handles session/discard: remove the worktree and delete the
// branch. The branch is unmerged by definition, so the delete is forced.
// The explicit RPC call is the confirmation; the UI gates it behind one.
func (w *WorktreeManager) Discard(_ context.Context, params json.RawMessage) (any, error) {
	var p discardParams
	if err := decodeParams(params, &p, "session/discard"); err != nil {
		return nil, err
	}
	rt, ws, err := w.isolated(p.SessionID)
	if err != nil {
		return nil, err
	}
	if rerr := w.git.WorktreeRemove(rt.ProjectRoot, ws.ActiveRoot); rerr != nil {
		return nil, serverErrorf("remove worktree %s: %v", ws.ActiveRoot, rerr)
	}
	if derr := w.git.BranchDelete(rt.ProjectRoot, ws.Branch, true); derr != nil {
		return nil, serverErrorf("delete branch %s: %v", ws.Branch, derr)
	}
	rt.State.SetWorkspace(session.Workspace{ProjectRoot: rt.ProjectRoot, ActiveRoot: rt.ProjectRoot})
	return struct{}{}, nil
}

// PruneResult is the session/worktree_prune result. Pruned lists git's own
// stale administrative entries removed; Unknown lists live worktrees that
// no known agent claims.
type PruneResult struct {
	Pruned  []string `json:"pruned"`
	Unknown []string `json:"unknown"`
}

type pruneParams struct {
	Cwd string `json:"cwd"`
}

// Prune handles session/worktree_prune. cwd-scoped, not session-scoped: the
// bridge calls it at startup when a project may have no live session.
//
// It never removes an unknown worktree — that is potentially someone's
// uncommitted work. It only runs `git worktree prune`, which clears git's
// stale metadata, and reports what it could not account for.
func (w *WorktreeManager) Prune(_ context.Context, params json.RawMessage) (any, error) {
	var p pruneParams
	if err := decodeParams(params, &p, "session/worktree_prune"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Cwd) == "" {
		return nil, invalidParamsError("cwd is required")
	}
	if !filepath.IsAbs(p.Cwd) {
		return nil, invalidParamsError("cwd must be an absolute path")
	}
	if err := validateWorkingPaths(p.Cwd, nil); err != nil {
		return nil, invalidParamsError("%v", err)
	}

	before, err := w.git.WorktreeList(p.Cwd)
	if err != nil {
		return nil, serverErrorf("list worktrees: %v", err)
	}
	if perr := w.git.WorktreePrune(p.Cwd); perr != nil {
		return nil, serverErrorf("prune worktrees: %v", perr)
	}
	after, err := w.git.WorktreeList(p.Cwd)
	if err != nil {
		return nil, serverErrorf("list worktrees after prune: %v", err)
	}

	stillThere := make(map[string]bool, len(after))
	for _, p2 := range after {
		stillThere[p2] = true
	}
	res := PruneResult{Pruned: []string{}, Unknown: []string{}}
	for _, p2 := range before {
		if !stillThere[p2] {
			res.Pruned = append(res.Pruned, p2)
		}
	}

	claimed := map[string]bool{}
	if w.known != nil {
		for _, p2 := range w.known(p.Cwd) {
			claimed[p2] = true
		}
	}
	for _, p2 := range after {
		if !claimed[p2] {
			res.Unknown = append(res.Unknown, p2)
		}
	}
	return res, nil
}
