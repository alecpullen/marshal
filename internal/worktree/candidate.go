package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MergeExact merges one exact commit into dir's current HEAD with a real merge
// commit (--no-ff --no-edit), so the candidate's own history is retained rather
// than squashed or fast-forwarded away, and returns the resulting commit.
//
// The candidate argument must be a commit object id, never a branch or tag name:
// a ref can move between review and mutation, and the caller's record binds the
// work it reviewed by object id. message is the whole merge message; callers put
// their recovery trailer in it.
//
// On a conflict the merge is left in progress (git's own behaviour) so the
// caller can prove it owns that merge before aborting it. The error carries
// git's report, which names the refused paths.
func (g CLIGitOps) MergeExact(ctx context.Context, dir, candidate, message string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !isObjectID(candidate) {
		return "", fmt.Errorf("worktree git: merge needs a commit object id, got %q", candidate)
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("worktree git: merge needs a message")
	}
	args := []string{"merge", "--no-ff", "--no-edit", "-m", message, candidate}
	if _, err := g.run(dir, "config", "user.email"); err != nil {
		// No identity in this repository: supply one for this merge commit
		// only, exactly as CommitIndex does. The operator's configured
		// identity wins wherever one exists.
		args = append([]string{
			"-c", "user.name=" + commitAuthorName,
			"-c", "user.email=" + commitAuthorEmail,
		}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("worktree git: merge %s: %w: %s", candidate, err, strings.TrimSpace(string(out)))
	}
	return g.RevParse(dir, "HEAD")
}

// MergeState reports a merge in progress at dir. It is empty when the checkout
// is idle, which is the only answer that permits starting another merge.
//
// A non-empty value is line oriented, so a caller can prove which merge it is
// looking at instead of assuming it owns whatever it finds:
//
//	head <commit id>   one line per commit being merged
//	conflict <path>    one line per unmerged path
//
// MERGE_HEAD lives in the checkout's own git directory, so a linked worktree
// reports its own merge and never the main checkout's.
func (g CLIGitOps) MergeState(dir string) (string, error) {
	heads, err := g.mergeHeads(dir)
	if err != nil {
		return "", err
	}
	if len(heads) == 0 {
		return "", nil
	}
	conflicts, err := g.unmergedPaths(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, head := range heads {
		b.WriteString("head " + head + "\n")
	}
	for _, path := range conflicts {
		b.WriteString("conflict " + path + "\n")
	}
	return b.String(), nil
}

// mergeHeads reads MERGE_HEAD for dir. rev-parse --git-path resolves the
// per-worktree git directory, so this works in a linked worktree too.
func (g CLIGitOps) mergeHeads(dir string) ([]string, error) {
	path, err := g.run(dir, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worktree git: read merge state of %s: %w", dir, err)
	}
	var heads []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			heads = append(heads, line)
		}
	}
	return heads, nil
}

// unmergedPaths lists the paths git left unmerged in dir.
func (g CLIGitOps) unmergedPaths(dir string) ([]string, error) {
	out, err := g.runRaw(dir, "diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, field := range strings.Split(out, "\x00") {
		if field != "" {
			paths = append(paths, field)
		}
	}
	return paths, nil
}

// Parents returns the parent commits of commit, oldest first. A merge commit has
// two or more, and their identity is how a caller proves which merge it is
// looking at; a root commit has none.
func (g CLIGitOps) Parents(dir, commit string) ([]string, error) {
	if !isObjectID(commit) {
		return nil, fmt.Errorf("worktree git: parents need a commit object id, got %q", commit)
	}
	out, err := g.run(dir, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("worktree git: rev-list --parents %s: empty output", commit)
	}
	if len(fields) == 1 {
		return nil, nil
	}
	return fields[1:], nil
}

// MergeStateInfo is a parsed MergeState value: the commits a checkout is
// merging, and the paths it left unmerged.
type MergeStateInfo struct {
	Heads     []string
	Conflicts []string
}

// ParseMergeState parses a MergeState value. An empty value yields an empty
// info, and an unrecognised line is ignored rather than guessed at: a caller
// that cannot prove a fact must not act on it.
func ParseMergeState(state string) MergeStateInfo {
	var info MergeStateInfo
	for _, line := range strings.Split(state, "\n") {
		switch {
		case strings.HasPrefix(line, "head "):
			if v := strings.TrimSpace(strings.TrimPrefix(line, "head ")); v != "" {
				info.Heads = append(info.Heads, v)
			}
		case strings.HasPrefix(line, "conflict "):
			if v := strings.TrimSpace(strings.TrimPrefix(line, "conflict ")); v != "" {
				info.Conflicts = append(info.Conflicts, v)
			}
		}
	}
	return info
}

// HasHead reports whether commit is one of the commits being merged.
func (i MergeStateInfo) HasHead(commit string) bool {
	for _, head := range i.Heads {
		if head == commit {
			return true
		}
	}
	return false
}
