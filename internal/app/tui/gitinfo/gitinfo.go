// Package gitinfo reads the current git branch and linked-worktree name
// from git internals — pure file I/O, no subprocess, so it is safe to call
// on the TUI render path. A linked worktree is detected by the `.git` file
// (not directory) that git creates, pointing at <repo>/.git/worktrees/<name>.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"marshal/internal/strutil"
)

const (
	maxBranchRunes   = 20
	maxWorktreeRunes = 16
)

// Info describes the git state of a working directory. The zero value
// (InRepo false, empty strings) means "not a git repo" or "unreadable";
// callers render nothing in that case.
type Info struct {
	Branch   string
	Worktree string
	InRepo   bool
}

var cache sync.Map // workingDir (string) -> Info

// Read returns cached git info for workingDir, reading from disk on a cache
// miss. Safe for concurrent callers. On any parse error it returns the zero
// Info and caches it (so a non-repo dir is only walked once).
func Read(workingDir string) Info {
	if workingDir == "" {
		return Info{}
	}
	if v, ok := cache.Load(workingDir); ok {
		return v.(Info)
	}
	info := readUncached(workingDir)
	cache.Store(workingDir, info)
	return info
}

// Invalidate drops the cached entry for workingDir so the next Read re-reads
// from disk. Call after a known branch change (e.g. on resize/focus) when
// freshness matters more than the ~instant cache hit.
func Invalidate(workingDir string) {
	if workingDir == "" {
		return
	}
	cache.Delete(workingDir)
}

func readUncached(workingDir string) Info {
	gitPath, ok := findGit(workingDir)
	if !ok {
		return Info{}
	}
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return Info{}
	}
	switch {
	case fi.IsDir():
		// Main worktree: .git is a directory.
		branch, ok := parseHead(filepath.Join(gitPath, "HEAD"))
		if !ok {
			return Info{}
		}
		return Info{Branch: truncateBranch(branch), InRepo: true}
	default:
		// Linked worktree: .git is a file containing "gitdir: <abspath>".
		raw, err := os.ReadFile(gitPath)
		if err != nil {
			return Info{}
		}
		gitdir := parseGitdir(string(raw))
		if gitdir == "" {
			return Info{}
		}
		branch, ok := parseHead(filepath.Join(gitdir, "HEAD"))
		if !ok {
			return Info{}
		}
		name := filepath.Base(gitdir)
		if name == "" || name == "." || name == "/" {
			return Info{}
		}
		return Info{Branch: truncateBranch(branch), Worktree: strutil.Truncate(name, maxWorktreeRunes, true), InRepo: true}
	}
}

// findGit walks up from start looking for a ".git" entry, stopping at the
// first match. Returns the absolute path and true, or "" and false at the
// filesystem root.
func findGit(start string) (string, bool) {
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, ".git")
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// parseHead reads a HEAD file and returns the branch name (or 7-char short
// SHA for detached HEAD) and true. Returns false on any read/parse failure.
func parseHead(headPath string) (string, bool) {
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(raw))
	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(s, refPrefix) {
		return strings.TrimPrefix(s, refPrefix), true
	}
	// Detached HEAD: a 40-char SHA. Use the first 7 chars (git short-SHA).
	if isHexSha(s) {
		if len(s) >= 7 {
			return s[:7], true
		}
		return s, true
	}
	return "", false
}

// parseGitdir extracts the path from a "gitdir: <path>" line.
func parseGitdir(raw string) string {
	const prefix = "gitdir:"
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(s, prefix))
}

func truncateBranch(b string) string { return strutil.Truncate(b, maxBranchRunes, true) }

func isHexSha(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
