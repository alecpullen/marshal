package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/oauth"
	"marshal/internal/redact"
	"marshal/internal/tools/registry"
)

func TestManagerRegistersAndInvokesTools(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"mock": {
			Command: exe,
			Args:    []string{"-test.run=TestManagerRegistersAndInvokesTools"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
			Trust:   "unrestricted",
		},
	}

	mgr := NewManager(&cfg)
	if failures := mgr.Start(ctx); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool, ok := reg.Lookup("mcp.mock.hello")
	if !ok {
		t.Fatal("tool mcp.mock.hello not registered")
	}

	if tool.Description != "says hello" {
		t.Errorf("description = %q, want 'says hello'", tool.Description)
	}

	res, err := tool.Handler(ctx, registry.ToolCall{Name: "mcp.mock.hello", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("Handler error: %v", err)
	}

	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("content = %q, want containing 'hello world'", res.Content)
	}
}

// TestRegisterTools_SkipsHangingServer verifies that RegisterTools applies a
// per-server timeout and skips unresponsive servers instead of failing the
// entire registration.
func TestRegisterTools_SkipsHangingServer(t *testing.T) {
	switch {
	case os.Getenv("BE_HANGING_SERVER") == "1":
		hangingServerMain()
		return
	case os.Getenv("BE_MOCK_SERVER") == "1":
		mockServerMain()
		return
	}

	// Shorten timeout so the test doesn't wait 10s per hanging server.
	origTimeout := mcpServerTimeout
	mcpServerTimeout = 100 * time.Millisecond
	defer func() { mcpServerTimeout = origTimeout }()

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"hanging": {
			Command: exe,
			Args:    []string{"-test.run=TestRegisterTools_SkipsHangingServer"},
			Env:     map[string]string{"BE_HANGING_SERVER": "1"},
			Trust:   "unrestricted",
		},
		"good": {
			Command: exe,
			Args:    []string{"-test.run=TestRegisterTools_SkipsHangingServer"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
			Trust:   "unrestricted",
		},
	}

	mgr := NewManager(&cfg)
	if failures := mgr.Start(ctx); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	reg := registry.New()
	start := time.Now()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	elapsed := time.Since(start)

	// Should complete quickly — two servers × 100ms timeout gives ~200ms.
	// Add generous overhead for process startup and shutdown.
	if elapsed > 10*time.Second {
		t.Errorf("RegisterTools took %v, expected < 10s", elapsed)
	}

	// The good server's tool should be registered.
	_, ok := reg.Lookup("mcp.good.hello")
	if !ok {
		t.Error("mcp.good.hello not registered — good server should have succeeded")
	}

	// The hanging server's tool should NOT be registered.
	_, ok = reg.Lookup("mcp.hanging.hello")
	if ok {
		t.Error("mcp.hanging.hello should not be registered — hanging server should have been skipped")
	}
}

// TestManagerStartsRemoteServerAndRegistersTools covers the remote branch of
// Start end to end: a Streamable-HTTP server is configured by URL, its header
// value is env-interpolated, its tools register under mcp.<server>.<tool>,
// and calling one round-trips through the HTTP client.
func TestManagerStartsRemoteServerAndRegistersTools(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	var (
		mu      sync.Mutex
		sawAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			mu.Lock()
			sawAuth = got
			mu.Unlock()
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set(mcpSessionIDHeader, "sess-1")
			result = InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "remote", Version: "1"},
			}
		case "tools/list":
			result = ListToolsResult{Tools: []MCPTool{{
				Name:        "hello",
				Description: "says hello",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}}}
		case "tools/call":
			result = CallToolResult{Content: []MCPContent{{Type: "text", Text: "hello from remote"}}}
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
			return
		}
		raw, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
	}))
	defer srv.Close()

	t.Setenv("MCP_TEST_REMOTE_TOKEN", "resolved-token-value")

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {
			URL:     srv.URL,
			Trust:   "unrestricted",
			Headers: map[string]string{"Authorization": "Bearer $MCP_TEST_REMOTE_TOKEN"},
		},
	}

	mgr := NewManager(&cfg)
	if failures := mgr.Start(context.Background()); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool, ok := reg.Lookup("mcp.remote.hello")
	if !ok {
		t.Fatal("tool mcp.remote.hello not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{Name: "mcp.remote.hello", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "hello from remote") {
		t.Errorf("content = %q, want containing 'hello from remote'", res.Content)
	}

	mu.Lock()
	gotAuth := sawAuth
	mu.Unlock()
	if gotAuth != "Bearer resolved-token-value" {
		t.Errorf("Authorization header = %q, want the env-resolved value", gotAuth)
	}

	// The resolved header value must be registered with the redactor so it is
	// masked in logs and exported transcripts.
	if masked := redact.Secrets("sending resolved-token-value upstream"); strings.Contains(masked, "resolved-token-value") {
		t.Errorf("resolved header value was not registered with the redactor: %q", masked)
	}
}

