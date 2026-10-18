package native

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// workspaceFinishArgs are workspace.finish's arguments.
type workspaceFinishArgs struct {
	Message string `json:"message"`
	Squash  *bool  `json:"squash"`
	Target  string `json:"target"`
}

// workspaceFinishTool merges the session's worktree branch into the base
// branch, then removes the worktree and deletes the branch. It mirrors
// workspaceWorktreeTool: mechanics live here, judgment lives in the
// using-worktrees skill. A refusal is content, not an error — the agent
// must read it and act.
func (t *toolSet) workspaceFinishTool() registry.Tool {
	tool := registry.Tool{
		Name: "workspace.finish",
		Description: "Merge the session's worktree branch into the base branch (squash by default, squash=false for --no-ff), then remove the worktree and delete the branch. " +
			"Use this instead of shell.run git merge, and pop or drop any stash you created before calling it. " +
			"Guards: refuses on uncommitted tracked changes in the project (untracked files are fine) or a moved HEAD. Commit worktree changes first, or pass message.",
		Schema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"},"squash":{"type":"boolean"},"target":{"type":"string"}},"additionalProperties":false}`),
		Risk:   registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[workspaceFinishArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		st := t.wsState()
		if st == nil {
			return registry.ToolResult{}, fmt.Errorf("workspace.finish requires a session")
		}
		ws := st.Workspace()

		// The tool finishes the session's own worktree. At the project root
		// there is nothing to finish.
		if ws.ActiveRoot == ws.ProjectRoot {
			return registry.ToolResult{}, fmt.Errorf("session is not in a worktree")
		}
		// Only worktrees the agent created may be finished: finishing runs
		// WorktreeRemove, and a hand-made worktree elsewhere on disk must
		// never be deletable through this tool.
		if !worktree.OwnedByAgentDir(ws.ProjectRoot, ws.ActiveRoot) {
			return registry.ToolResult{}, fmt.Errorf("worktree %s is not owned by the agent (must live under %s)", ws.ActiveRoot, filepath.Join(ws.ProjectRoot, ".marshal", "worktrees"))
		}

		// The target-moved guard compares the project's current HEAD SHA
		// against target, so target must be a SHA. The recorded BaseSha is
		// already one (captured at isolation time); a caller-supplied target
		// may be a branch name, so resolve it to a SHA first.
		target := ws.BaseSha
		if args.Target != "" {
			target, err = t.gitOps.RevParse(ws.ProjectRoot, args.Target)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("resolve target %q: %w", args.Target, err)
			}
		}
		if target == "" {
			return registry.ToolResult{}, fmt.Errorf("no merge target recorded for the session's worktree; pass target")
		}

		squash := true
		if args.Squash != nil {
			squash = *args.Squash
		}
		res, err := worktree.FinishBranch(t.gitOps, ws.ProjectRoot, target, worktree.Worktree{Path: ws.ActiveRoot, Branch: ws.Branch}, worktree.FinishOptions{
			Squash:        squash,
			CommitMessage: args.Message,
			DeleteBranch:  true,
		})
		if err != nil {
			return registry.ToolResult{}, err
		}
		if !res.Merged {
			// A refusal is content, not an error: the agent must read the
			// reason and decide what to do next.
			return registry.ToolResult{
				Summary: fmt.Sprintf("refused: %s", res.Reason),
				Content: finishRefusalText(res),
			}, nil
		}

		// On success the session returns to the project root; the worktree
		// and branch are gone.
		st.SetWorkspace(session.Workspace{ProjectRoot: ws.ProjectRoot, ActiveRoot: ws.ProjectRoot})
		kind := "merge commit (--no-ff)"
		if res.SquashCommit {
			kind = "squash commit"
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("merged %s into the target", ws.Branch),
			Content: fmt.Sprintf("Merged branch %q into the target as a %s %s. The worktree %s was removed and branch %q deleted; the session root moved back to the project root %s.",
				ws.Branch, kind, res.Commit, ws.ActiveRoot, ws.Branch, ws.ProjectRoot),
		}, nil
	}
	return tool
}

// finishRefusalText tells the agent what blocked the merge and what to do
// next. A bare reason token left agents improvising workarounds (often a
// shell merge into the same checkout) instead of taking the supported next
// step.
func finishRefusalText(res worktree.FinishResult) string {
	head := fmt.Sprintf("Refused to finish (%s). Nothing was merged and no repository was changed.", res.Reason)
	paths := strings.Join(res.Blocking, ", ")
	switch res.Reason {
	case worktree.ReasonDirtyWorktree:
		return head + ` The worktree has uncommitted changes. Commit them in the worktree, or call workspace.finish again with "message" to commit them as part of finishing.`
	case worktree.ReasonDirtyProject:
		// Blocking holds tracked files with uncommitted changes or untracked
		// files the merge would overwrite; either way they are the user's.
		return head + " The project checkout has files in the way: " + paths + ". They are the user's: do not commit, stash, discard, move, or delete them, and do not merge with shell git instead. Tell the user which files block the merge and ask them to commit, stash, or move them; then call workspace.finish again."
	case worktree.ReasonTargetMoved:
		return head + ` The project checkout has new commits since this worktree was created. If merging on top of them is intended, call workspace.finish again with "target": "HEAD"; the merge still refuses on conflicts.`
	case worktree.ReasonConflicts:
		if len(res.Conflicted) > 0 {
			return head + " The branch conflicts with the target in: " + strings.Join(res.Conflicted, ", ") + ". The merge was undone. Resolve the conflicts in the worktree (merge or rebase the target into the branch there), commit, and call workspace.finish again."
		}
		return head + " The branch does not merge cleanly into the target, and the merge was undone. Merge or rebase the target into the branch in the worktree, resolve the result, commit, and call workspace.finish again."
	default:
		return head
	}
}
