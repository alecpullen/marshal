package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/credentials"
)

type memStore struct {
	mu       sync.Mutex
	data     map[string][]byte
	setCalls int
	delCalls int
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Get(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		// Mirror credentials.Store's documented contract: a missing key is
		// reported with ErrNotFound, not as a silent empty result. Returning
		// nil here would let a caller that treats "not found" as a hard
		// failure pass its tests while breaking in production.
		return nil, fmt.Errorf("%w: %s", credentials.ErrNotFound, key)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memStore) Set(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls++
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delCalls++
	delete(m.data, key)
	return nil
}

func mustSetJSON(t *testing.T, s *memStore, key string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := s.Set(key, raw); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
}

type oauthErrorBody struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

type fakeAS struct {
	server *httptest.Server
	mu     sync.Mutex

	registerN int
	tokenN    int

	suppressRFC8414 bool

	nextAccessToken  string
	nextRefreshToken string
	nextExpiresIn    int

	refreshErr *oauthErrorBody
}

func newFakeAS(t *testing.T) *fakeAS {
	t.Helper()
	fa := &fakeAS{
		nextAccessToken:  "access-token-A",
		nextRefreshToken: "refresh-token-A",
		nextExpiresIn:    3600,
	}
	var meta AuthorizeServerMetadata
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fa.serveHTTP(w, r, meta)
	}))
	fa.server = srv
	t.Cleanup(srv.Close)
	meta = AuthorizeServerMetadata{
		Issuer:                        srv.URL,
		AuthorizationEndpoint:         srv.URL + "/authorize",
		TokenEndpoint:                 srv.URL + "/token",
		RegistrationEndpoint:          srv.URL + "/register",
		CodeChallengeMethodsSupported: []string{"S256"},
	}
	return fa
}

func (fa *fakeAS) serveHTTP(w http.ResponseWriter, r *http.Request, m AuthorizeServerMetadata) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/.well-known/oauth-authorization-server"),
		strings.HasSuffix(path, "/.well-known/openid-configuration"):
		if fa.suppressRFC8414 && strings.HasSuffix(path, "/.well-known/oauth-authorization-server") {
			http.NotFound(w, r)
			return
		}
		fa.writeMetadata(w, m)
	case strings.HasSuffix(path, "/register"):
		fa.registerN++
		var req dcrRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dcrResponse{
			ClientID:                "client-DCR-1",
			TokenEndpointAuthMethod: "none",
			RedirectURIs:            req.RedirectURIs,
			GrantTypes:              req.GrantTypes,
			ClientName:              req.ClientName,
		})
	case strings.HasSuffix(path, "/token"):
		fa.tokenN++
		_ = r.ParseForm()
		grant := r.PostForm.Get("grant_type")
		if fa.refreshErr != nil && grant == "refresh_token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(fa.refreshErr)
			return
		}
		access := fa.nextAccessToken
		refresh := fa.nextRefreshToken
		exp := fa.nextExpiresIn
		if exp == 0 {
			exp = 3600
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  access,
			RefreshToken: refresh,
			ExpiresIn:    exp,
			TokenType:    "Bearer",
		})
	default:
		http.NotFound(w, r)
	}
}

func (fa *fakeAS) writeMetadata(w http.ResponseWriter, m AuthorizeServerMetadata) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                m.Issuer,
		"authorization_endpoint":                m.AuthorizationEndpoint,
		"token_endpoint":                        m.TokenEndpoint,
		"registration_endpoint":                 m.RegistrationEndpoint,
		"code_challenge_methods_supported":      m.CodeChallengeMethodsSupported,
		"token_endpoint_auth_methods_supported": m.TokenEndpointAuthMethodsSupported,
	})
}

type testDisplay struct {
	mu     sync.Mutex
	url    string
	showCh chan struct{}
	waitCh chan error
}

func newTestDisplay() *testDisplay {
	return &testDisplay{
		showCh: make(chan struct{}, 1),
		waitCh: make(chan error, 1),
	}
}

func (d *testDisplay) ShowURL(u string) {
	d.mu.Lock()
	d.url = u
	d.mu.Unlock()
	select {
	case d.showCh <- struct{}{}:
	default:
	}
}

func (d *testDisplay) Wait(ctx context.Context) error {
	select {
	case err := <-d.waitCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *testDisplay) URL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url
}

