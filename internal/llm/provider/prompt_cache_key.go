package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

// promptCacheKey derives the prompt_cache_key for a conversation. OpenAI
// routes requests that share a key and a prompt prefix to the same cache,
// so a session's tool loop keeps hitting it instead of landing on a cold
// machine. The session ID is hashed: the key only has to be stable and
// distinct per conversation, and the provider has no use for marshal's
// own identifiers. Empty sessions get no key.
func promptCacheKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "marshal-" + hex.EncodeToString(sum[:12])
}

// sendsPromptCacheKey reports whether this endpoint accepts
// prompt_cache_key. Only OpenAI's own API is known to; strict
// OpenAI-compatible servers reject unknown fields, so every other
// endpoint keeps its existing wire.
func (p *OpenAICompatible) sendsPromptCacheKey() bool {
	if p.sessionID == "" {
		return false
	}
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "api.openai.com"
}
