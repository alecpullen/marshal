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
