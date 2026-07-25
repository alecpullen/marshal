package sdd

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