// stubCaller is a test stub that implements the caller interface.
type stubCaller struct {
	res CallToolResult
	err error
}

func (s *stubCaller) ServerName() string { return "stub" }

func (s *stubCaller) Close() error { return nil }

func (s *stubCaller) Call(ctx context.Context, method string, params, result any) error {
	if s.err != nil {
		return s.err
	}
	data, _ := json.Marshal(s.res)
	return json.Unmarshal(data, result)
}

func TestMakeHandlerPropagatesIsError(t *testing.T) {
	m := NewManager(nil)
	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			IsError: true,
			Content: []MCPContent{{Type: "text", Text: "boom"}},
		},
	}, "server", "any")
	res, err := handler(context.Background(), registry.ToolCall{Name: "any", Args: nil})
	if err == nil {
		t.Fatal("expected error when IsError=true, got nil")
	}
	if res.Error == "" {
		t.Errorf("expected ToolResult.Error to be set, got %q", res.Error)
	}
}

// TestManagerLogsCall verifies that makeHandler logs an "mcp call" line
// with server, tool, duration_ms, and error fields.
func TestManagerLogsCall(t *testing.T) {
	var buf bytes.Buffer
	m := NewManager(nil, WithManagerLogger(slog.New(slog.NewTextHandler(&buf, nil))))

	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			Content: []MCPContent{{Type: "text", Text: "ok"}},
		},
	}, "myserver", "mytool")

	_, err := handler(context.Background(), registry.ToolCall{Name: "mytool", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "mcp call") {
		t.Fatalf("expected 'mcp call' in log, got %q", logged)
	}
	if !strings.Contains(logged, "server=myserver") {
		t.Errorf("expected server=myserver in log, got %q", logged)
	}
	if !strings.Contains(logged, "tool=mytool") {
		t.Errorf("expected tool=mytool in log, got %q", logged)
	}
	if !strings.Contains(logged, "duration_ms=") {
		t.Errorf("expected duration_ms= in log, got %q", logged)
	}
	if !strings.Contains(logged, "error=<nil>") {
		t.Errorf("expected error=<nil> in log, got %q", logged)
	}
}

func TestStartRejectsDangerousEnvKey(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"evil": {
			Command: exe,
			Args:    []string{"-test.run=TestStartRejectsDangerousEnvKey"},
			Env: map[string]string{
				"BE_MOCK_SERVER": "1",
				"LD_PRELOAD":     "/tmp/evil.so",
			},
			Trust: "unrestricted",
		},
	}
	m := NewManager(&cfg)
	failures := m.Start(context.Background())
	if len(failures) == 0 {
		t.Fatal("expected Start to reject LD_PRELOAD env key, got nil")
	}
}

func TestStartRejectsInterpreterEnvKey(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"PYTHONPATH", "NODE_OPTIONS"} {
		t.Run(key, func(t *testing.T) {
			cfg := config.Default()
			cfg.MCP.Servers = map[string]config.MCPServerConfig{
				"evil": {
					Command: exe,
					Args:    []string{"-test.run=TestStartRejectsInterpreterEnvKey"},
					Env: map[string]string{
						"BE_MOCK_SERVER": "1",
						key:              "/tmp/evil",
					},
					Trust: "unrestricted",
				},
			}
			m := NewManager(&cfg)
			failures := m.Start(context.Background())
			if len(failures) == 0 {
				t.Fatalf("expected Start to reject %q env key, got nil", key)
			}
		})
	}
}

