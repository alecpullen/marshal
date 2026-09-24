package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// dcrTimeout bounds a single registration request. Registration is a
// one-shot handshake per server, so we are happy to wait a little but not
// the full loopback timeout.
const dcrTimeout = 15 * time.Second

// dcrResponse is the subset of RFC 7591 we use. Some servers return a
// client_secret even though we requested "none"; we keep it around in case
// a future server requires it. We intentionally keep only the fields we
// need and let unknown fields fall through.
type dcrResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
}

// dcrRequest is the RFC 7591 client registration request body.
type dcrRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
}

// dcrHTTPClient is the minimum interface RegisterClient needs. Tests
// supply an httptest.Server-backed client; production uses an injected
// *http.Client on the engine.
type dcrHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// RegisterClient performs RFC 7591 dynamic client registration against
// the given endpoint. It returns the server-issued client_id; an empty
// client_id means the server declined to register us.
//
// The supplied redirectURI is the loopback URL the engine just bound;
// clientName is the human-readable label sent in the request (defaults
// to "marshal" if empty). tokenEndpointAuthMethod is sent as "none" per
// the MCP authorization spec, but tests can override it.
func RegisterClient(ctx context.Context, hc dcrHTTPClient, endpoint, redirectURI, clientName string) (dcrResponse, error) {
	if endpoint == "" {
		return dcrResponse{}, errors.New("oauth: registration endpoint is empty")
	}
	if clientName == "" {
		clientName = "marshal"
	}
	req := dcrRequest{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              clientName,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return dcrResponse{}, fmt.Errorf("oauth: marshal registration request: %w", err)
	}

	dctx, cancel := context.WithTimeout(ctx, dcrTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(dctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return dcrResponse{}, fmt.Errorf("oauth: build registration request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := hc.Do(httpReq)
	if err != nil {
		return dcrResponse{}, fmt.Errorf("oauth: register client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return dcrResponse{}, fmt.Errorf("oauth: registration endpoint %s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var raw dcrResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return dcrResponse{}, fmt.Errorf("oauth: decode registration response: %w", err)
	}
	if raw.ClientID == "" {
		return dcrResponse{}, errors.New("oauth: registration response missing client_id")
	}
	return raw, nil
}
