package worktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FinishOptions controls FinishBranch.
type FinishOptions struct {
	// Squash squashes the branch into one staged change and commits it on
	// the target (git merge --squash + commit) instead of a --no-ff merge
	// commit.
	Squash bool
	// CommitMessage commits a dirty worktree when set, and is the squash
	// commit's message. Empty means "refuse a dirty worktree" and, for a
	// squash, the default "squash: <branch>" message.
	CommitMessage string
	// DeleteBranch deletes the branch after a successful merge. A squash
	// merge looks unmerged to git, so the delete is forced; a --no-ff
	// merge leaves the branch merged and a plain -d suffices.
	DeleteBranch bool
}

// FinishResult reports what FinishBranch did — or why it refused. A
// refusal is not an error: Merged is false and Reason names the guard
// that fired, using the same strings the ACP session/merge result maps
// back onto (see internal/acp/worktree.go).
type FinishResult struct {
	Merged     bool
	Reason     string
	Conflicted []string
	// Blocking names the project paths behind a ReasonDirtyProject refusal:
	// tracked files with uncommitted changes, or untracked files the merge
	// would have overwritten.
	Blocking     []string
	Commit       string // commit created on the target
	SquashCommit bool
}

// Refusal reasons. The first four must stay byte-identical to the
// constants in internal/acp/worktree.go — the bridge maps a non-empty
// reason onto HTTP 409 and clients branch on the exact string.
const (
	ReasonDirtyWorktree = "dirty"
	ReasonDirtyProject  = "project_dirty"
	ReasonTargetMoved   = "target_moved"
	ReasonConflicts     = "conflicts"
)

// untrackedOverwrites pulls the paths out of git's refusal to merge over
// untracked files ("The following untracked working tree files would be
// overwritten by merge:" followed by one indented path per line). git refuses
// before changing anything, so the project is untouched.
func untrackedOverwrites(msg string) []string {
	var out []string
	listing := false
	for _, line := range strings.Split(msg, "\n") {
		if strings.Contains(line, "untracked working tree files would be") {
			listing = true
			continue
		}
		if !listing {
			continue
		}
		if line == "" || (line[0] != '\t' && line[0] != ' ') {
			listing = false
			continue
		}
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// FinishBranch merges wt.Branch into the target in repoRoot, then removes
// the worktree and deletes the branch (both best-effort). It refuses
// rather than improvising: a dirty worktree without a commit message, a
// dirty project checkout, or a project that has moved off target each
// return a FinishResult with Merged false and the guard's Reason, leaving
// every repository untouched.
//
// target is compared against the project's current HEAD SHA — pass the
// SHA captured when the worktree was created (Worktree.Base for a
// worktree created from the project's HEAD).
func FinishBranch(git GitOps, repoRoot, target string, wt Worktree, opts FinishOptions) (FinishResult, error) {
	// 1. The worktree must be committed, or we must be told to commit it.
	dirty, err := git.IsDirty(wt.Path)
	if err != nil {
		return FinishResult{}, fmt.Errorf("worktree: check worktree: %w", err)
	}
	if dirty {
		if opts.CommitMessage == "" {
			return FinishResult{Reason: ReasonDirtyWorktree}, nil
		}
		if _, cerr := git.CommitAll(wt.Path, opts.CommitMessage); cerr != nil {
			return FinishResult{}, fmt.Errorf("worktree: commit worktree: %w", cerr)
		}
	}

	// 2. Never merge into a project checkout with uncommitted changes to
	// tracked files: they are the operator's work in progress, and a merge
	// (or the undo of a failed one) must not mix with or discard them.
	// Untracked files do not block: no commit below stages them, reset
	// --merge leaves them alone, and git itself refuses a merge that would
	// overwrite one (reported below as ReasonDirtyProject with its paths).
	tracked, err := git.TrackedChanges(repoRoot)
	if err != nil {
		return FinishResult{}, fmt.Errorf("worktree: check project: %w", err)
	}
	if len(tracked) > 0 {
		return FinishResult{Reason: ReasonDirtyProject, Blocking: tracked}, nil
	}

	// 3. The project must still be where it was when the worktree was
	// created. A moved target means the merge would land on top of work
	// the operator has not seen.
	head, err := git.RevParse(repoRoot, "HEAD")
	if err != nil {
		return FinishResult{}, fmt.Errorf("worktree: resolve project HEAD: %w", err)
	}
	if head != target {
		return FinishResult{Reason: ReasonTargetMoved}, nil
	}

	// 4. Attempt the merge. On failure abort before returning, so a
	// refused merge never leaves the project mid-merge.
	if opts.Squash {
		if merr := git.SquashMerge(repoRoot, wt.Branch); merr != nil {
			// merge --abort does not apply here: --squash never writes
			// MERGE_HEAD, so --abort fails and leaves the conflicted index
			// and SQUASH_MSG behind. reset --merge is the correct undo, and
			// is safe because guard 2 verified no tracked file has
			// uncommitted changes; it leaves untracked files alone.
			_ = git.ResetMerge(repoRoot)
			return mergeRefusal(merr), nil
		}
		msg := opts.CommitMessage
		if msg == "" {
			msg = "squash: " + wt.Branch
		}
		// Commit exactly what the squash staged. CommitAll would run
		// `git add -A` in the operator's checkout and sweep their untracked
		// files into the commit.
		if _, cerr := git.CommitIndex(repoRoot, msg); cerr != nil {
			return FinishResult{}, fmt.Errorf("worktree: commit squash: %w", cerr)
		}
	} else if merr := git.Merge(repoRoot, wt.Branch); merr != nil {
		_ = git.MergeAbort(repoRoot)
		return mergeRefusal(merr), nil
	}

	// 5. Cleanup is best-effort: the merge is done, and a failure here
	// leaves a stale worktree or branch the operator can remove by hand.
	_ = git.WorktreeRemove(repoRoot, wt.Path)
	if opts.DeleteBranch {
		_ = git.BranchDelete(repoRoot, wt.Branch, opts.Squash)
	}

	commit, err := git.RevParse(repoRoot, "HEAD")
	if err != nil {
		return FinishResult{}, fmt.Errorf("worktree: resolve merged HEAD: %w", err)
	}
	return FinishResult{Merged: true, Commit: commit, SquashCommit: opts.Squash}, nil
}

// OwnedByAgentDir reports whether wtPath is inside the repo's agent
// worktree dir (.marshal/worktrees/), after symlink canonicalization on
// both sides. A purely lexical containment check would let a symlink
// inside the agent dir (e.g. .marshal/worktrees/link → /elsewhere) pass,
// and a later WorktreeRemove would then remove the symlink's target.
// Replicates the containment half of WorktreeManager.verifyOwnership in
// internal/acp/worktree.go.
func OwnedByAgentDir(repoRoot, wtPath string) bool {
	agentDir := filepath.Join(repoRoot, ".marshal", "worktrees")
	canonAgent := CanonicalPath(agentDir)
	canonActive := CanonicalPath(wtPath)
	rel, err := filepath.Rel(canonAgent, canonActive)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// mergeRefusal classifies a failed merge. A refusal to overwrite untracked
// files is the project blocking the merge, not a conflict between branches.
func mergeRefusal(merr error) FinishResult {
	if paths := untrackedOverwrites(merr.Error()); len(paths) > 0 {
		return FinishResult{Reason: ReasonDirtyProject, Blocking: paths}
	}
	return FinishResult{Reason: ReasonConflicts, Conflicted: conflictFiles(merr.Error())}
}
