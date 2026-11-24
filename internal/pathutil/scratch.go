// internal/pathutil/scratch.go — the roots a session may use as ordinary
// scratch space, outside its workspace.
package pathutil

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// TempRoots returns the directories the operating system hands out for this
// process's temporary work: its own temporary directory.
//
// A session stages build output, test fixtures and throwaway scripts under the
// temporary directory as a matter of course. Treating that space as privileged
// made every such file need a System grant, even though the shell surface
// could already read and write it without one — an inconsistency between two
// surfaces rather than a boundary worth keeping.
//
// The list is the session's own temporary directory, deliberately not every
// well-known temporary location. A Unix host's shared /tmp belongs to every
// process on the machine, so granting it would make one session's scratch grant
// cover every other session's and every other user's files there — and on a
// host whose TMPDIR points elsewhere (macOS) it is not this session's
// temporary directory at all.
//
// The entries are resolved through symlinks, so a grant spelled with a linked
// directory still covers the real one. Duplicates collapse to one entry.
func TempRoots() []string {
	candidates := []string{os.TempDir()}
	if runtime.GOOS == "windows" {
		// os.TempDir already consults TMP and TEMP, but a process may carry a
		// different value in each; both name the same space to Windows.
		candidates = append(candidates, os.Getenv("TMP"), os.Getenv("TEMP"))
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

// HomeDir returns the user's home directory, and whether it could be
// determined at all. The environment's answer (os.UserHomeDir) wins; the
// passwd entry is the fallback for a process started without HOME — a service
// unit, a container, a stripped test environment.
//
// A caller that uses the home directory as a carve-out must treat !ok as "no
// scratch reach", never as "no carve-out": a grant that keeps credentials out
// of reach cannot fail open because the environment lost HOME.
func HomeDir() (string, bool) {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home, true
	}
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.HomeDir) != "" {
		return current.HomeDir, true
	}
	return "", false
}
