package sdd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Merge fast-forwards the pipeline branch from the task's worktree branch
// (spec §12). Pre-checks BranchBaseGuard; on success calls git.MergeFF,
// then marks the task merged in both RepoState (MarkMerged) and DAG
// (SetTaskStatus), and updates the task's ReviewedHead to the new pipeline
// HEAD. The caller is responsible for having the pipeline branch checked out
// in git.Dir (per the P1 MergeFF contract).
func Merge(git GitOps, ws *Workspace, progress *Progress, dag *DAG, rs *RepoState, taskID string) MergeResult {
	task, ok := dag.TaskByID(taskID)
	if !ok {
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "unknown task"}
	}
	if task.Branch == "" {
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "task has no branch"}
	}
	// Pre-check: pipeline branch must contain the target HEAD.
	guard := BranchBaseGuard(git, rs)
	if !guard.Pass {
		_ = progress.Append(ws, taskID, "BLOCKED", "reason", "wrong_base", "detail", guard.Reason)
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "wrong base: " + guard.Reason}
	}
	// Merge the task branch into the currently-checked-out branch (pipeline).
	if err := git.MergeFF(task.Branch); err != nil {
		_ = progress.Append(ws, taskID, "BLOCKED", "reason", "merge_conflict", "detail", err.Error())
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "merge conflict: " + err.Error()}
	}
	// Update state.
	if err := rs.MarkMerged(taskID); err != nil {
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "mark merged: " + err.Error()}
	}
	if err := dag.SetTaskStatus(taskID, TaskMerged); err != nil {
		return MergeResult{Merged: false, TaskID: taskID, Event: "BLOCKED", Reason: "set status: " + err.Error()}
	}
	// Update ReviewedHead to the new pipeline HEAD.
	newHead, err := git.RevParse(rs.Branch)
	if err == nil {
		for i := range dag.Tasks {
			if dag.Tasks[i].ID == taskID {
				dag.Tasks[i].ReviewedHead = newHead
				break
			}
		}
	}
	_ = progress.Append(ws, taskID, "MERGED", "branch", task.Branch)
	return MergeResult{Merged: true, TaskID: taskID, Event: "MERGED"}
}

// FinalizeConfirm fast-forwards the target branch from the pipeline HEAD
// (spec §12). Pre-checks branch-base-guard. On success the target branch
// now points at the pipeline HEAD; the human's final merge is complete.
func FinalizeConfirm(git GitOps, rs *RepoState) GuardResult {
	guard := BranchBaseGuard(git, rs)
	if !guard.Pass {
		return guard
	}
	// The caller (P4 controller) is responsible for checking out the target
	// branch before calling; MergeFF advances the current checkout.
	pipelineHead, err := git.RevParse(rs.Branch)
	if err != nil {
		return GuardResult{Pass: false, Event: "FINALIZE_FAIL", Reason: "rev-parse pipeline: " + err.Error()}
	}
	if err := git.MergeFF(rs.Branch); err != nil {
		return GuardResult{Pass: false, Event: "FINALIZE_FAIL", Reason: "merge pipeline into target: " + err.Error()}
	}
	return GuardResult{Pass: true, Event: "FINALIZE_OK", Reason: "target fast-forwarded to " + pipelineHead}
}

// CheckpointCreate writes a checkpoint to <ws.Dir()>/checkpoints/<shortSHA>.json.
// shortSHA is cp.SHA truncated to 8 chars (or full if shorter).
func CheckpointCreate(ws *Workspace, cp Checkpoint) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	short := cp.SHA
	if len(short) > 8 {
		short = short[:8]
	}
	path := filepath.Join(ws.Dir(), "checkpoints", short+".json")
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("sdd checkpoint: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("sdd checkpoint: write: %w", err)
	}
	return nil
}

// BranchRebranch moves the pipeline branch to newBranch (spec §12 "Branch
// correction"). Never wipes state: only updates rs.Branch and rs.Head. The
// new branch must exist in git (the human creates it first).
func BranchRebranch(git GitOps, rs *RepoState, newBranch string) error {
	if !git.BranchExists(newBranch) {
		return fmt.Errorf("sdd rebranch: branch %q does not exist", newBranch)
	}
	head, err := git.RevParse(newBranch)
	if err != nil {
		return fmt.Errorf("sdd rebranch: rev-parse %s: %w", newBranch, err)
	}
	rs.Branch = newBranch
	rs.Head = head
	return nil
}

// nowISO returns the current UTC time in RFC3339. Used by callers building
// Checkpoint structs (kept here so checkpoint creation has a consistent
// timestamp helper).
func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
