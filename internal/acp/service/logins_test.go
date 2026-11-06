package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
)

// testAuth is a test AuthResolver: a private in-memory token store, the
// failures a test asks for, and a record of what was asked for. Engines are
// cached by provider name, mirroring provider.scopedAuthResolver: a caller that
// asks twice gets the same engine, so a pinned test engine stays pinned.
type testAuth struct {
	store     *credentials.MemStore
	storeErr  error
	engineErr error
	services  []string
	engines   []string
	cache     map[string]*oauth.Engine
}

func newTestAuth() *testAuth {
	return &testAuth{store: credentials.NewMemStore(), cache: map[string]*oauth.Engine{}}
}

func (a *testAuth) OpenStore(service string) (credentials.Store, error) {
	a.services = append(a.services, service)
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return a.store, nil
}

func (a *testAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.engineErr != nil {
		return nil, a.engineErr
	}
	if eng, ok := a.cache[name]; ok {
		return eng, nil
	}
	eng, err := provider.NewCodexOAuthEngineWithStore(name, a.store)
	if err != nil {
		return nil, err
	}
	a.cache[name] = eng
	return eng, nil
}

func TestPrepareProviderLoginReportsTheResolver(t *testing.T) {
	t.Run("unavailable keychain is coded", func(t *testing.T) {
		auth := newTestAuth()
		auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
		h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

		err := h.PrepareProviderLogin(context.Background(), "codex")
		var coded *acpwire.Error
		if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
			t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
		}
		if coded.Message != auth.engineErr.Error() {
			t.Fatalf("message = %q, want the resolver's %q", coded.Message, auth.engineErr)
		}
		if len(auth.engines) != 1 || auth.engines[0] != "codex" {
			t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
		}
	})

	t.Run("other failures keep their text", func(t *testing.T) {
		auth := newTestAuth()
		auth.engineErr = errors.New("injected engine failure")
		h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

		err := h.PrepareProviderLogin(context.Background(), "codex")
		if !errors.Is(err, auth.engineErr) {
			t.Fatalf("err = %v, want the resolver's error unchanged", err)
		}
	})

	t.Run("a working resolver passes", func(t *testing.T) {
		auth := newTestAuth()
		h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

		if err := h.PrepareProviderLogin(context.Background(), "codex"); err != nil {
			t.Fatalf("PrepareProviderLogin: %v", err)
		}
		if len(auth.engines) != 1 || auth.engines[0] != "codex" {
			t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
		}
	})
}

// TestLoginProviderRunsTheFlow runs the real sign-in through a test-owned
// authorization server, exactly as the wizard's command used to.
func TestLoginProviderRunsTheFlow(t *testing.T) {
	var tokenCalls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-at","refresh_token":"test-rt","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()

	auth := newTestAuth()
	engine := &oauth.Engine{
		ServerURL:  "https://auth.test.invalid",
		Store:      auth.store,
		StorageKey: "marshal:provider:codex-test",
		Flow: oauth.FlowConfig{
			Issuer:        "https://auth.test.invalid",
			AuthorizeURL:  "https://auth.test.invalid/oauth/authorize",
			TokenURL:      tokenSrv.URL,
			ClientID:      "app_test",
			RedirectPorts: []int{0}, // ephemeral port: no fixed-port collision
			RedirectPath:  "/auth/callback",
		},
		CacheDir: t.TempDir(), // never the repo's .marshal/oauth-metadata
	}
	engine.LoopbackTimeout = 10 * time.Second
	auth.cache["codex-test"] = engine

	h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

	var mu sync.Mutex
	var shown []string
	done := make(chan error, 1)
	go func() {
		done <- h.LoginProvider(context.Background(), "codex-test", func(u string) {
			mu.Lock()
			defer mu.Unlock()
			shown = append(shown, u)
		})
	}()

	shownURLs := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), shown...)
	}

	// Drive the loopback callback the way a browser would.
	var callback string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if urls := shownURLs(); len(urls) > 0 {
			callback = urls[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if callback == "" {
		t.Fatal("the flow never reported an authorization URL")
	}
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatalf("authorize URL %q: %v", callback, err)
	}
	if state := u.Query().Get("state"); state != "" {
		redirect := u.Query().Get("redirect_uri")
		resp, err := http.Get(redirect + "?code=test-code&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("loopback callback: %v", err)
		}
		resp.Body.Close()
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoginProvider: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LoginProvider did not return")
	}

	urls := shownURLs()
	if len(urls) != 1 {
		t.Fatalf("show calls = %v, want exactly one", urls)
	}
	if urls[0] != callback {
		t.Fatalf("showed %q, want %q", urls[0], callback)
	}
	if engine.Open != nil {
		t.Fatal("the adapter must leave the engine's Open nil: show reports the URL")
	}
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", tokenCalls)
	}
	if _, err := auth.store.Get("marshal:provider:codex-test"); err != nil {
		t.Fatalf("tokens should be persisted in the adapter's store: %v", err)
	}
}