func driveCallback(t *testing.T, d *testDisplay, as *fakeAS, code string) {
	t.Helper()
	go func() {
		<-d.showCh
		u, _ := url.Parse(d.URL())
		redirect := u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		cb := redirect + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
		req, _ := http.NewRequest(http.MethodGet, cb, nil)
		resp, err := as.server.Client().Do(req)
		if err != nil {
			d.waitCh <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		d.waitCh <- nil
	}()
}

func driveCallbackWithWrongState(t *testing.T, d *testDisplay, as *fakeAS, code string) {
	t.Helper()
	go func() {
		<-d.showCh
		u, _ := url.Parse(d.URL())
		redirect := u.Query().Get("redirect_uri")
		cb := redirect + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape("WRONG")
		req, _ := http.NewRequest(http.MethodGet, cb, nil)
		resp, err := as.server.Client().Do(req)
		if err != nil {
			d.waitCh <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		d.waitCh <- nil
	}()
}

type quietDiscardHandler struct{}

func (quietDiscardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (quietDiscardHandler) Handle(context.Context, slog.Record) error { return nil }
func (quietDiscardHandler) WithAttrs([]slog.Attr) slog.Handler        { return quietDiscardHandler{} }
func (quietDiscardHandler) WithGroup(string) slog.Handler             { return quietDiscardHandler{} }

func engineWithAS(t *testing.T, as *fakeAS, store *memStore, opts ...func(*Engine)) *Engine {
	t.Helper()
	e := &Engine{
		ServerURL:  as.server.URL,
		ClientName: "marshal",
		Store:      store,
		HTTPClient: as.server.Client(),
		Logger:     slog.New(quietDiscardHandler{}),
		CacheDir:   t.TempDir(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }

var _ = time.Second

// ---- Tests ----

func TestAuthorize_HappyPath(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store)

	display := newTestDisplay()
	driveCallback(t, display, as, "CODE-good")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := e.Authorize(ctx, display); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	as.mu.Lock()
	registerN, tokenN := as.registerN, as.tokenN
	as.mu.Unlock()
	if registerN != 1 {
		t.Fatalf("register calls: got %d want 1", registerN)
	}
	if tokenN != 1 {
		t.Fatalf("token calls: got %d want 1", tokenN)
	}

	u, err := url.Parse(display.URL())
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("expected S256, got %q", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Fatalf("code_challenge missing")
	}
	if q.Get("state") == "" {
		t.Fatalf("state missing")
	}
	if q.Get("client_id") != "client-DCR-1" {
		t.Fatalf("expected client_id from DCR, got %q", q.Get("client_id"))
	}

	raw, err := store.Get(e.storageKey())
	if err != nil || len(raw) == 0 {
		t.Fatalf("token store empty after Authorize: err=%v", err)
	}
	var stored StoredTokens
	if err := decodeJSON(raw, &stored); err != nil {
		t.Fatalf("decode stored tokens: %v", err)
	}
	if stored.AccessToken != "access-token-A" {
		t.Fatalf("access token stored: got %q", stored.AccessToken)
	}
	if stored.RefreshToken != "refresh-token-A" {
		t.Fatalf("refresh token stored: got %q", stored.RefreshToken)
	}
	if stored.ClientID != "client-DCR-1" {
		t.Fatalf("client_id stored: got %q", stored.ClientID)
	}
	if stored.ExpiresAt.IsZero() {
		t.Fatalf("expires_at was not set")
	}
}

func TestMetadata_FallbackToOIDC(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.suppressRFC8414 = true
	as.mu.Unlock()
	store := newMemStore()
	e := engineWithAS(t, as, store)
	tokens, err := e.metadataCache().Lookup(context.Background(), e.ServerURL)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if tokens.AuthorizationEndpoint == "" || tokens.TokenEndpoint == "" {
		t.Fatalf("endpoints missing from fallback: %+v", tokens)
	}
}

func TestAuthorize_RegistersPerFlow(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store)

	// A cached client_id from an earlier flow must NOT suppress
	// registration: the redirect_uri carries a per-flow loopback port,
	// so a strict authorization server would reject a client that was
	// registered for a different port.
	preset := StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ClientID:     "client-CACHED",
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuedAt:     time.Now(),
	}
	mustSetJSON(t, store, e.storageKey(), preset)

	display := newTestDisplay()
	driveCallback(t, display, as, "CODE-2")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := e.Authorize(ctx, display); err != nil {
		t.Fatalf("Authorize (cached): %v", err)
	}

	as.mu.Lock()
	registerN := as.registerN
	as.mu.Unlock()
	if registerN != 1 {
		t.Fatalf("expected 1 register call (re-register per flow), got %d", registerN)
	}
	u, err := url.Parse(display.URL())
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	if got := u.Query().Get("client_id"); got == "" || got == "client-CACHED" {
		t.Fatalf("authorization URL should use the freshly registered client_id; got %q", got)
	}
}

func TestAuthorize_StateMismatch(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store)

	display := newTestDisplay()
	driveCallbackWithWrongState(t, display, as, "CODE-state-bad")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := e.Authorize(ctx, display)
	if err == nil {
		t.Fatalf("Authorize with mismatched state must error")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Fatalf("expected state-related error, got %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("no tokens should be stored on state-mismatch failure; got %d sets", store.setCalls)
	}
}

func TestTokenSource_RefreshOnExpiry(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.nextAccessToken = "access-REFRESHED"
	as.nextRefreshToken = "refresh-REFRESHED"
	as.nextExpiresIn = 7200
	as.mu.Unlock()

	store := newMemStore()
	e := engineWithAS(t, as, store)

	expired := StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ClientID:     "client-CACHED",
		ExpiresAt:    time.Now().Add(-time.Hour),
		IssuedAt:     time.Now().Add(-2 * time.Hour),
	}
	mustSetJSON(t, store, e.storageKey(), expired)

	token, err := e.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if token != "access-REFRESHED" {
		t.Fatalf("refreshed access token: got %q", token)
	}

	as.mu.Lock()
	tokenN := as.tokenN
	as.mu.Unlock()
	if tokenN != 1 {
		t.Fatalf("expected 1 token-endpoint call, got %d", tokenN)
	}

	raw, _ := store.Get(e.storageKey())
	var stored StoredTokens
	if err := decodeJSON(raw, &stored); err != nil {
		t.Fatalf("decode stored: %v", err)
	}
	if stored.AccessToken != "access-REFRESHED" {
		t.Fatalf("stored access token after refresh: got %q", stored.AccessToken)
	}
	if stored.RefreshToken != "refresh-REFRESHED" {
		t.Fatalf("stored refresh token after refresh: got %q", stored.RefreshToken)
	}
}

func TestTokenSource_InvalidGrant(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.refreshErr = &oauthErrorBody{Code: "invalid_grant", Description: "refresh expired"}
	as.mu.Unlock()

	store := newMemStore()
	e := engineWithAS(t, as, store)

	expired := StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ClientID:     "client-CACHED",
		ExpiresAt:    time.Now().Add(-time.Hour),
		IssuedAt:     time.Now().Add(-2 * time.Hour),
	}
	mustSetJSON(t, store, e.storageKey(), expired)

	_, err := e.TokenSource(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrAuthSentinel) {
		t.Fatalf("expected ErrAuthRequired, got %v", err)
	}
	var ear *ErrAuthRequired
	if !errors.As(err, &ear) {
		t.Fatalf("ErrAuthRequired should be in chain")
	}
	if ear.Reason != "refresh rejected" {
		t.Fatalf("reason: got %q want refresh rejected", ear.Reason)
	}

	store.mu.Lock()
	delCalls, exists := store.delCalls, store.data[e.storageKey()] != nil
	store.mu.Unlock()
	if delCalls != 1 {
		t.Fatalf("expected 1 Delete call, got %d", delCalls)
	}
	if exists {
		t.Fatalf("stored entry should be removed after invalid_grant")
	}
}