func TestStartRejectsSecretEnvKey(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"leaky": {
			Command: exe,
			Args:    []string{"-test.run=TestStartRejectsSecretEnvKey"},
			Env: map[string]string{
				"BE_MOCK_SERVER": "1",
				"MY_API_KEY":     "sk-secret",
			},
			Trust: "unrestricted",
		},
	}
	m := NewManager(&cfg)
	failures := m.Start(context.Background())
	if len(failures) == 0 {
		t.Fatal("expected Start to reject MY_API_KEY env key, got nil")
	}
}

func TestStartRejectsNewlineInEnvValue(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"evil": {
			Command: exe,
			Args:    []string{"-test.run=TestStartRejectsNewlineInEnvValue"},
			Env: map[string]string{
				"BE_MOCK_SERVER": "1",
				"FOO":            "bar\nbaz",
			},
			Trust: "unrestricted",
		},
	}
	m := NewManager(&cfg)
	failures := m.Start(context.Background())
	if len(failures) == 0 {
		t.Fatal("expected Start to reject newline in env value, got nil")
	}
}

func TestStartRejectsUnknownCommandWithoutTrust(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServerConfig{
				"evil": {Command: "/tmp/evil-binary", Args: []string{}},
			},
		},
	}
	m := NewManager(cfg)
	failures := m.Start(context.Background())
	if len(failures) == 0 {
		t.Fatal("expected Start to reject unlisted command without trust flag")
	}
}

func TestStartAcceptsUnknownCommandWithUnrestrictedTrust(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServerConfig{
				"evil": {Command: "/tmp/evil-binary", Args: []string{}, Trust: "unrestricted"},
			},
		},
	}
	m := NewManager(cfg)
	// /tmp/evil-binary doesn't exist, so Start will report an exec-not-found
	// failure. We're asserting the validation step does NOT reject it.
	for _, f := range m.Start(context.Background()) {
		msg := f.Error()
		if strings.Contains(msg, "deny-list") || strings.Contains(msg, "allow-list") {
			t.Fatalf("validation should have passed; got %v", msg)
		}
	}
}

func TestStartAcceptsNpx(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServerConfig{
				"ok": {Command: "npx", Args: []string{"-y", "some-mcp-server"}},
			},
		},
	}
	m := NewManager(cfg)
	// npx may not be installed in the test env; we just want validation to pass.
	for _, f := range m.Start(context.Background()) {
		msg := f.Error()
		if strings.Contains(msg, "deny-list") || strings.Contains(msg, "allow-list") {
			t.Fatalf("validation should have passed; got %v", msg)
		}
	}
}

// One MCP tool with an uncompilable schema must not cost the user the other
// tools from the same server. Registration fails closed per tool (see
// registry.Register), so without the skip the first bad tool aborts the loop.
func TestRegisterToolsSkipsUncompilableSchema(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"mock": {
			Command: exe,
			Args:    []string{"-test.run=TestRegisterToolsSkipsUncompilableSchema"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
			Trust:   "unrestricted",
		},
	}

	mgr := NewManager(&cfg)
	if failures := mgr.Start(ctx); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools = %v, want nil (a bad tool must be skipped, not fatal)", err)
	}

	for _, name := range []string{"mcp.mock.hello", "mcp.mock.goodbye"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("%s was not registered; one bad sibling took down the server", name)
		}
	}
	if _, ok := reg.Lookup("mcp.mock.broken"); ok {
		t.Error("mcp.mock.broken registered despite an uncompilable schema")
	}
}

