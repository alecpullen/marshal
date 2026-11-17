package desktop

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"marshal/internal/permissions"
)

func urlAllowed(rawURL string, allowlist, denylist []string, workspaceRoot string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("navigation blocked: invalid URL %q", rawURL)
	}
	switch parsed.Scheme {
	case "file":
		return fileAllowed(parsed, denylist, workspaceRoot)
	case "http", "https":
	default:
		return fmt.Errorf("navigation blocked: scheme %q not allowed for %q", parsed.Scheme, rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("navigation blocked: invalid URL %q", rawURL)
	}
	host := parsed.Hostname()
	path := parsed.Path

	for _, entry := range denylist {
		if matchHostPath(host, path, entry) {
			return fmt.Errorf("navigation blocked by policy (denylist): %s", rawURL)
		}
	}

	if len(allowlist) == 0 {
		return nil
	}

	for _, entry := range allowlist {
		if matchHostPath(host, path, entry) {
			return nil
		}
	}
	return fmt.Errorf("navigation blocked by policy (not in allowlist): %s", rawURL)
}

// fileAllowed confines file: navigation to the workspace. The allowlist does
// not apply: it names network destinations, and a configured allowlist must
// not become a second way to deny local rendering.
func fileAllowed(u *url.URL, denylist []string, workspaceRoot string) error {
	if u.Host != "" {
		return fmt.Errorf("navigation blocked: remote file URL %q", u.String())
	}
	if workspaceRoot == "" {
		return fmt.Errorf("navigation blocked: no workspace root for %q", u.String())
	}
	path := filepath.FromSlash(u.Path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("navigation blocked: %q does not resolve: %w", u.String(), err)
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return fmt.Errorf("navigation blocked: workspace root %q does not resolve: %w", workspaceRoot, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("navigation blocked: %q is outside the workspace", u.String())
	}
	relSlash := filepath.ToSlash(rel)
	for _, entry := range denylist {
		if permissions.Matches(entry, resolved) {
			return fmt.Errorf("navigation blocked by policy (denylist): %s", u.String())
		}
		// A relative entry names a workspace-relative path — the form a user
		// writes, "scratch/**" — and permissions.Matches anchors a pattern
		// containing "/" at the subject's start, which the absolute path above
		// can never be. Matching it against rel as well keeps a workspace entry
		// able to block the file it names.
		if !filepath.IsAbs(entry) && permissions.Matches(entry, relSlash) {
			return fmt.Errorf("navigation blocked by policy (denylist): %s", u.String())
		}
	}
	return nil
}

func matchHostPath(host, path, entry string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	parts := strings.SplitN(entry, "/", 2)
	entryHost := parts[0]
	if !hostMatches(host, entryHost) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	entryPath := "/" + strings.TrimPrefix(parts[1], "/")
	return strings.HasPrefix(path, entryPath)
}

func hostMatches(host, pattern string) bool {
	if pattern == host {
		return true
	}
	return strings.HasSuffix(host, "."+pattern)
}
