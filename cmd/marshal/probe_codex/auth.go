//go:build probe_c

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"marshal/internal/oauth"
)

// This file drives the codex authorization flow. It deliberately consumes
// marshal's existing OAuth machinery (now internal/oauth, extracted from
// internal/tools/mcp/oauth in Phase 2) rather than forking it: the spike was
// the proof that the seams the extraction needs actually exist.
//
// Reused verbatim from the engine: oauth.NewPKCE, oauth.NewState,
// oauth.VerifyState. Mirrored (not reused) because codex pins its own
// redirect path and endpoints: the loopback receiver (oauth.StartLoopbackOnPort
// hardcodes /callback; codex's Hydra allow-list registers /auth/callback) and
// the token POST (oauth.Exchange drops unknown response fields such as
// id_token, which the spike must capture raw).

// codexLoopbackTimeout bounds how long auth-poll waits for the browser
// callback. Codex's own device flow uses 15 minutes; we match that.
const codexLoopbackTimeout = 15 * time.Minute

// probeAuthBegin starts the authorization flow, prints the URL (or device
// code) for the user, and persists the in-flight state for auth-poll.
//
// It does NOT block on the callback: the two-command split exists so the user
// can complete the browser step in their own time. The redirect URI is fixed
// (port 1455, falling back to 1457), so releasing and re-binding the port
// between the two commands is safe.
func probeAuthBegin(args []string) error {
	fs := flag.NewFlagSet("auth-begin", flag.ContinueOnError)
	grant := fs.String("grant", "pkce", "authorization grant: pkce (browser + loopback) or device")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := codexRequired("codexClientID", codexClientID); err != nil {
		return err
	}
	if err := codexRequired("codexAuthorizeURL", codexAuthorizeURL); err != nil {
		return err
	}

	switch *grant {
	case "pkce":
		return authBeginPKCE()
	case "device":
		return authBeginDevice()
	default:
		return fmt.Errorf("unknown -grant %q (want pkce or device)", *grant)
	}
}

// authBeginPKCE generates a PKCE pair and CSRF state, picks the loopback
// port, and prints the authorization URL.
func authBeginPKCE() error {
	pkce, err := oauth.NewPKCE()
	if err != nil {
		return err
	}
	state, err := oauth.NewState()
	if err != nil {
		return err
	}

	port, err := pickLoopbackPort()
	if err != nil {
		return err
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, codexRedirectPath)

	authURL, err := buildCodexAuthorizeURL(redirectURI, pkce, state)
	if err != nil {
		return err
	}

	st := flowState{
		Grant:        "pkce",
		Verifier:     pkce.Verifier,
		Challenge:    pkce.Challenge,
		State:        state,
		RedirectURI:  redirectURI,
		AuthorizeURL: authURL,
	}
	if err := saveFlowState(st); err != nil {
		return err
	}

	path, _ := flowStatePath()
	fmt.Println("grant:        pkce (browser + loopback)")
	fmt.Println("redirect_uri: " + redirectURI)
	fmt.Println("client_id:    " + codexClientID)
	fmt.Println("scope:        " + codexScope)
	fmt.Println()
	fmt.Println("Open this URL in a browser and sign in:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	fmt.Printf("Flow state saved to %s (0600).\n", path)
	fmt.Println("Then run: go run -tags probe_c ./cmd/marshal/probe_codex auth-poll")
	return nil
}

// authBeginDevice requests a device user code from codex's bespoke device
// endpoints (NOT RFC 8628 — see the source notes §1).
func authBeginDevice() error {
	if err := codexRequired("codexDeviceUserCodeURL", codexDeviceUserCodeURL); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"client_id": codexClientID})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceUserCodeURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := newSpikeClient().Do(req)
	if err != nil {
		return fmt.Errorf("device usercode request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("device usercode request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var uc struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     string `json:"interval"`
	}
	if err := json.Unmarshal(raw, &uc); err != nil {
		return fmt.Errorf("decode device usercode response: %w (body: %s)", err, strings.TrimSpace(string(raw)))
	}
	interval := 5
	if uc.Interval != "" {
		if _, err := fmt.Sscanf(strings.TrimSpace(uc.Interval), "%d", &interval); err != nil {
			interval = 5
		}
	}

	st := flowState{
		Grant:           "device",
		DeviceAuthID:    uc.DeviceAuthID,
		UserCode:        uc.UserCode,
		IntervalSeconds: interval,
		VerificationURL: codexDeviceVerificationURL,
	}
	if err := saveFlowState(st); err != nil {
		return err
	}

	path, _ := flowStatePath()
	fmt.Println("grant:            device (codex bespoke two-endpoint flow)")
	fmt.Println("verification_url: " + codexDeviceVerificationURL)
	fmt.Println("user_code:        " + uc.UserCode)
	fmt.Printf("poll interval:    %ds\n", interval)
	fmt.Println()
	fmt.Printf("Flow state saved to %s (0600).\n", path)
	fmt.Println("Then run: go run -tags probe_c ./cmd/marshal/probe_codex auth-poll")
	return nil
}

