package tui

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

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
)

// lastMessage returns the text of the most recent system message.
func lastMessage(t *testing.T, m Model) string {
	t.Helper()
	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages recorded")
	}
	return msgs[len(msgs)-1].Content
}

// TestMCPAuthDoneReconnectsRuntime pins the fix for the post-auth reconnect
// gap: after Authorize stores a token, the runtime must be rebuilt so the
// server that failed to start with ErrAuthRequired is connected and its tools
// are registered. Without the reload the command reports success while the
// server stays unreachable until a restart.
func TestMCPAuthDoneReconnectsRuntime(t *testing.T) {
	m := newTestModel(t)
	m.mcpAuthServer = "linear"

	var reloaded []config.Config
	m.configReloader = func(cfg config.Config) error {
		reloaded = append(reloaded, cfg)
		return nil
	}

	updated, _ := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)

	if len(reloaded) != 1 {
		t.Fatalf("successful auth should trigger exactly one runtime reload, got %d", len(reloaded))
	}
	msg := lastMessage(t, m)
	if !strings.Contains(msg, "Authorized MCP server") {
		t.Fatalf("expected success message, got %q", msg)
	}
	if m.mcpAuthServer != "" {
		t.Fatalf("mcpAuthServer should be cleared, got %q", m.mcpAuthServer)
	}
}

// TestMCPAuthDoneReloadFailureIsReported: a failed rebuild must not be
// reported as a clean success. The user needs to know the server is still
// unreachable even though the token was stored.
func TestMCPAuthDoneReloadFailureIsReported(t *testing.T) {
	m := newTestModel(t)
	m.mcpAuthServer = "linear"
	m.configReloader = func(config.Config) error { return errors.New("provider build failed") }

	updated, _ := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)

	msg := lastMessage(t, m)
	if !strings.HasPrefix(msg, "\u2717") {
		t.Fatalf("reload failure should be reported, got %q", msg)
	}
	if !strings.Contains(msg, "linear") || !strings.Contains(msg, "provider build failed") {
		t.Fatalf("failure message should name the server and the cause, got %q", msg)
	}
}

// TestMCPAuthDoneNoReconnectOnNonSuccess: a cancelled or failed flow persists
// no token, so rebuilding the runtime would be pointless churn.
func TestMCPAuthDoneNoReconnectOnNonSuccess(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"cancelled", context.Canceled},
		{"failed", errors.New("exchange failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mcpAuthServer = "linear"
			reloads := 0
			m.configReloader = func(config.Config) error {
				reloads++
				return nil
			}

			updated, _ := m.Update(mcpauth.AuthDoneMsg{Err: tt.err})
			m = asModel(t, updated)

			if reloads != 0 {
				t.Fatalf("no reload expected when auth did not succeed, got %d", reloads)
			}
		})
	}
}

// TestReconnectKeepsAFreshMCPFailureNotice pins the ordering inside
// reconnectMCPServer. The notices must be cleared BEFORE the rebuild, because
// a rebuild whose MCP server still fails to start sets its own notice; clearing
// afterwards would wipe that notice and tell the user the reconnect worked when
// the server is still unreachable.
func TestReconnectKeepsAFreshMCPFailureNotice(t *testing.T) {
	m := newTestModel(t)
	// The stale notice the flow is supposed to resolve.
	m.state.SetNotice(session.Notice{
		Category: session.NoticeConfig,
		Severity: session.SeverityWarn,
		Message:  "MCP server 'linear' needs OAuth — run /mcp auth linear",
		Source:   "mcp",
	})
	// Stand in for the real rebuild: it starts the server, it fails again for
	// a different reason, and buildAgentRunnerWithLock sets a fresh notice.
	m.configReloader = func(config.Config) error {
		m.state.SetNotice(session.Notice{
			Category: session.NoticeConfig,
			Severity: session.SeverityWarn,
			Message:  "MCP server 'linear' is unavailable: 403 forbidden",
			Source:   "mcp",
		})
		return nil
	}

	m.reconnectMCPServer("linear")

	notice, ok := m.state.Notice()
	if !ok {
		t.Fatal("the reconnect must not clear the fresh failure notice")
	}
	if !strings.Contains(notice.Message, "unavailable") {
		t.Fatalf("wanted the post-reload notice, got %q", notice.Message)
	}
	if strings.Contains(notice.Message, "needs OAuth") {
		t.Fatalf("the stale needs-OAuth notice should have been cleared first, got %q", notice.Message)
	}
}

