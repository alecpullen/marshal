package desktop

import (
	"fmt"
	"net/url"
	"strings"
)

func urlAllowed(rawURL string, allowlist, denylist []string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
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
