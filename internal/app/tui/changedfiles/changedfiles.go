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
// untracked-but-staged files and untracked-and-unstaged new files. Returns
// nil on any error.
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

	// Third pass: name-status for accurate file classification. numstat
	// can't distinguish a new file from a modified file with only
	// additions; --name-status provides the authoritative status letter.
	ctx3, cancel3 := context.WithTimeout(context.Background(), readTimeout)
	defer cancel3()

	out3, err := exec.CommandContext(ctx3, "git", "-C", workingDir,
		"diff", "--name-status", baseRef).Output()
	if err == nil {
		statusMap := parseNameStatus(string(out3))
		for i := range files {
			if s, ok := statusMap[files[i].Path]; ok {
				files[i].Status = s
			}
		}
	}

	// Fourth pass: untracked-and-unstaged new files. None of the diff
	// passes above report these (they only see tracked content), so list
	// them explicitly and append as additions. Respect .gitignore via
	// --exclude-standard so ignored files never surface in the rail.
	ctx4, cancel4 := context.WithTimeout(context.Background(), readTimeout)
	defer cancel4()

	out4, err := exec.CommandContext(ctx4, "git", "-C", workingDir,
		"ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return files
	}
	for _, path := range strings.Split(strings.TrimSpace(string(out4)), "\n") {
		if path == "" {
			continue
		}
		if _, ok := byPath[path]; ok {
			continue
		}
		files = append(files, sidepanel.ChangedFile{
			Path: path, Status: 'A', Added: 1,
		})
	}

	return files
}

// parseNameStatus parses `git diff --name-status` output into a path→status
// map. The status letter is the first character of each line's first field
// (A, M, D, R, C, etc.). Rename/copy lines carry two paths (old and new);
// numstat reports the new path, so the map is keyed by the new path to stay
// consistent with the entries produced by parseNumstat.
func parseNameStatus(out string) map[string]rune {
	m := make(map[string]rune)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		path := parts[1]
		if len(parts) == 3 {
			// Rename/copy: "R100\told\tnew" — use the new path.
			path = parts[2]
		}
		m[path] = rune(parts[0][0])
	}
	return m
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
