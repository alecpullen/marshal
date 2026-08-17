package plugins

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"marshal/internal/app/config"
)

// NormalizeSource converts a user-supplied source into a clone URL and a
// default plugin name. "github:owner/repo" expands to the HTTPS clone URL;
// every other form (full URL, local path) passes through unchanged, with
// the name derived from the basename minus any ".git" suffix.
func NormalizeSource(source string) (cloneURL, name string, err error) {
	if strings.HasPrefix(source, "github:") {
		repo := strings.TrimPrefix(source, "github:")
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid source %q: want github:owner/repo", source)
		}
		return "https://github.com/" + repo + ".git", parts[1], nil
	}
	name = path.Base(filepath.ToSlash(strings.TrimSuffix(source, ".git")))
	if !ValidName(name) {
		return "", "", fmt.Errorf("cannot derive a plugin name from %q; pass --name", source)
	}
	return source, name, nil
}

// SplitSourceRef splits an inline "@ref" off a source. Only the github:
// shorthand supports inline refs; full URLs (which may legitimately
// contain "@", e.g. git@host:path) pass through untouched and callers
// should use the --ref flag for them.
func SplitSourceRef(arg string) (source, ref string, err error) {
	if !strings.HasPrefix(arg, "github:") {
		return arg, "", nil
	}
	i := strings.LastIndex(arg, "@")
	if i == -1 {
		return arg, "", nil
	}
	if i == len(arg)-1 {
		return "", "", fmt.Errorf("invalid source %q: empty ref after @", arg)
	}
	return arg[:i], arg[i+1:], nil
}

// ValidName reports whether name is safe to use as a single directory
// name in the plugin store.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// ValidateCloneSource validates that a file:// source is contained
// within the workspace root, after resolving symlinks. Non-file:// sources
// always pass — git handles remote URLs with its own transport security,
// and plain local paths are a supported interactive install source (the
// user explicitly provides them). This prevents a malicious config,
// lockfile, or ACP request from cloning arbitrary local paths (e.g.
// file:///etc/passwd) or a workspace-internal symlink that points outside
// the workspace. Local development and testing use file:// with paths
// inside the workspace.
func ValidateCloneSource(source, workspaceRoot string) error {
	u, err := url.Parse(source)
	if err != nil {
		return nil // malformed sources fail later in git clone
	}
	if u.Scheme != "file" {
		return nil
	}
	// Only an empty host or "localhost" is a local file URL. A non-empty
	// host (file://attacker-host/path) is a remote file share and must be
	// rejected outright.
	if u.Host != "" && u.Host != "localhost" {
		return fmt.Errorf("file:// source %q has a non-local host %q", source, u.Host)
	}
	// Make the file path absolute (relative to the process cwd) so symlink
	// resolution and containment compare against a consistent base.
	abs, err := filepath.Abs(u.Path)
	if err != nil {
		return fmt.Errorf("invalid file:// path %q: %w", u.Path, err)
	}
	// Resolve symlinks on both the source and the workspace root so a
	// symlink inside the workspace pointing outside is caught, and so a
	// workspace root reached via a symlink compares consistently.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The path may not exist yet (git clone will fail on it anyway);
		// fall back to the cleaned absolute path for the containment check.
		resolved = abs
	}
	wsAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("invalid workspace root %q: %w", workspaceRoot, err)
	}
	wsResolved, err := filepath.EvalSymlinks(wsAbs)
	if err != nil {
		wsResolved = wsAbs
	}

	// Check containment: resolved must be inside wsResolved.
	rel, err := filepath.Rel(wsResolved, resolved)
	if err != nil {
		return fmt.Errorf("file:// source %q is outside the workspace root", u.Path)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("file:// source %q is outside the workspace root", u.Path)
	}
	return nil
}

// GlobalStoreDir is where user-scope plugins are installed.
func GlobalStoreDir(home string) string {
	return filepath.Join(config.UserDir(home), "plugins")
}

// GlobalLockPath is the user-scope lockfile path.
func GlobalLockPath(home string) string {
	return filepath.Join(config.UserDir(home), "plugins-lock.json")
}

// ProjectStoreDir is where project-scope plugins are installed.
func ProjectStoreDir(work string) string {
	return filepath.Join(work, ".marshal", "plugins")
}

// ProjectLockPath is the project-scope lockfile path.
func ProjectLockPath(work string) string {
	return filepath.Join(work, ".marshal", "plugins-lock.json")
}