// One unstartable server must not cost the user a healthy server's tools,
// and must not abort the manager.
func TestStartDegradesOnOneBadServer(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"good": {
			Command: exe,
			Args:    []string{"-test.run=TestStartDegradesOnOneBadServer"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
			Trust:   "unrestricted",
		},
		"bad": {
			// Not in the allow-list and no trust flag: rejected by
			// validateServerCommand before any process is spawned.
			Command: "/nonexistent/marshal-mcp-server",
		},
	}

	mgr := NewManager(&cfg)
	failures := mgr.Start(ctx)
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	if failures[0].Name != "bad" {
		t.Errorf("failed server = %q, want %q", failures[0].Name, "bad")
	}

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if _, ok := reg.Lookup("mcp.good.hello"); !ok {
		t.Error("healthy server's tool was not registered")
	}
}

// hangingServerMain is a minimal MCP server that completes the initialize
// handshake but never responds to tools/list (or any subsequent request),
// simulating a hanging server.
func hangingServerMain() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return
			}
			panic(err)
		}
		if req.Method == "initialize" {
			result := InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "hanging-server", Version: "1.0"},
			}
			res := Response{JSONRPC: "2.0", ID: req.ID}
			data, _ := json.Marshal(result)
			res.Result = data
			_ = enc.Encode(res)
			continue
		}
		// Block forever without responding. The parent will kill this process.
		select {}
	}
}

// TestStartRemoteOAuthWithoutTokenSurfacesAuthRequired covers the OAuth branch
// of startRemote when no token is stored: the initialize POST is answered with
// 401 (no bearer), and the handshake error must still satisfy
// errors.Is(err, oauth.ErrAuthSentinel) after HTTPClient.Start wraps it as
// "initialize handshake: %w". The failure is reported through the
// []ServerFailure path, so a broken OAuth server degrades to unavailable
// rather than taking down the agent.
func TestStartRemoteOAuthWithoutTokenSurfacesAuthRequired(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	// The server rejects any request without a bearer token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0"})
	}))
	defer srv.Close()

	// Substitute an empty in-memory store for the OS keychain through this
	// manager's own opener; no package global is mutated.
	store := credentials.NewMemStore()

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}

	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return store, nil }))
	failures := mgr.Start(context.Background())
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	if !errors.Is(failures[0].Err, oauth.ErrAuthSentinel) {
		t.Errorf("failure error = %v, want errors.Is(err, oauth.ErrAuthSentinel)", failures[0].Err)
	}
	if !strings.Contains(failures[0].Error(), "authentication required") {
		t.Errorf("failure = %q, want an actionable auth notice", failures[0].Error())
	}
}

// TestStartRemoteOAuthWithStoredTokenSucceeds verifies the happy path: with a
// valid unexpired token already in the store, TokenSource yields it, the
// initialize POST carries the bearer, and the handshake completes.
func TestStartRemoteOAuthWithStoredTokenSucceeds(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	const wantToken = "tok-abc"

	var (
		mu      sync.Mutex
		sawAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		sawAuth = r.Header.Get("Authorization")
		mu.Unlock()

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		raw, _ := json.Marshal(InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo:      Implementation{Name: "remote", Version: "1"},
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
	}))
	defer srv.Close()

	// Seed a valid, unexpired token under the engine's server-scoped key.
	store := credentials.NewMemStore()
	blob, err := json.Marshal(oauth.StoredTokens{
		AccessToken: wantToken,
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("marshal:mcp:"+srv.URL, blob); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}

	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return store, nil }))
	if failures := mgr.Start(context.Background()); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	mu.Lock()
	gotAuth := sawAuth
	mu.Unlock()
	if gotAuth != "Bearer "+wantToken {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer "+wantToken)
	}
}

// TestStartRemoteOAuthKeyringUnavailableDegrades ensures a missing OS keychain
// surfaces as an actionable per-server failure (naming the problem) rather than
// a fatal error, and never falls back to plaintext storage.
func TestStartRemoteOAuthKeyringUnavailableDegrades(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	// The manager's own opener reports unavailable storage; the failure must
	// propagate per-server and must not be retried against the OS keychain.
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}

	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) {
		return nil, credentials.ErrKeyringUnavailable
	}))
	failures := mgr.Start(context.Background())
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	msg := failures[0].Error()
	if !strings.Contains(msg, "keychain unavailable") {
		t.Errorf("failure = %q, want a keychain-unavailable notice", msg)
	}
	if !errors.Is(failures[0].Err, credentials.ErrKeyringUnavailable) {
		t.Errorf("failure error = %v, want errors.Is(err, credentials.ErrKeyringUnavailable)", failures[0].Err)
	}
}