// TestLoginProviderCodesAnUnavailableKeychain pins the coding Prepare also
// applies: a client that calls LoginProvider alone still gets the error it can
// act on, not an uncoded one.
func TestLoginProviderCodesAnUnavailableKeychain(t *testing.T) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

	err := h.LoginProvider(context.Background(), "codex", func(string) {})
	var coded *acpwire.Error
	if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
		t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
	}
	if coded.Message != auth.engineErr.Error() {
		t.Fatalf("message = %q, want the resolver's %q", coded.Message, auth.engineErr)
	}
	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
	}
}

// TestLoginMCPComposesTheServerAndTheStore drives the seam's own LoginMCP — not
// mcpEngine directly — against a test-owned TLS server whose certificate the
// engine's client does not trust, so the flow stops after resolving the server
// and opening its token store. Plain http would be rejected by
// mcp.ValidateRemoteServer before the store opens. The engine's successful path
// is TestMCPEngineRunsAgainstATestServer's.
func TestLoginMCPComposesTheServerAndTheStore(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	st := newState(t, "s1")
	st.Config.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: srv.URL + "/mcp", Auth: "oauth", Trust: "unrestricted"},
	}
	auth := newTestAuth()
	_, sess := New(Deps{State: st, Auth: auth})

	var shown []string
	err := sess.LoginMCP(context.Background(), "linear", func(u string) { shown = append(shown, u) })
	if err == nil {
		t.Fatal("a server with no OAuth metadata should fail the sign-in")
	}
	if len(shown) != 0 {
		t.Fatalf("show calls = %v, want none without an authorization URL", shown)
	}
	if len(auth.services) != 1 || auth.services[0] != "marshal" {
		t.Fatalf("store requests = %v, want one for \"marshal\"", auth.services)
	}
	if _, err := auth.store.Get("marshal:mcp:" + srv.URL + "/mcp"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("a failed flow stored a token: %v", err)
	}
}

func TestPrepareMCPLoginSentences(t *testing.T) {
	configured := func() *session.State {
		st := newState(t, "s1")
		st.Config.MCP.Servers = map[string]config.MCPServerConfig{
			"linear": {URL: "https://mcp.example.com/sse", Auth: "oauth", Trust: "unrestricted"},
		}
		return st
	}

	t.Run("the server does not resolve", func(t *testing.T) {
		st := configured()
		auth := newTestAuth()
		_, sess := New(Deps{State: st, Auth: auth})

		wantErr := func() error {
			_, err := commands.ResolveOAuthServer(st.Config.MCP.Servers, "absent")
			return err
		}()
		if wantErr == nil {
			t.Fatal("the fixture resolves \"absent\" unexpectedly")
		}
		err := sess.PrepareMCPLogin(context.Background(), "absent")
		if err == nil || err.Error() != wantErr.Error() {
			t.Fatalf("err = %v, want %q", err, wantErr)
		}
		if len(auth.services) != 0 {
			t.Fatalf("store requests = %v, want none before the server resolves", auth.services)
		}
	})

	t.Run("an unavailable keychain is a coded sentence", func(t *testing.T) {
		auth := newTestAuth()
		auth.storeErr = credentials.ErrKeyringUnavailable
		_, sess := New(Deps{State: configured(), Auth: auth})

		err := sess.PrepareMCPLogin(context.Background(), "linear")
		var coded *acpwire.Error
		if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
			t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
		}
		if !strings.Contains(coded.Message, "OS keychain") || !strings.Contains(coded.Message, "/mcp auth linear") {
			t.Fatalf("message = %q, want the keychain guidance naming the server", coded.Message)
		}
	})

	t.Run("another store failure is the generic sentence", func(t *testing.T) {
		auth := newTestAuth()
		auth.storeErr = errors.New("injected store failure")
		_, sess := New(Deps{State: configured(), Auth: auth})

		err := sess.PrepareMCPLogin(context.Background(), "linear")
		if err == nil || err.Error() != "Cannot open the credential store: injected store failure" {
			t.Fatalf("err = %v, want the generic store sentence", err)
		}
	})

	t.Run("a working store passes", func(t *testing.T) {
		auth := newTestAuth()
		_, sess := New(Deps{State: configured(), Auth: auth})

		if err := sess.PrepareMCPLogin(context.Background(), "linear"); err != nil {
			t.Fatalf("PrepareMCPLogin: %v", err)
		}
		if len(auth.services) != 1 || auth.services[0] != "marshal" {
			t.Fatalf("store requests = %v, want one for \"marshal\"", auth.services)
		}
	})
}