// probeAuthPoll completes the flow started by auth-begin: it obtains the
// authorization code (loopback callback or device poll), exchanges it for
// tokens, and stores the raw token response.
func probeAuthPoll(args []string) error {
	fs := flag.NewFlagSet("auth-poll", flag.ContinueOnError)
	timeout := fs.Duration("timeout", codexLoopbackTimeout, "how long to wait for the authorization code")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := loadFlowState()
	if err != nil {
		return err
	}

	var code string
	switch st.Grant {
	case "pkce":
		code, err = pollLoopback(st, *timeout)
	case "device":
		code, err = pollDevice(st, *timeout)
	default:
		return fmt.Errorf("unknown grant %q in saved flow state", st.Grant)
	}
	if err != nil {
		return err
	}

	raw, err := exchangeCodeRaw(st, code)
	if err != nil {
		return err
	}
	if err := saveTokens(raw); err != nil {
		return err
	}
	if err := clearFlowState(); err != nil {
		return err
	}

	path, _ := tokenPath()
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	fmt.Printf("token response keys: %v\n", keys)
	if tok, _ := raw["access_token"].(string); tok != "" {
		fmt.Printf("access_token: %s\n", redactToken(tok))
	}
	if tok, _ := raw["refresh_token"].(string); tok != "" {
		fmt.Printf("refresh_token: %s (present)\n", redactToken(tok))
	} else {
		fmt.Println("refresh_token: ABSENT")
	}
	if tok, _ := raw["id_token"].(string); tok != "" {
		fmt.Printf("id_token: %s (present)\n", redactToken(tok))
	} else {
		fmt.Println("id_token: ABSENT")
	}
	fmt.Printf("tokens stored at %s (0600)\n", path)
	return nil
}

// pickLoopbackPort returns codex's preferred redirect port, falling back to
// the registered fallback port when it is already in use. Mirrors
// bind_server in codex-rs/login/src/server.rs.
func pickLoopbackPort() (int, error) {
	for _, port := range []int{codexRedirectPort, codexRedirectFallbackPort} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("neither redirect port %d nor %d is available", codexRedirectPort, codexRedirectFallbackPort)
}

