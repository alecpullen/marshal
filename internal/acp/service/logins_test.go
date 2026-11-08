package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
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

// TestMCPEngineRunsAgainstATestServer runs the production engine wiring against
// a test-owned authorization server: the assertion is on the URL handed to
// show, and no browser or real keychain is touched. It stays here because it
// tests the engine, not the seam.
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

// TestAClosedSessionStartsNoMCPLogin stays here: it drives the in-process
// host's swap, which the seam does not carry.
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
