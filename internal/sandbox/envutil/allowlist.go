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

// IsSecretKey reports whether key names a likely secret/credential. It is
// the union of the old IsSecretKey (case-insensitive substring patterns)
// and IsSecretBearer (exact well-known names, provider prefixes, and
// credential suffixes): one predicate, one list, used by the sandbox, the
// MCP client/manager, and the hooks runner.
func IsSecretKey(key string) bool {
	k := strings.ToUpper(key)
	switch k {
	case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO",
		"DATABASE_URL", "REDIS_URL", "MONGODB_URI":
		return true
	}
	for _, p := range []string{
		"API_KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
		"AWS_", "GCP_", "AZURE_", "GH_", "GITHUB_", "GITLAB_", "GOOGLE_",
		"ANTHROPIC", "OPENAI", "COHERE", "MISTRAL", "HUGGINGFACE", "OPENROUTER",
	} {
		if strings.Contains(k, p) {
			return true
		}
	}
	for _, s := range []string{
		"_KEY", "_TOKEN", "_SECRET", "_PASSWORD",
		"_CREDENTIALS", "_CREDENTIAL", "_PRIVATE_KEY", "_ACCESS_KEY",
	} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// IsDangerousKey reports whether key is a shell-hijack or dynamic-loader
// variable that should never be inherited from or set by user-supplied
// env additions. This is for downstream consumers (e.g. MCP client) that
// need to reject user-supplied env vars containing LD_*, DYLD_*,
// BASH_FUNC_* prefixes, or exact-dangerous names like IFS, BASH_ENV,
// PATH, etc.
func IsDangerousKey(key string) bool {
	if strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
		return true
	}
	if strings.HasPrefix(key, "BASH_FUNC_") {
		return true
	}
	switch key {
	case "IFS", "SHELLOPTS", "BASH_ENV", "ENV", "ZDOTDIR", "PATH":
		return true
	}
	return false
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