// TestReconnectMCPServerWithoutReloaderIsSafe: a model with no reload path
// (tests, embedded use) must not panic or claim a reconnect happened.
func TestReconnectMCPServerWithoutReloaderIsSafe(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = nil
	before := len(m.state.Messages())

	m.reconnectMCPServer("linear")

	if got := len(m.state.Messages()); got != before {
		t.Fatalf("reconnect without a reloader should add no messages, got %d new", got-before)
	}
}

// ── /mcp auth dependency injection ───────────────────────────────────
//
// Without an injected resolver, `/mcp auth` opens the real OS keychain. Every
// test below supplies an in-memory store and a stub opener instead.

// memAuth is a test AuthResolver: an in-memory store plus a record of the
// services and provider names asked for. storeErr stands in for an unavailable
// keychain.
type memAuth struct {
	store    *credentials.MemStore
	storeErr error
	services []string
	engines  []string
}

func newMemAuth() *memAuth { return &memAuth{store: credentials.NewMemStore()} }

func (a *memAuth) OpenStore(service string) (credentials.Store, error) {
	a.services = append(a.services, service)
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return a.store, nil
}

func (a *memAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.storeErr != nil {
		// Same failure the resolver would report when the OS keychain cannot
		// be opened, so a provider built through it fails here.
		return nil, a.storeErr
	}
	return provider.NewCodexOAuthEngineWithStore(name, a.store)
}

// urlRecorder collects browser-open attempts. The engine calls the opener from
// its own goroutine, so access is mutex-guarded: the race detector runs this
// package and an unguarded slice would be flagged.
type urlRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (o *urlRecorder) record(u string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.urls = append(o.urls, u)
}

func (o *urlRecorder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.urls...)
}

// mcpAuthModel returns a model wired to isolated auth/browser dependencies and
// with one OAuth-enabled MCP server configured.
func mcpAuthModel(t *testing.T, auth provider.AuthResolver, open func(string) error) Model {
	t.Helper()
	m := newTestModel(t)
	m.auth = auth
	m.openBrowser = open
	m.state.Config.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: "https://mcp.example.com/sse", Auth: "oauth", Trust: "unrestricted"},
	}
	return m
}

// TestMCPAuthUsesInjectedStore: /mcp auth must take its token store from the
// runtime's resolver. Reaching credentials.New would open the developer's real
// keychain.
func TestMCPAuthUsesInjectedStore(t *testing.T) {
	auth := newMemAuth()
	opened := &urlRecorder{}
	m := mcpAuthModel(t, auth, func(u string) error { opened.record(u); return nil })

	updated, _ := m.handleMCPCommand([]string{"auth", "linear"})
	m = asModel(t, updated)

	if len(auth.services) != 1 || auth.services[0] != "marshal" {
		t.Fatalf("store requests = %v, want one for \"marshal\"", auth.services)
	}
	if m.mcpAuthServer != "linear" {
		t.Fatalf("mcpAuthServer = %q, want the flow to be in flight for linear", m.mcpAuthServer)
	}
	if got := opened.snapshot(); len(got) != 0 {
		t.Fatalf("opening the browser happens inside Authorize, not at command start; got %v", got)
	}
	if _, ok := m.dock.Panel().(*mcpauth.Panel); !ok {
		t.Fatalf("dock panel = %T, want *mcpauth.Panel", m.dock.Panel())
	}
}

// TestMCPAuthUnavailableStorePropagates: when the store cannot be opened the
// existing guidance must be shown — and no authorization flow may start.
func TestMCPAuthUnavailableStorePropagates(t *testing.T) {
	auth := newMemAuth()
	auth.storeErr = credentials.ErrKeyringUnavailable
	m := mcpAuthModel(t, auth, func(string) error { return nil })

	updated, cmd := m.handleMCPCommand([]string{"auth", "linear"})
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatal("no authorization command may start when the store is unavailable")
	}
	msg := lastMessage(t, m)
	if !strings.Contains(msg, "OS keychain") {
		t.Fatalf("message = %q, want the keychain-unavailable guidance", msg)
	}
	if m.mcpAuthServer != "" {
		t.Fatalf("mcpAuthServer = %q, want the flow to be marked not running", m.mcpAuthServer)
	}
}