// --- instance-scoped credential factory ----------------------------------

// newOAuthInitializeServer is a test-owned MCP endpoint that both serves the
// OAuth metadata discovery documents and completes the initialize handshake
// once the request carries the expected bearer token.
func newOAuthInitializeServer(t *testing.T, wantToken string) (*httptest.Server, *int32) {
	t.Helper()
	var auths int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server"),
			strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			origin := "http://" + r.Host
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 origin,
				"authorization_endpoint": origin + "/authorize",
				"token_endpoint":         origin + "/token",
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access-token",
				"refresh_token": "refresh-token-2",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			if r.Header.Get("Authorization") != "Bearer "+wantToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			atomic.AddInt32(&auths, 1)
			var req Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			raw, _ := json.Marshal(InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "remote", Version: "1"},
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &auths
}

// seedToken writes an OAuth token blob under the engine's per-server key.
func seedToken(t *testing.T, store credentials.Store, serverURL string, tok oauth.StoredTokens) {
	t.Helper()
	blob, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("marshal:mcp:"+serverURL, blob); err != nil {
		t.Fatal(err)
	}
}

// TestOAuthEngineUsesInstanceCredentialFactory pins the core of this change:
// the store a manager's OAuth engine receives comes from that manager's own
// opener, and the engine keeps its per-server storage key inside it.
func TestOAuthEngineUsesInstanceCredentialFactory(t *testing.T) {
	cfg := config.Default()

	var gotService string
	store := credentials.NewMemStore()
	mgr := NewManager(&cfg, WithCredentialFactory(func(service string) (credentials.Store, error) {
		gotService = service
		return store, nil
	}))

	const serverURL = "https://mcp.example.com/mcp"
	eng, err := mgr.oauthEngine("remote", config.MCPServerConfig{URL: serverURL, Auth: "oauth"})
	if err != nil {
		t.Fatalf("oauthEngine: %v", err)
	}
	if gotService != "marshal" {
		t.Errorf("opener service = %q, want %q", gotService, "marshal")
	}
	if eng.Store == nil {
		t.Fatal("engine Store is nil")
	}

	// The engine writes under marshal:mcp:<ServerURL> in the injected store.
	if err := eng.Store.Set("marshal:mcp:"+serverURL, []byte(`{"access_token":"x"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := store.Get("marshal:mcp:" + serverURL); err != nil {
		t.Errorf("token was not stored under the per-server key: %v", err)
	}
}

// An explicitly supplied nil opener is a programming error, not a request for
// the OS keychain: the OAuth construction path must fail rather than silently
// opening the developer's real credentials.
func TestOAuthEngineNilCredentialFactoryDoesNotFallBack(t *testing.T) {
	cfg := config.Default()
	mgr := NewManager(&cfg, WithCredentialFactory(nil))

	eng, err := mgr.oauthEngine("remote", config.MCPServerConfig{URL: "https://mcp.example.com/mcp", Auth: "oauth"})
	if err == nil {
		t.Fatal("oauthEngine returned nil error with an explicitly nil credential factory")
	}
	if eng != nil {
		t.Error("oauthEngine returned an engine alongside an error")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Errorf("error = %q, want it to name the server", err.Error())
	}
}

// Two managers holding different stores must not interfere. Each one's engine
// sees only its own tokens, and the two Start calls run concurrently — with a
// package-global opener this is exactly the race the old shape allowed.
func TestManagersUseIndependentCredentialStoresConcurrently(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	// Each server accepts only its own token, so a store leaking into the
	// other manager fails the handshake loudly.
	srvA, authsA := newOAuthInitializeServer(t, "token-a")
	srvB, authsB := newOAuthInitializeServer(t, "token-b")

	storeA := credentials.NewMemStore()
	storeB := credentials.NewMemStore()
	seedToken(t, storeA, srvA.URL, oauth.StoredTokens{AccessToken: "token-a", ExpiresAt: time.Now().Add(time.Hour)})
	seedToken(t, storeB, srvB.URL, oauth.StoredTokens{AccessToken: "token-b", ExpiresAt: time.Now().Add(time.Hour)})

	cfgA := config.Default()
	cfgA.MCP.Servers = map[string]config.MCPServerConfig{"a": {URL: srvA.URL, Trust: "unrestricted", Auth: "oauth"}}
	cfgB := config.Default()
	cfgB.MCP.Servers = map[string]config.MCPServerConfig{"b": {URL: srvB.URL, Trust: "unrestricted", Auth: "oauth"}}

	mgrA := NewManager(&cfgA, WithCredentialFactory(func(string) (credentials.Store, error) { return storeA, nil }))
	mgrB := NewManager(&cfgB, WithCredentialFactory(func(string) (credentials.Store, error) { return storeB, nil }))
	defer mgrA.Close()
	defer mgrB.Close()

	failures := make([][]ServerFailure, 2)
	var wg sync.WaitGroup
	for i, mgr := range []*Manager{mgrA, mgrB} {
		wg.Add(1)
		go func(i int, mgr *Manager) {
			defer wg.Done()
			failures[i] = mgr.Start(context.Background())
		}(i, mgr)
	}
	wg.Wait()

	for i, f := range failures {
		if len(f) > 0 {
			t.Fatalf("manager %d Start: %v", i, f)
		}
	}
	if got := atomic.LoadInt32(authsA); got == 0 {
		t.Error("server A never saw its manager's bearer token")
	}
	if got := atomic.LoadInt32(authsB); got == 0 {
		t.Error("server B never saw its manager's bearer token")
	}
}

// A manager whose store holds nothing must surface ErrAuthRequired per server
// (degrading to unavailable) rather than reaching for another store.
func TestOAuthManagerMissingTokenUsesInjectedStore(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	store := credentials.NewMemStore()
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}
	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return store, nil }))

	failures := mgr.Start(context.Background())
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	if !errors.Is(failures[0].Err, oauth.ErrAuthSentinel) {
		t.Errorf("failure error = %v, want errors.Is(err, oauth.ErrAuthSentinel)", failures[0].Err)
	}
	if !strings.Contains(failures[0].Error(), "no stored tokens") {
		t.Errorf("failure = %q, want it to report missing tokens", failures[0].Error())
	}
}

// An expired stored token must be refreshed against the test-owned token
// endpoint and re-persisted into the manager's store, so the client's
// initialize POST carries the refreshed bearer.
func TestOAuthManagerRefreshesExpiredTokenIntoInjectedStore(t *testing.T) {
	// Discovery writes its metadata cache relative to the working directory;
	// chdir into a temp dir so the test never touches the repository tree.
	t.Chdir(t.TempDir())

	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	const refreshed = "refreshed-access-token"
	var refreshCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server"),
			strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			origin := "http://" + r.Host
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 origin,
				"authorization_endpoint": origin + "/authorize",
				"token_endpoint":         origin + "/token",
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			if got := r.Form.Get("grant_type"); got != "refresh_token" {
				http.Error(w, "want refresh_token grant, got "+got, http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("refresh_token"); got != "refresh-token-1" {
				http.Error(w, "want the stored refresh token, got "+got, http.StatusBadRequest)
				return
			}
			atomic.AddInt32(&refreshCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  refreshed,
				"refresh_token": "refresh-token-2",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			if r.Header.Get("Authorization") != "Bearer "+refreshed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			raw, _ := json.Marshal(InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "remote", Version: "1"},
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		}
	}))
	defer srv.Close()

	store := credentials.NewMemStore()
	key := "marshal:mcp:" + srv.URL
	seedToken(t, store, srv.URL, oauth.StoredTokens{
		AccessToken:   "stale-access-token",
		RefreshToken:  "refresh-token-1",
		ExpiresAt:     time.Now().Add(-time.Minute), // expired: forces a refresh
		TokenEndpoint: srv.URL + "/token",
	})

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}
	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return store, nil }))
	if failures := mgr.Start(context.Background()); len(failures) > 0 {
		t.Fatalf("Start: %v", failures)
	}
	defer mgr.Close()

	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Errorf("refresh calls = %d, want exactly 1", got)
	}
	raw, err := store.Get(key)
	if err != nil {
		t.Fatalf("refreshed tokens were not persisted in the injected store: %v", err)
	}
	var got oauth.StoredTokens
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != refreshed {
		t.Errorf("stored access token = %q, want %q", got.AccessToken, refreshed)
	}
	if got.RefreshToken != "refresh-token-2" {
		t.Errorf("stored refresh token = %q, want the rotated token", got.RefreshToken)
	}
}

// An already-cancelled context aborts startup through the injected store path
// rather than being masked by a fresh open or a retry.
func TestOAuthManagerCancellationAbortsStartup(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	store := credentials.NewMemStore()
	seedToken(t, store, srv.URL, oauth.StoredTokens{AccessToken: "tok-valid", ExpiresAt: time.Now().Add(time.Hour)})

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
	}
	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return store, nil }))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	failures := mgr.Start(ctx)
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one after cancellation", failures)
	}
	if !errors.Is(failures[0].Err, context.Canceled) {
		t.Errorf("failure error = %v, want errors.Is(err, context.Canceled)", failures[0].Err)
	}
}

// Each oauthEngine call gets whatever its opener returns at that moment: a
// manager whose opener is swapped between constructions never hands the old
// store to the new engine.
func TestOAuthEngineStoreIsPerOpenerCall(t *testing.T) {
	cfg := config.Default()
	first := credentials.NewMemStore()
	second := credentials.NewMemStore()

	which := credentials.Store(first)
	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) { return which, nil }))

	engOne, err := mgr.oauthEngine("remote", config.MCPServerConfig{URL: "https://mcp.example.com/one"})
	if err != nil {
		t.Fatal(err)
	}
	which = second
	engTwo, err := mgr.oauthEngine("remote", config.MCPServerConfig{URL: "https://mcp.example.com/two"})
	if err != nil {
		t.Fatal(err)
	}

	if err := engOne.Store.Set("probe-key", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Get("probe-key"); !errors.Is(err, credentials.ErrNotFound) {
		t.Errorf("the second store saw the first engine's write: err = %v, want ErrNotFound", err)
	}
	if err := engTwo.Store.Set("probe-key", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if raw, err := second.Get("probe-key"); err != nil || string(raw) != "two" {
		t.Errorf("second engine's write = (%q, %v), want (\"two\", nil)", raw, err)
	}
}

// One server's unavailable storage must not cost a healthy server its tools,
// and the opener is consulted exactly once — there is no second attempt
// against a different store.
func TestOAuthEngineErrorsStayPerServer(t *testing.T) {
	origInsecure := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = origInsecure })

	srv, _ := newOAuthInitializeServer(t, "any-token")

	// A plain (non-OAuth) healthy server on the same host, served by its own
	// handler so it does not require a bearer token.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		raw, _ := json.Marshal(InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo:      Implementation{Name: "plain", Version: "1"},
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
	}))
	defer plain.Close()

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"broken":  {URL: srv.URL, Trust: "unrestricted", Auth: "oauth"},
		"healthy": {URL: plain.URL, Trust: "unrestricted"},
	}
	var calls int32
	mgr := NewManager(&cfg, WithCredentialFactory(func(string) (credentials.Store, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("opener: backend exploded")
	}))

	failures := mgr.Start(context.Background())
	defer mgr.Close()

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	if failures[0].Name != "broken" {
		t.Errorf("failed server = %q, want %q", failures[0].Name, "broken")
	}
	if !strings.Contains(failures[0].Error(), "backend exploded") {
		t.Errorf("failure = %q, want it to carry the opener's cause", failures[0].Error())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("opener calls = %d, want exactly 1 (no fallback store)", got)
	}
}
