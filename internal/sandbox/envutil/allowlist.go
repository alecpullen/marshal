package envutil

import (
	"sort"
	"strings"
)

// allowlistKeys is the set of parent env var names that are safe to
// pass through to sandboxed child processes. Everything else is
// dropped.
var allowlistKeys = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"LC_COLLATE": true, "LC_CTYPE": true, "LC_MESSAGES": true,
	"LC_MONETARY": true, "LC_NUMERIC": true, "LC_TIME": true,
	"TZ": true, "TMPDIR": true, "XDG_RUNTIME_DIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
}

// AllowList returns a copy of parent containing only the variables
// in allowlistKeys — a curated set of env vars that are safe to
// propagate to sandboxed child processes. Secret-bearing keys,
// dynamic-loader keys (LD_*, DYLD_*), and shell-hijack keys (IFS,
// SHELLOPTS, BASH_ENV, etc.) are excluded by virtue of not being
// in the allowlist. The result is sorted by key for stable output.
//
// Only keys present in both parent and allowlistKeys are included.
func AllowList(parent []string) []string {
	seen := map[string]string{}
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		seen[k] = v
	}

	out := make([]string, 0, len(seen))
	for k, v := range seen {
		if !allowlistKeys[k] {
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
