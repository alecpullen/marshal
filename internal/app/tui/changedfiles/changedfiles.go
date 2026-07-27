// Package changedfiles reads the working tree's diff against a base ref
// for the side panel's changed-files section. Every failure path returns
// nil: this is telemetry and must never break a turn or block a render.
package changedfiles

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"marshal/internal/app/tui/sidepanel"
)

// readTimeout bounds the git subprocess. A slow or hung git must not
// stall the caller.
const readTimeout = 2 * time.Second

// Read returns the files changed in workingDir since baseRef, including
// untracked-but-staged files. Returns nil on any error.
func Read(workingDir, baseRef string) []sidepanel.ChangedFile {
	if workingDir == "" || baseRef == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	// First pass: working tree vs base ref. This does NOT report
	// staged-but-uncommitted new files.
	out, err := exec.CommandContext(ctx, "git", "-C", workingDir,
		"diff", "--numstat", baseRef).Output()
	if err != nil {
		return nil
	}

	files := parseNumstat(string(out))

	// Second pass: staged (cached) vs base ref. This catches new files
	// that were added to the index but never committed. Merge by path so
	// we don't double-count modified files that appear in both passes.
	ctx2, cancel2 := context.WithTimeout(context.Background(), readTimeout)
	defer cancel2()

	out2, err := exec.CommandContext(ctx2, "git", "-C", workingDir,
		"diff", "--numstat", "--cached", baseRef).Output()
	if err != nil {
		// If the second pass fails, return whatever the first pass found.
		return files
	}

	byPath := make(map[string]int, len(files))
	for i, f := range files {
		byPath[f.Path] = i
	}
	for _, f := range parseNumstat(string(out2)) {
		if idx, ok := byPath[f.Path]; ok {
			// Merge counts: the cached diff may have more recent changes.
			files[idx].Added = f.Added
			files[idx].Removed = f.Removed
		} else {
			files = append(files, f)
		}
	}

	return files
}

// parseNumstat parses the output of `git diff --numstat` into ChangedFile
// entries. Returns nil for empty or unparseable output.
func parseNumstat(out string) []sidepanel.ChangedFile {
	var files []sidepanel.ChangedFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		// Binary files report "-" for both counts.
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		status := 'M'
		if removed == 0 && added > 0 {
			status = 'A'
		} else if added == 0 && removed > 0 {
			status = 'D'
		}
		files = append(files, sidepanel.ChangedFile{
			Path: parts[2], Status: status, Added: added, Removed: removed,
		})
	}
	return files
}