// buildCodexAuthorizeURL composes the authorization request with codex's
// pinned parameters (source notes §1). It mirrors oauth.buildAuthorizeURL
// (unexported) and adds the codex-specific extras.
func buildCodexAuthorizeURL(redirectURI string, pkce oauth.PKCE, state string) (string, error) {
	u, err := url.Parse(codexAuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", codexClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", codexScope)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", pkce.Method)
	q.Set("state", state)
	// codex-rs/login/src/server.rs:594-601
	q.Set(codexParamIDTokenAddOrganizations, "true")
	q.Set(codexParamSimplifiedFlow, "true")
	q.Set(codexParamOriginator, codexOriginator)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// pollLoopback binds the saved redirect port and waits for the single
// authorization callback. Mirrors oauth.StartLoopbackOnPort but with codex's
// /auth/callback path.
func pollLoopback(st flowState, timeout time.Duration) (string, error) {
	u, err := url.Parse(st.RedirectURI)
	if err != nil {
		return "", fmt.Errorf("parse saved redirect_uri: %w", err)
	}
	port := u.Port()
	if port == "" {
		return "", fmt.Errorf("saved redirect_uri %q has no port", st.RedirectURI)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return "", fmt.Errorf("bind loopback listener on %s: %w", st.RedirectURI, err)
	}

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(codexRedirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := result{}
		if errMsg := q.Get("error"); errMsg != "" {
			desc := q.Get("error_description")
			res.err = fmt.Errorf("authorization server returned %s: %s", errMsg, desc)
		} else if q.Get("code") == "" {
			res.err = errors.New("callback missing code parameter")
		} else if !oauth.VerifyState(st.State, q.Get("state")) {
			res.err = errors.New("callback state did not match — possible CSRF, aborting")
		} else {
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<html><body><h1>Authorization failed</h1><p>%s</p></body></html>", res.err)
		} else {
			fmt.Fprint(w, "<html><body><h1>Authorization complete</h1><p>You can close this tab.</p></body></html>")
		}
		select {
		case resultCh <- res:
		default:
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	fmt.Printf("waiting for the browser callback on %s (timeout %s)...\n", st.RedirectURI, timeout)
	select {
	case res := <-resultCh:
		return res.code, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out after %s waiting for the callback", timeout)
	}
}

// pollDevice polls codex's device token endpoint until it issues an
// authorization code. Mirrors poll_for_token in
// codex-rs/login/src/device_code_auth.rs: 403/404 mean "keep polling".
func pollDevice(st flowState, timeout time.Duration) (string, error) {
	if err := codexRequired("codexDeviceTokenURL", codexDeviceTokenURL); err != nil {
		return "", err
	}
	interval := st.IntervalSeconds
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(timeout)

	for {
		body, err := json.Marshal(map[string]string{
			"device_auth_id": st.DeviceAuthID,
			"user_code":      st.UserCode,
		})
		if err != nil {
			return "", err
		}

		ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceTokenURL, strings.NewReader(string(body)))
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := newSpikeClient().Do(req)
		if err != nil {
			cancel()
			return "", fmt.Errorf("device token poll: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			var ok struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeChallenge     string `json:"code_challenge"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(raw, &ok); err != nil {
				return "", fmt.Errorf("decode device token response: %w (body: %s)", err, strings.TrimSpace(string(raw)))
			}
			if ok.AuthorizationCode == "" {
				return "", fmt.Errorf("device token response had no authorization_code: %s", strings.TrimSpace(string(raw)))
			}
			// The device flow's PKCE pair is server-supplied; persist it so
			// the exchange uses the matching verifier.
			st.Verifier = ok.CodeVerifier
			st.Challenge = ok.CodeChallenge
			if err := saveFlowState(st); err != nil {
				return "", err
			}
			return ok.AuthorizationCode, nil
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("device auth timed out after %s", timeout)
			}
			fmt.Printf("  pending (HTTP %d), retrying in %ds...\n", resp.StatusCode, interval)
			time.Sleep(time.Duration(interval) * time.Second)
		default:
			return "", fmt.Errorf("device auth failed with HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
	}
}

// exchangeCodeRaw performs the authorization-code exchange and returns the
// raw token-endpoint JSON. The request shape is oauth.Exchange's verbatim
// (form-encoded, grant_type/code/redirect_uri/client_id/code_verifier); the
// raw map is kept because oauth.StoredTokens drops id_token, which the spike
// must record.
func exchangeCodeRaw(st flowState, code string) (map[string]any, error) {
	if err := codexRequired("codexTokenURL", codexTokenURL); err != nil {
		return nil, err
	}
	redirectURI := st.RedirectURI
	if st.Grant == "device" {
		redirectURI = codexDeviceRedirectURI
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", codexClientID)
	form.Set("code_verifier", st.Verifier)

	ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := newSpikeClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token exchange returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode token response: %w (body: %s)", err, strings.TrimSpace(string(raw)))
	}
	if _, ok := out["access_token"].(string); !ok {
		return nil, fmt.Errorf("token response has no access_token: %s", strings.TrimSpace(string(raw)))
	}
	return out, nil
}

// probeDecodeToken decodes the stored access token as a JWT without
// verification and prints its claims, the account-id claim, and expiry.
//
// Signature verification is deliberately skipped: the probe is not a
// consumer of the token, it is observing its shape. If the access token is
// opaque (not a JWT) that is itself a finding and is reported as such.
func probeDecodeToken() error {
	tok, err := accessToken()
	if err != nil {
		return err
	}
	fmt.Printf("access_token: %s (len %d)\n", redactToken(tok), len(tok))

	claims, err := decodeJWTClaims(tok)
	if err != nil {
		fmt.Printf("access token is NOT a decodable JWT: %v\n", err)
		fmt.Println("FINDING: access token is opaque, not a JWT")
		return nil
	}

	names := make([]string, 0, len(claims))
	for k := range claims {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Printf("claim names (%d): %v\n", len(names), names)

	if exp, ok := claims["exp"]; ok {
		fmt.Printf("exp: %v", exp)
		if f, ok := exp.(float64); ok {
			fmt.Printf(" (%s)", time.Unix(int64(f), 0).UTC().Format(time.RFC3339))
		}
		fmt.Println()
	} else {
		fmt.Println("exp: ABSENT")
	}
	if iat, ok := claims["iat"]; ok {
		fmt.Printf("iat: %v", iat)
		if f, ok := iat.(float64); ok {
			fmt.Printf(" (%s)", time.Unix(int64(f), 0).UTC().Format(time.RFC3339))
		}
		fmt.Println()
	} else {
		fmt.Println("iat: ABSENT")
	}

	// The account id lives under the namespaced auth claim (source notes §3).
	if auth, ok := claims[codexAuthClaimPath].(map[string]any); ok {
		authNames := make([]string, 0, len(auth))
		for k := range auth {
			authNames = append(authNames, k)
		}
		sort.Strings(authNames)
		fmt.Printf("claims under %q: %v\n", codexAuthClaimPath, authNames)
		if acct, ok := auth[codexAccountIDClaim]; ok {
			fmt.Printf("%s: %v\n", codexAccountIDClaim, acct)
		} else {
			fmt.Printf("%s: ABSENT under %q\n", codexAccountIDClaim, codexAuthClaimPath)
		}
	} else {
		fmt.Printf("claims under %q: ABSENT\n", codexAuthClaimPath)
	}

	// The id_token carries the same claim path; report it too when present.
	if raw, err := loadTokens(); err == nil {
		if idTok, _ := raw["id_token"].(string); idTok != "" {
			if idClaims, err := decodeJWTClaims(idTok); err == nil {
				if auth, ok := idClaims[codexAuthClaimPath].(map[string]any); ok {
					if acct, ok := auth[codexAccountIDClaim]; ok {
						fmt.Printf("id_token %s: %v\n", codexAccountIDClaim, acct)
					}
				}
			}
		}
	}
	return nil
}

// decodeJWTClaims base64url-decodes the payload segment of a JWT and returns
// its claims. No signature verification is performed (see probeDecodeToken).
func decodeJWTClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("expected 3 dot-separated segments, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("base64url-decode payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode payload JSON: %w", err)
	}
	return claims, nil
}

// accountIDFromToken extracts the ChatGPT account id from a JWT, preferring
// the id_token (where codex reads it) and falling back to the access token.
func accountIDFromToken() string {
	raw, err := loadTokens()
	if err != nil {
		return ""
	}
	for _, key := range []string{"id_token", "access_token"} {
		tok, _ := raw[key].(string)
		if tok == "" {
			continue
		}
		claims, err := decodeJWTClaims(tok)
		if err != nil {
			continue
		}
		auth, ok := claims[codexAuthClaimPath].(map[string]any)
		if !ok {
			continue
		}
		if acct, ok := auth[codexAccountIDClaim].(string); ok && acct != "" {
			return acct
		}
	}
	return ""
}