// TestMCPEngineRunsAgainstATestServer runs the production engine wiring against
// a test-owned authorization server: the assertion is on the URL handed to
// show, and no browser or real keychain is touched.
func TestMCPEngineRunsAgainstATestServer(t *testing.T) {
	var metadataSrv *httptest.Server
	metadataSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "http://" + r.Host
		switch {
		// The engine derives the RFC 8414 path from the server URL, so an
		// endpoint with a path (/mcp) is probed at /.well-known/…/mcp.
		case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-authorization-server"):
			io.WriteString(w, `{"issuer":"`+base+`","authorization_endpoint":"`+base+`/authorize","token_endpoint":"`+base+`/token","registration_endpoint":"`+base+`/register"}`)
		case r.URL.Path == "/register":
			io.WriteString(w, `{"client_id":"test-client"}`)
		case r.URL.Path == "/token":
			io.WriteString(w, `{"access_token":"test-at","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer metadataSrv.Close()

	store := credentials.NewMemStore()
	srv := config.MCPServerConfig{URL: metadataSrv.URL + "/mcp", Auth: "oauth", Trust: "unrestricted"}
	eng := mcpEngine(srv, store, nil)
	eng.CacheDir = t.TempDir() // never the repo's .marshal/oauth-metadata
	eng.LoopbackTimeout = 10 * time.Second

	var mu sync.Mutex
	var shown []string
	disp := &urlDisplay{show: func(u string) {
		mu.Lock()
		defer mu.Unlock()
		shown = append(shown, u)
	}}
	shownURLs := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), shown...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Authorize(ctx, disp) }()

	// Wait for the flow to reach the browser-open step, then cancel it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(shownURLs()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("authorize: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("authorize did not return after cancellation")
	}

	urls := shownURLs()
	if len(urls) != 1 {
		t.Fatalf("show calls = %v, want exactly one", urls)
	}
	if !strings.Contains(urls[0], "/authorize?") {
		t.Fatalf("showed %q, want an authorization URL", urls[0])
	}
	if _, err := store.Get("marshal:mcp:" + srv.URL); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("a cancelled flow stored a token: %v", err)
	}
}

func TestAClosedSessionStartsNoMCPLogin(t *testing.T) {
	st := newState(t, "s1")
	st.Config.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: "https://mcp.example.com/sse", Auth: "oauth", Trust: "unrestricted"},
	}
	h, old := New(Deps{State: st, Auth: newTestAuth()})
	h.swap(newState(t, "s2"))

	for name, call := range map[string]func() error{
		"PrepareMCPLogin": func() error { return old.PrepareMCPLogin(context.Background(), "linear") },
		"LoginMCP":        func() error { return old.LoginMCP(context.Background(), "linear", func(string) {}) },
	} {
		err := call()
		var coded *acpwire.Error
		if !errors.As(err, &coded) || coded.Code != acpwire.CodeSessionClosed {
			t.Errorf("%s on a closed session = %v, want CodeSessionClosed", name, err)
		}
	}
}
