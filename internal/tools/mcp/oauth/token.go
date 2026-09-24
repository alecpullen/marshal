package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"marshal/internal/redact"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tokenRefreshSkew is how early we proactively refresh a still-valid
// access token. The OAuth 2.1 spec recommends clients treat tokens as
// expired slightly before their declared expiry to absorb clock skew and
// network latency.
const tokenRefreshSkew = 30 * time.Second

// tokenRequestTimeout bounds a single token call.
const tokenRequestTimeout = 30 * time.Second

// tokenResponse is the OAuth 2.0 token-endpoint response (RFC 6749 §5.1).
// Fields are kept defensive: expires_in is optional, refresh_token is
// optional (only returned on first exchange by some servers).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// oauthError is RFC 6749 §5.2's error body. We only inspect `error` for
// the special-case invalid_grant refresh path; the rest just bubble up
// in the returned error message.
type oauthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// StoredTokens is the JSON blob persisted in the TokenStore under the
// server-scoped key. Fields use explicit JSON tags so the on-disk shape
// stays stable across releases.
type StoredTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"` // populated only when the AS issued one
	IssuedAt     time.Time `json:"issued_at"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`

	// TokenEndpoint records the endpoint that issued these tokens. A
	// refresh is refused when the server later advertises a different
	// one, so a refresh token is never sent to a host that did not
	// issue it.
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

// IsExpired reports whether the stored access token is expired or about
// to be (within tokenRefreshSkew).
func (s StoredTokens) IsExpired() bool {
	if s.ExpiresAt.IsZero() {
		return true
	}
	return !time.Now().Add(tokenRefreshSkew).Before(s.ExpiresAt)
}

// tokenHTTPClient is the minimum interface Exchange and Refresh need.
// Production uses an injected *http.Client on the engine; tests use
// httptest.Server-backed clients.
type tokenHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Exchange trades an authorization code (plus PKCE verifier) for a token
// set. The returned StoredTokens already has ExpiresAt / IssuedAt
// computed from the server's expires_in.
func Exchange(ctx context.Context, hc tokenHTTPClient, tokenEndpoint, code, redirectURI, clientID, verifier string) (StoredTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	return postToken(ctx, hc, tokenEndpoint, form)
}

// Refresh trades a refresh_token for a new token set. Some servers rotate
// the refresh token on every refresh; we honor that.
func Refresh(ctx context.Context, hc tokenHTTPClient, tokenEndpoint, refreshToken, clientID string) (StoredTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	return postToken(ctx, hc, tokenEndpoint, form)
}

func postToken(ctx context.Context, hc tokenHTTPClient, endpoint string, form url.Values) (StoredTokens, error) {
	dctx, cancel := context.WithTimeout(ctx, tokenRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return StoredTokens{}, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return StoredTokens{}, fmt.Errorf("oauth: token endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var oe oauthError
		_ = json.Unmarshal(body, &oe)
		return StoredTokens{}, &TokenError{
			Code:        oe.Error,
			Description: oe.ErrorDescription,
			Status:      resp.StatusCode,
			Body:        strings.TrimSpace(string(body)),
			Endpoint:    endpoint,
		}
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return StoredTokens{}, fmt.Errorf("oauth: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return StoredTokens{}, errors.New("oauth: token response missing access_token")
	}

	now := time.Now()
	expiry := now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	// A missing expires_in is suspicious but tolerable: treat the
	// token as valid for one hour so we still try a refresh next call.
	if tr.ExpiresIn == 0 {
		expiry = now.Add(time.Hour)
	}
	return StoredTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    expiry,
		IssuedAt:     now,
		TokenType:    tr.TokenType,
		Scope:        tr.Scope,
	}, nil
}

// TokenError is the typed error returned for non-2xx token-endpoint
// responses. The Code field carries the RFC 6749 error string (e.g.
// "invalid_grant"); callers use it to decide whether to drop the stored
// refresh token.
type TokenError struct {
	Code        string
	Description string
	Status      int
	Body        string
	Endpoint    string
}

func (e *TokenError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth: token endpoint %s returned %s: %s (HTTP %d)", e.Endpoint, e.Code, e.Description, e.Status)
	}
	if e.Body != "" {
		return fmt.Sprintf("oauth: token endpoint %s returned HTTP %d: %s", e.Endpoint, e.Status, e.Body)
	}
	return fmt.Sprintf("oauth: token endpoint %s returned HTTP %d", e.Endpoint, e.Status)
}

// IsInvalidGrant reports whether a TokenError carries the RFC 6749
// invalid_grant error code — the signal that the refresh token is dead
// and the user needs to re-authenticate.
func (e *TokenError) IsInvalidGrant() bool {
	return e != nil && strings.EqualFold(e.Code, "invalid_grant")
}

// registerSecretsWithRedact marks the literal access and refresh token
// values for masking. Both are persisted in plaintext on disk; without
// this they would not match redact's pattern catalog and could leak into
// logs and exports.
func registerSecretsWithRedact(access, refresh string) {
	if access != "" {
		redact.RegisterSecret(access)
	}
	if refresh != "" {
		redact.RegisterSecret(refresh)
	}
}