func TestTokenSource_NoStoredTokens(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store)
	_, err := e.TokenSource(context.Background())
	if err == nil || !errors.Is(err, ErrAuthSentinel) {
		t.Fatalf("expected ErrAuthRequired, got %v", err)
	}
	var ear *ErrAuthRequired
	if !errors.As(err, &ear) || ear.Reason != "no stored tokens" {
		t.Fatalf("expected reason=no stored tokens, got %v", err)
	}
}

// TestTokenSource_MissingKeyIsNotAnError guards the contract between the
// engine and credentials.Store: Get reports a missing key with ErrNotFound,
// and that is the normal "not authorized yet" state. A store that models the
// real one (memStore does) must yield ErrAuthRequired with reason "no stored
// tokens" -- not a wrapped "read stored tokens" failure.
func TestTokenSource_MissingKeyIsNotAnError(t *testing.T) {
	as := newFakeAS(t)
	e := engineWithAS(t, as, newMemStore())

	_, err := e.TokenSource(context.Background())
	if err == nil {
		t.Fatalf("expected ErrAuthRequired, got nil")
	}
	if strings.Contains(err.Error(), "read stored tokens") {
		t.Fatalf("a missing entry must not surface as a read failure: %v", err)
	}
	var ear *ErrAuthRequired
	if !errors.As(err, &ear) {
		t.Fatalf("expected ErrAuthRequired in chain, got %v", err)
	}
	if ear.Reason != "no stored tokens" {
		t.Fatalf("reason: got %q want %q", ear.Reason, "no stored tokens")
	}
}

