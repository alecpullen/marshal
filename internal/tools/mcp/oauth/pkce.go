// Package oauth implements an OAuth 2.1 authorization-code flow with PKCE
// (RFC 7636) for remote Streamable-HTTP MCP servers. See package oauth for
// the Engine entry point.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// pkceVerifierBytes is the RFC 7636 §4.1 minimum (43 chars after
// base64url-encoding). 64 bytes produces an 86-char verifier — comfortably
// above the minimum and a common choice in MCP-style clients.
const pkceVerifierBytes = 64

// stateBytes is the byte length of the per-flow random state parameter
// (CSRF guard). 32 bytes → 43 base64url chars, matching common practice.
const stateBytes = 32

// PKCE holds a freshly generated code_verifier and the derived
// code_challenge. The verifier is secret; the challenge is safe to embed
// in the authorization URL.
type PKCE struct {
	Verifier  string
	Challenge string
	Method    string
}

// NewPKCE generates a fresh S256 PKCE pair. The verifier is 64 random
// bytes, base64url-encoded without padding (RFC 7636 §4.1); the challenge
// is BASE64URL(SHA256(verifier)) per §4.2.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("oauth: read random bytes for PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	return PKCE{
		Verifier:  verifier,
		Challenge: challenge,
		Method:    "S256",
	}, nil
}

// NewState returns a fresh, URL-safe random state string for CSRF
// protection on the authorization callback.
func NewState() (string, error) {
	raw := make([]byte, stateBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: read random bytes for state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// VerifyState reports whether the state returned on the callback matches
// the state issued with the authorization request. The comparison is
// constant-time so an attacker cannot probe the state byte-by-byte.
func VerifyState(expected, got string) bool {
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}
