//go:build probe_c

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The spike persists its token and in-flight flow state as plain files under
// $XDG_CACHE_HOME/marshal-codex-spike/ (0600). This is deliberately NOT
// internal/credentials: the spike is run repeatedly during development and
// must not fight keyring daemon unlocks. The real provider uses
// internal/credentials in Phase 3 of the design.

const spikeCacheDirName = "marshal-codex-spike"

// spikeCacheDir resolves $XDG_CACHE_HOME/marshal-codex-spike, defaulting to
// ~/.cache/marshal-codex-spike.
func spikeCacheDir() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, spikeCacheDirName), nil
}

// tokenPath is the on-disk location of the raw token-endpoint response.
func tokenPath() (string, error) {
	dir, err := spikeCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token.json"), nil
}

// flowStatePath is the on-disk location of the in-flight authorization state
// (PKCE verifier, CSRF state, redirect URI, device-flow handles).
func flowStatePath() (string, error) {
	dir, err := spikeCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "flow.json"), nil
}

// flowState carries everything auth-begin learns that auth-poll needs. It is
// written 0600 and removed once the exchange succeeds.
type flowState struct {
	// Grant is "pkce" (browser + loopback) or "device" (codex's bespoke
	// two-endpoint device flow).
	Grant string `json:"grant"`

	// PKCE flow fields.
	Verifier     string `json:"verifier,omitempty"`
	Challenge    string `json:"challenge,omitempty"`
	State        string `json:"state,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	AuthorizeURL string `json:"authorize_url,omitempty"`

	// Device flow fields.
	DeviceAuthID    string `json:"device_auth_id,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	VerificationURL string `json:"verification_url,omitempty"`

	// Code is filled in by auth-poll once the authorization code is known
	// (loopback callback or device poll).
	Code string `json:"code,omitempty"`
}

// writePrivateFile writes data to path with mode 0600, creating the parent
// directory 0700 if needed.
func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// saveTokens persists the raw token-endpoint JSON response verbatim so the
// findings doc can quote the exact key set the server returned.
func saveTokens(raw map[string]any) error {
	path, err := tokenPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token response: %w", err)
	}
	return writePrivateFile(path, data)
}

// loadTokens reads the stored token response. The map is raw so unknown keys
// survive a round trip.
func loadTokens() (map[string]any, error) {
	path, err := tokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s (run auth-begin/auth-poll first): %w", path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return raw, nil
}

// accessToken returns the stored access_token, or an error naming the file.
func accessToken() (string, error) {
	raw, err := loadTokens()
	if err != nil {
		return "", err
	}
	tok, _ := raw["access_token"].(string)
	if tok == "" {
		return "", fmt.Errorf("stored token response has no access_token")
	}
	return tok, nil
}

// saveFlowState persists the in-flight authorization state.
func saveFlowState(st flowState) error {
	path, err := flowStatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode flow state: %w", err)
	}
	return writePrivateFile(path, data)
}

// loadFlowState reads the in-flight authorization state.
func loadFlowState() (flowState, error) {
	path, err := flowStatePath()
	if err != nil {
		return flowState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return flowState{}, fmt.Errorf("read %s (run auth-begin first): %w", path, err)
	}
	var st flowState
	if err := json.Unmarshal(data, &st); err != nil {
		return flowState{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return st, nil
}

// clearFlowState removes the in-flight state once the exchange has completed,
// so a stale verifier can never be replayed.
func clearFlowState() error {
	path, err := flowStatePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
