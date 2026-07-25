package sdd

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree owns the per-task git worktree lifecycle (spec §11). The zero
// value is not useful; construct via NewWorktree.
type Worktree struct {
	WS  *Workspace
	DAG *DAG
	Git GitOps
}

// NewWorktree returns a Worktree bound to the given workspace, DAG, and
// GitOps. The DAG and GitOps are required (Create / CleanupStale read from
// both).
func NewWorktree(ws *Workspace, dag *DAG, git GitOps) *Worktree {
	return &Worktree{WS: ws, DAG: dag, Git: git}
}

// Create just-in-time creates a worktree for the named task per spec §11:
//   - if the task has a recorded Branch + WorktreePath AND the branch
//     already exists in git, treat as idempotent resume (return existing
//     path; do not call WorktreeAdd)
//   - otherwise resolve the pipeline HEAD via GitOps.RevParse and create
//     `sdd/<taskID>` rooted at that HEAD; update DAGTask.{Base,Branch,WorktreePath}
//
// pipelineBranch is the name of the pipeline branch (e.g. "sdd/feature")
// that the worktree branches are created from. The caller (P4 controller)
// reads it from RepoState.Branch.
func (w *Worktree) Create(taskID string, pipelineBranch string) (string, error) {
	t, ok := w.DAG.TaskByID(taskID)
	if !ok {
		return "", fmt.Errorf("sdd worktree: unknown task %q", taskID)
	}
	// Idempotent resume: recorded branch exists, recorded path is non-empty.
	if t.Branch != "" && t.WorktreePath != "" && w.Git.BranchExists(t.Branch) {
		return t.WorktreePath, nil
	}
	head, err := w.Git.RevParse(pipelineBranch)
	if err != nil {
		return "", fmt.Errorf("sdd worktree: rev-parse pipeline branch %s: %w", pipelineBranch, err)
	}
	newBranch := "sdd/" + taskID
	path := filepath.Join(w.WS.WorktreesDir(), taskID)
	if err := w.Git.WorktreeAdd(path, newBranch, head); err != nil {
		return "", fmt.Errorf("sdd worktree: WorktreeAdd %s on %s: %w", path, newBranch, err)
	}
	// Mutate the task in the DAG. Status is unchanged; this method only
	// manages worktree metadata. Direct field set is allowed here (per
	// the global constraint: only Status has a chokepoint mutator).
	for i := range w.DAG.Tasks {
		if w.DAG.Tasks[i].ID == taskID {
			w.DAG.Tasks[i].Base = head
			w.DAG.Tasks[i].Branch = newBranch
			w.DAG.Tasks[i].WorktreePath = path
			break
		}
	}
	return path, nil
}

// CreateForce recreates the worktree for the named task, intentionally
// destroying the previous branch's commits. Pre-flight check is simplified
// in P2: the real "no commits beyond Base" guard is P3's branch-base-guard
// (it has the merge-context). For P2 we trust the caller (the controller in
// P3/P4) to have already validated the precondition.
//
// pipelineBranch is the name of the pipeline branch (e.g. "sdd/feature")
// that the worktree branches are created from. The caller (P4 controller)
// reads it from RepoState.Branch.
func (w *Worktree) CreateForce(taskID string, pipelineBranch string) (string, error) {
	t, ok := w.DAG.TaskByID(taskID)
	if !ok {
		return "", fmt.Errorf("sdd worktree: unknown task %q", taskID)
	}
	if t.WorktreePath != "" {
		_ = w.Git.WorktreeRemove(t.WorktreePath)
	}
	branch := "sdd/" + taskID
	head, err := w.Git.RevParse(pipelineBranch)
	if err != nil {
		return "", fmt.Errorf("sdd worktree: rev-parse pipeline branch %s: %w", pipelineBranch, err)
	}
	path := filepath.Join(w.WS.WorktreesDir(), taskID)
	if err := w.Git.WorktreeAdd(path, branch, head); err != nil {
		return "", fmt.Errorf("sdd worktree: WorktreeAdd (force): %w", err)
	}
	for i := range w.DAG.Tasks {
		if w.DAG.Tasks[i].ID == taskID {
			w.DAG.Tasks[i].Base = head
			w.DAG.Tasks[i].Branch = branch
			w.DAG.Tasks[i].WorktreePath = path
			break
		}
	}
	return path, nil
}

// CleanupStale removes sdd/* branches and worktrees that do not correspond
// to any task in the current DAG (spec §11). Uses GitOps.ListSDDBranches +
// GitOps.DeleteBranch (no shell-out). Best-effort: tolerates errors.
func (w *Worktree) CleanupStale() error {
	taskIDs := make(map[string]bool, len(w.DAG.Tasks))
	for _, t := range w.DAG.Tasks {
		taskIDs[t.ID] = true
	}
	branches, err := w.Git.ListSDDBranches()
	if err != nil {
		// No sdd/* branches, or git not in PATH. Tolerate.
		return nil
	}
	for _, line := range branches {
		id := strings.TrimPrefix(line, "sdd/")
		if id == line || id == "" {
			continue
		}
		if taskIDs[id] {
			continue
		}
		// Stale: remove worktree (tolerate "not a worktree") and delete branch.
		wtPath := filepath.Join(w.WS.WorktreesDir(), id)
		_ = w.Git.WorktreeRemove(wtPath)
		_ = w.Git.DeleteBranch(line)
	}
	return nil
}
