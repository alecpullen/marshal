package plugins

import (
	"fmt"
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
