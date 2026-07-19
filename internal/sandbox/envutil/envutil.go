// Package envutil hosts env-scrubbing helpers that are shared between the
// sandbox and the hooks runner. The sandbox package itself imports
// marshal/internal/tools/native (which transitively imports the agent
// package in tests), so anything that wants to use these helpers without
// pulling in the full sandbox dep graph must import this package
// directly. Keeping the helpers here avoids duplicating the denylist and
// keeps hooks and the sandbox's nil-allowlist scrub in agreement.
package envutil

import "strings"

// EnvKey returns the key portion of an "KEY=VALUE" env string. If the
// string contains no '=', the whole string is returned.
func EnvKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

// Set returns env with key=value, replacing an existing entry for key if
// present. env entries are "KEY=value" strings.
func Set(env []string, key, value string) []string {
	for i, kv := range env {
		if EnvKey(kv) == key {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

// FilterKeys returns env without entries whose key is in deny.
func FilterKeys(env []string, deny map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if deny[EnvKey(kv)] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
