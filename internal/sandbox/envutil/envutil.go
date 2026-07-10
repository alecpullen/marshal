// Package envutil hosts env-scrubbing helpers that are shared between the
// sandbox and the hooks runner. The sandbox package itself imports
// marshal/internal/tools/native (which transitively imports the agent
// package in tests), so anything that wants to use these helpers without
// pulling in the full sandbox dep graph must import this package
// directly. Keeping the helpers here avoids duplicating the denylist and
// keeps hooks and the sandbox's nil-allowlist scrub in agreement.
package envutil

import "strings"

// IsSecretBearer reports whether an env key is commonly secret-bearing.
// Used in the "no explicit allowlist" path so parent env leaks don't carry
// obvious API keys / tokens / credentials to untrusted commands. This is
// a best-effort denylist; users who want stronger isolation must set an
// explicit allowlist.
func IsSecretBearer(key string) bool {
	// Exact-name denylist (common credential env vars).
	switch key {
	case "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_ACCESS_KEY_ID":
		return true
	case "GITHUB_TOKEN", "GITHUB_TOKEN_LEGACY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY":
		return true
	case "OPENROUTER_API_KEY", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO":
		return true
	case "DATABASE_URL", "REDIS_URL", "MONGODB_URI", "POSTGRES_PASSWORD":
		return true
	}
	// Prefix/suffix rules for *_KEY, *_TOKEN, *_SECRET, etc.
	k := strings.ToUpper(key)
	for _, p := range []string{"AWS_", "GITHUB_", "OPENAI_", "ANTHROPIC_", "OPENROUTER_", "HUGGINGFACE_", "COHERE_", "GOOGLE_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	for _, s := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIALS", "_API_KEY", "_PRIVATE_KEY", "_ACCESS_KEY", "_CREDENTIAL"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// EnvKey returns the key portion of an "KEY=VALUE" env string. If the
// string contains no '=', the whole string is returned.
func EnvKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}
