// internal/pathutil/scratch.go — the roots a session may use as ordinary
// scratch space, outside its workspace.
package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// TempRoots returns the directories an operating system hands out for
// temporary work: the session's own temporary directory and, when it differs,
// the conventional shared one.
//
// A session stages build output, test fixtures and throwaway scripts under the
// temporary directory as a matter of course. Treating that space as privileged
// made every such file need a System grant, even though the shell surface
// could already read and write it without one — an inconsistency between two
// surfaces rather than a boundary worth keeping.
//
// The list is resolved through symlinks, because a grant spelled /tmp must
// still cover the real directory on a platform where /tmp is a link into
// /private/tmp. Duplicates collapse to one entry.
func TempRoots() []string {
	candidates := []string{os.TempDir()}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, os.Getenv("TMP"), os.Getenv("TEMP"))
	} else {
		candidates = append(candidates, "/tmp")
	}
	seen := make(map[string]bool, len(candidates))
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved := absolute
		if link, err := filepath.EvalSymlinks(absolute); err == nil {
			resolved = link
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		roots = append(roots, resolved)
	}
	return roots
}
