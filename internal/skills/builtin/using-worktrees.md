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
- A refusal is content, not a malfunction; it says what blocked the merge and
  what to do next. Untracked files in the project checkout do not block it.
  Uncommitted changes to tracked files do, and so does an untracked file the
  merge would overwrite: those are the user's files, so do not stash, reset,
  move, or clean them to get past the guard, and do not merge with shell git
  instead. Name the files and let the user decide. If the checkout only has
  new commits, call it again with `"target": "HEAD"` to merge on top of them.
- Never delete a worktree or branch by hand. `workspace.finish` removes them
  after a successful merge; an unmerged branch is the user's to keep or drop.

## Delegated work: who owns which checkout

`agent.run` behaves the same way, per child, and the parent does the isolating.

- **A child is isolated by default.** `agent.run` with no `workspace` argument
  gives that child its own worktree and branch (`marshal/subagent/<task key>`),
  created from your **committed** HEAD. Uncommitted work in your checkout is not
  visible to it. So finish and commit whatever the child needs — a new
  interface, a plan, a fixture — **before** you dispatch, and leave unrelated
  user changes out of that commit. If an essential change cannot safely be
  committed, settle that before dispatching dependent work.
- **An isolated child commits its own scoped work.** It works in its own branch,
  commits there, and reports the task key, branch, starting commit, candidate
  commit and captured target. That branch is a retained candidate: your checkout
  is unchanged, and nothing is merged for you.
- **A shared child never commits.** `"workspace": "shared"` binds the child to
  your checkout exactly as it is, so its edits land in your working tree and its
  commits are yours to make. Choose it only for a small task whose files and
  build state are genuinely suitable to share; a shared child cannot give you an
  isolated candidate.
- **Integration is explicit.** Bring a retained branch in yourself with git —
  `shell.run git merge <branch>` (or `git cherry-pick <candidate>`) once you have
  reviewed its candidate and its report. Check the report's branch and candidate
  commit before you merge; do not assume the child's work is in your tree. Until
  integration tooling exists, git is the only integration path, and the retained
  branch stays on disk for you to keep or drop.
