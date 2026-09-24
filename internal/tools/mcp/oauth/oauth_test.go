package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