// TestMCPAuthOtherStoreErrorPropagates: a non-keychain failure takes the
// generic branch and still reports the cause.
func TestMCPAuthOtherStoreErrorPropagates(t *testing.T) {
	auth := newMemAuth()
	auth.storeErr = errors.New("injected store failure")
	m := mcpAuthModel(t, auth, func(string) error { return nil })

	updated, cmd := m.handleMCPCommand([]string{"auth", "linear"})
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatal("no authorization command may start on a store failure")
	}
	msg := lastMessage(t, m)
	if !strings.Contains(msg, "Cannot open the credential store") ||
		!strings.Contains(msg, "injected store failure") {
		t.Fatalf("message = %q, want the generic store-failure branch naming the cause", msg)
	}
}

// TestMCPAuthInjectedErrorNeverFallsBackToRealStore: an erroring resolver must
// not be retried through the production constructor. The failure surfaces once
// and the flow stays down.
func TestMCPAuthInjectedErrorNeverFallsBackToRealStore(t *testing.T) {
	auth := newMemAuth()
	auth.storeErr = errors.New("injected store failure")
	m := mcpAuthModel(t, auth, func(string) error { return nil })

	updated, _ := m.handleMCPCommand([]string{"auth", "linear"})
	m = asModel(t, updated)

	if len(auth.services) != 1 {
		t.Fatalf("store requests = %v, want exactly one (no fallback retry)", auth.services)
	}
	if m.mcpAuthServer != "" {
		t.Fatal("a failed store must not leave a flow marked in flight")
	}
	if _, ok := m.dock.Panel().(*mcpauth.Panel); ok {
		t.Fatal("a failed store must not dock the authorization panel")
	}
}

// TestMCPAuthAuthorizeCommandUsesStubBrowser is the executed-command test: the
// production engine wiring (mcpOAuthEngine + authorizeMCPCmd) runs against a
// test-owned authorization server, and the assertion is on the URL handed to
// the injected opener — the real browser is never launched and the real
// keychain is never opened.
//
// The engine is built directly because ResolveOAuthServer enforces the https
// remote-endpoint gate that a plain httptest server cannot satisfy; the wiring
// under test (store, opener, cancellation) is exactly the one handleMCPCommand
// uses.
func TestMCPAuthAuthorizeCommandUsesStubBrowser(t *testing.T) {
	// One test-owned authorization server serves discovery, registration, and
	// the token endpoint. Nothing in the flow is allowed to reach the
	// internet.
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

	auth := newMemAuth()
	opened := &urlRecorder{}
	m := mcpAuthModel(t, auth, func(u string) error { opened.record(u); return nil })

	// Build the engine exactly as handleMCPCommand does, but against the
	// test-owned metadata host.
	srv := m.state.Config.MCP.Servers["linear"]
	srv.URL = metadataSrv.URL + "/mcp"
	store, err := m.auth.OpenStore("marshal")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	eng := m.mcpOAuthEngine(srv, store)
	eng.CacheDir = t.TempDir() // never the repo's .marshal/oauth-metadata
	eng.LoopbackTimeout = 10 * time.Second

	disp := mcpauth.NewDisplay()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan mcpauth.AuthDoneMsg, 1)
	cmd := authorizeMCPCmd(ctx, eng, disp, cancel)
	go func() { done <- cmd().(mcpauth.AuthDoneMsg) }()

	// Wait for the flow to reach the browser-open step, then cancel it through
	// the same context the panel's Esc handler cancels.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(opened.snapshot()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case msg := <-done:
		if msg.Err != nil && !errors.Is(msg.Err, context.Canceled) {
			t.Fatalf("authorize: %v", msg.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("authorize command did not return after cancellation")
	}

	urls := opened.snapshot()
	if len(urls) != 1 {
		t.Fatalf("opener calls = %v, want exactly one authorization URL", urls)
	}
	if !strings.Contains(urls[0], "/authorize?") {
		t.Fatalf("opened %q, want the authorization endpoint", urls[0])
	}
	// The store is the resolver's in-memory one; a cancelled flow writes no
	// tokens into it.
	if _, err := store.Get("marshal:mcp:" + srv.URL); err == nil {
		t.Fatal("a cancelled flow must not persist tokens")
	}
}