// TestMemStoreReportsErrNotFound pins the fixture used by the tests above to
// the real store's contract, so the suite cannot drift back to a fixture that
// hides the missing-key case.
func TestMemStoreReportsErrNotFound(t *testing.T) {
	s := newMemStore()
	_, err := s.Get("marshal:mcp:absent")
	if !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("missing key error = %v, want ErrNotFound", err)
	}
}

func TestAuthorize_Timeout(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store, func(en *Engine) { en.LoopbackTimeout = 100 * time.Millisecond })
	display := newTestDisplay()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := e.Authorize(ctx, display)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("no tokens should be stored on timeout; got %d sets", store.setCalls)
	}
}

func TestAuthorize_CtxCancel(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()
	e := engineWithAS(t, as, store, func(en *Engine) { en.LoopbackTimeout = 30 * time.Second })
	display := newTestDisplay()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := e.Authorize(ctx, display)
	if err == nil {
		t.Fatalf("expected cancel error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled, got %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("no tokens should be stored on cancel; got %d sets", store.setCalls)
	}
}

// ---- Provider seams (StorageKey, FlowConfig) ----

// TestStorageKeyDefaultsToMCP pins the MCP behavior: an engine that does not
// set StorageKey keeps the per-server key, so the extraction is invisible to
// existing MCP installs.
func TestStorageKeyDefaultsToMCP(t *testing.T) {
	e := &Engine{ServerURL: "https://mcp.example.com/mcp"}
	if got, want := e.storageKey(), "marshal:mcp:https://mcp.example.com/mcp"; got != want {
		t.Fatalf("storageKey() = %q, want %q", got, want)
	}
}

// TestStorageKeyOverride pins the provider seam: an explicit StorageKey wins
// over the ServerURL-derived default.
func TestStorageKeyOverride(t *testing.T) {
	e := &Engine{
		ServerURL:  "https://chatgpt.com/backend-api",
		StorageKey: "marshal:provider:codex",
	}
	if got, want := e.storageKey(), "marshal:provider:codex"; got != want {
		t.Fatalf("storageKey() = %q, want %q", got, want)
	}
}

// TestTokenSourceUsesStorageKeyOverride proves the override reaches the store,
// not just the helper: a token written under the provider key is found, and
// nothing is written under the MCP key.
func TestTokenSourceUsesStorageKeyOverride(t *testing.T) {
	store := newMemStore()
	e := &Engine{
		ServerURL:  "https://chatgpt.com/backend-api",
		StorageKey: "marshal:provider:codex",
		Store:      store,
		Logger:     slog.New(quietDiscardHandler{}),
	}
	mustSetJSON(t, store, "marshal:provider:codex", StoredTokens{
		AccessToken: "provider-token",
		ExpiresAt:   time.Now().Add(time.Hour),
	})

	got, err := e.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if got != "provider-token" {
		t.Fatalf("token = %q, want %q", got, "provider-token")
	}
	if _, ok := store.data["marshal:mcp:https://chatgpt.com/backend-api"]; ok {
		t.Fatal("token was read from the MCP key; StorageKey override did not apply")
	}
}

// TestFlowConfigSkipsDiscoveryAndDCR pins the pinned-flow seam: with Issuer
// and ClientID set, Authorize must not touch the discovery or registration
// endpoints at all, and must use the pinned authorize/token URLs.
func TestFlowConfigSkipsDiscoveryAndDCR(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()

	var discoveryHits, registerHits int
	var mu sync.Mutex
	pinned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch {
		case strings.Contains(r.URL.Path, ".well-known"):
			discoveryHits++
		case strings.HasSuffix(r.URL.Path, "/register"):
			registerHits++
		}
		mu.Unlock()
		as.serveHTTP(w, r, AuthorizeServerMetadata{})
	}))
	defer pinned.Close()

	e := &Engine{
		ServerURL:  "https://chatgpt.com/backend-api",
		StorageKey: "marshal:provider:codex",
		ClientName: "marshal",
		Store:      store,
		HTTPClient: pinned.Client(),
		Logger:     slog.New(quietDiscardHandler{}),
		CacheDir:   t.TempDir(),
		Flow: FlowConfig{
			Issuer:       pinned.URL,
			AuthorizeURL: pinned.URL + "/authorize",
			TokenURL:     pinned.URL + "/token",
			ClientID:     "app_fixed_client",
			ExtraAuthorizeParams: map[string]string{
				"originator": "codex_cli_rs",
			},
		},
	}

	display := newTestDisplay()
	// Drive the callback against the pinned server, not the fake AS.
	go func() {
		<-display.showCh
		u, _ := url.Parse(display.URL())
		redirect := u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		cb := redirect + "?code=CODE-pinned&state=" + url.QueryEscape(state)
		req, _ := http.NewRequest(http.MethodGet, cb, nil)
		resp, err := pinned.Client().Do(req)
		if err != nil {
			display.waitCh <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		display.waitCh <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := e.Authorize(ctx, display); err != nil {
		t.Fatalf("Authorize with pinned flow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if discoveryHits != 0 {
		t.Fatalf("discovery was called %d times; a pinned Issuer must skip it", discoveryHits)
	}
	if registerHits != 0 {
		t.Fatalf("dynamic registration was called %d times; a pinned ClientID must skip it", registerHits)
	}

	// The pinned client_id and extra params must appear in the authorize URL.
	u, _ := url.Parse(display.URL())
	if got := u.Query().Get("client_id"); got != "app_fixed_client" {
		t.Fatalf("authorize client_id = %q, want %q", got, "app_fixed_client")
	}
	if got := u.Query().Get("originator"); got != "codex_cli_rs" {
		t.Fatalf("authorize originator = %q, want %q", got, "codex_cli_rs")
	}

	// The token must land under the provider key.
	if _, ok := store.data["marshal:provider:codex"]; !ok {
		t.Fatal("token was not stored under the provider StorageKey")
	}
}

// TestFlowConfigRedirectPorts pins that the configured redirect port is the
// one actually bound, so the redirect_uri matches what the authorization
// server has registered.
func TestFlowConfigRedirectPorts(t *testing.T) {
	as := newFakeAS(t)
	store := newMemStore()

	// Find a free port to hand to the engine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	e := engineWithAS(t, as, store, func(en *Engine) {
		en.Flow = FlowConfig{RedirectPorts: []int{port}}
	})

	display := newTestDisplay()
	driveCallback(t, display, as, "CODE-port")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := e.Authorize(ctx, display); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	u, _ := url.Parse(display.URL())
	redirect := u.Query().Get("redirect_uri")
	want := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	if redirect != want {
		t.Fatalf("redirect_uri = %q, want %q", redirect, want)
	}
}

func TestFlowConfigRegisteredCallbackPath(t *testing.T) {
	as := newFakeAS(t)
	display := newTestDisplay()
	e := engineWithAS(t, as, newMemStore(), func(en *Engine) {
		en.Flow.RedirectPath = "/auth/callback"
	})
	driveCallback(t, display, as, "CODE-path")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := e.Authorize(ctx, display); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	u, _ := url.Parse(display.URL())
	redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
	if redirect.Path != "/auth/callback" {
		t.Fatalf("callback path = %q", redirect.Path)
	}
}

func TestFlowConfigOccupiedRegisteredPortsFail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	e := &Engine{Flow: FlowConfig{RedirectPorts: []int{ln.Addr().(*net.TCPAddr).Port}, RedirectPath: "/auth/callback"}}
	lb, err := e.startLoopback(context.Background(), time.Second)
	if err == nil {
		lb.Close()
		t.Fatal("used an unregistered port when registered ports were occupied")
	}
	if !strings.Contains(err.Error(), "no registered loopback port available") {
		t.Fatalf("unexpected error: %v", err)
	}
}
