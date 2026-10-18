---
name: using-worktrees
description: Isolate speculative, long-running, or wide-ranging work in a git worktree via workspace.worktree instead of editing the main checkout or copying the repository, and merge it back with workspace.finish. Not for single-file fixes or read-only questions.
risk: workspace_write
---

# Using Worktrees

`workspace.worktree` isolates the session in a git worktree; `workspace.finish`
merges it back. The tools cover the mechanics; this skill is about judgment.

## When to isolate

- The work is speculative (you are not yet sure what the right change is),
  long-running (you will iterate over multiple turns), or touches many files
  (the diff would be hard to review in one shot).
- The work may need to be discarded without polluting the main checkout's
  history. In a worktree, discarding is just leaving the branch unmerged.

## When NOT to isolate

- A single-file fix or a read-only question. The rebind costs more than it
  saves.

## Entering and leaving

- Never copy a repository. `cp -r` produces a directory with no branch, no
  history relationship, and no way to merge back.
- Call `workspace.worktree` with `{"branch": "<name>"}` to enter a worktree.
  Pick a branch name that does not exist yet; the tool refuses an existing
  branch that has no worktree.
- Inside the worktree, paths resolve from the worktree root. Do not prefix
  them with the worktree's directory.
- Call `workspace.worktree` with `{}` to return to the project root without
  merging. Commit first: uncommitted worktree changes stay behind in the
  worktree.
- The semantic index stays pinned to the project root, so code search over
  worktree-only changes may be stale until the branch is merged back.

## Finishing

Call `workspace.finish` only when the work is complete and verified, and the
user wants it integrated — they asked you to merge, or the workflow's last step
is integration. Otherwise return to the root with `{}` and leave the branch for
the user to review.

- Merge with `workspace.finish`, never with `shell.run git merge`. It squashes
  by default; pass `"squash": false` for a `--no-ff` merge. Commit worktree
  changes first, or pass `message` to commit them.
- Pop or drop any stash you created before calling it.
- A refusal is content, not a malfunction. If the project checkout is dirty or
  its HEAD moved, do not stash, reset, or clean the user's checkout to get past
  the guard. Report the refusal and let the user decide.
- Never delete a worktree or branch by hand. `workspace.finish` removes them
  after a successful merge; an unmerged branch is the user's to keep or drop.
