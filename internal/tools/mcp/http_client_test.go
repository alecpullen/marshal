package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/oauth"
	"marshal/internal/tools/registry"
)

// decodeRequest reads and decodes the JSON-RPC request from an HTTP request.
func decodeRequest(t *testing.T, r *http.Request) Request {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

// writeJSONResult writes a JSON-RPC success response.
func writeJSONResult(w http.ResponseWriter, id json.Number, result any) {
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: id, Result: raw})
}

// TestHTTPClientRoundTrip exercises the full initialize handshake followed by
// tools/list and tools/call against a Streamable-HTTP handler.
func TestHTTPClientRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var sessionOnList string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch req.Method {
		case "initialize":
			w.Header().Set(mcpSessionIDHeader, "sess-123")
			writeJSONResult(w, req.ID, InitializeResult{ProtocolVersion: "2024-11-05"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			sessionOnList = r.Header.Get(mcpSessionIDHeader)
			mu.Unlock()
			writeJSONResult(w, req.ID, ListToolsResult{Tools: []MCPTool{{
				Name:        "hello",
				Description: "says hi",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}}})
		case "tools/call":
			writeJSONResult(w, req.ID, CallToolResult{Content: []MCPContent{{Type: "text", Text: "hi there"}}})
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "hello" {
		t.Fatalf("tools/list returned %+v, want one tool named hello", list.Tools)
	}

	var call CallToolResult
	if err := c.Call(context.Background(), "tools/call", CallToolParams{Name: "hello"}, &call); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if len(call.Content) != 1 || call.Content[0].Text != "hi there" {
		t.Fatalf("tools/call returned %+v, want text 'hi there'", call.Content)
	}

	mu.Lock()
	got := sessionOnList
	mu.Unlock()
	if got != "sess-123" {
		t.Errorf("tools/list carried session id %q, want %q", got, "sess-123")
	}
}

// TestHTTPClientSSEFraming verifies that a text/event-stream response is
// parsed and the matching JSON-RPC message is returned.
func TestHTTPClientSSEFraming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"tools":[{"name":"sse-tool"}]}`),
		})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list over SSE: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "sse-tool" {
		t.Fatalf("SSE tools/list returned %+v, want one tool named sse-tool", list.Tools)
	}
}

// TestHTTPClientSSEMultiline verifies multi-line data: continuation per the
// SSE spec.
func TestHTTPClientSSEMultiline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"tools":[{"name":"multi"}]}`),
		})
		// Split the JSON across two data: lines; they join with a newline.
		// Split after the opening brace so the joined payload stays valid
		// JSON (a newline between tokens is insignificant whitespace).
		fmt.Fprintf(w, "data: %s\ndata: %s\n\n", raw[:1], raw[1:])
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list over multi-line SSE: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "multi" {
		t.Fatalf("multi-line SSE returned %+v, want one tool named multi", list.Tools)
	}
}

// TestHTTPClientSessionIDPropagation verifies the session id returned by the
// initialize response is echoed on subsequent requests.
func TestHTTPClientSessionIDPropagation(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		mu.Lock()
		seen = append(seen, r.Header.Get(mcpSessionIDHeader))
		mu.Unlock()
		switch req.Method {
		case "initialize":
			w.Header().Set(mcpSessionIDHeader, "abc-789")
			writeJSONResult(w, req.ID, InitializeResult{})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			writeJSONResult(w, req.ID, ListToolsResult{})
		}
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 3 {
		t.Fatalf("expected at least 3 requests, saw %d", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("initialize request carried session id %q, want empty", seen[0])
	}
	if seen[1] != "abc-789" {
		t.Errorf("initialized notification carried session id %q, want abc-789", seen[1])
	}
	if seen[2] != "abc-789" {
		t.Errorf("tools/list carried session id %q, want abc-789", seen[2])
	}
}

// TestHTTPClientJSONRPCError verifies a JSON-RPC error is surfaced with the
// stdio client's wording.
func TestHTTPClientJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32601, Message: "nope"},
		})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MCP error (-32601): nope") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "MCP error (-32601): nope")
	}
}

// TestHTTPClientNon2xx verifies a non-2xx status is reported with the status
// code and a body snippet.
func TestHTTPClientNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to include the body snippet", err.Error())
	}
}

// TestHTTPClientCallAfterClose verifies Call returns ErrClientClosed once the
// client has been closed.
func TestHTTPClientCallAfterClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResult(w, json.Number("1"), ListToolsResult{})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close must be idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	err := c.Call(context.Background(), "tools/list", nil, nil)
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("Call after Close = %v, want ErrClientClosed", err)
	}
}

// TestHTTPClientContextCancellation verifies Call honours ctx cancellation and
// returns promptly against a blocking server.
func TestHTTPClientContextCancellation(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)

	c := NewHTTPClient("remote", srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Call(ctx, "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want a context error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Call took %v, want it to return promptly", elapsed)
	}
}

// TestHTTPClientUnauthorizedWithoutTokenSource is a regression guard: with no
// OAuth token source the 401 error text is unchanged.
func TestHTTPClientUnauthorizedWithoutTokenSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no creds", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "mcp: HTTP 401: no creds"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, oauth.ErrAuthSentinel) {
		t.Errorf("error = %v, want it not to satisfy oauth.ErrAuthSentinel", err)
	}
}

// TestHTTPClientUnauthorizedWithEmptyToken verifies an empty token is
// reported as an authentication requirement, not a generic HTTP error.
func TestHTTPClientUnauthorizedWithEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no creds", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil, WithHTTPOAuth(func(context.Context) (string, error) {
		return "", nil
	}))
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Errorf("error = %v, want it to satisfy oauth.ErrAuthSentinel", err)
	}
}

// TestHTTPClientTokenSourceError verifies a failing token source surfaces an
// authentication requirement that still unwraps to the source error.
func TestHTTPClientTokenSourceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached when the token source fails")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cause := errors.New("refresh token expired")
	c := NewHTTPClient("remote", srv.URL, nil, WithHTTPOAuth(func(context.Context) (string, error) {
		return "", cause
	}))
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Errorf("error = %v, want it to satisfy oauth.ErrAuthSentinel", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("error = %v, want it to unwrap to the token source error", err)
	}
	var authErr *oauth.ErrAuthRequired
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want it to be an *oauth.ErrAuthRequired", err)
	}
	if authErr.ServerName != "remote" {
		t.Errorf("ServerName = %q, want %q", authErr.ServerName, "remote")
	}
}

// TestHTTPClientUnauthorizedWithToken verifies a 401 returned despite a
// token source is reported as an authentication requirement.
func TestHTTPClientUnauthorizedWithToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil, WithHTTPOAuth(func(context.Context) (string, error) {
		return "stale-token", nil
	}))
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Errorf("error = %v, want it to satisfy oauth.ErrAuthSentinel", err)
	}
}

// TestHTTPClientOAuthBearerToken verifies the token source's token reaches the
// server as a bearer credential.
func TestHTTPClientOAuthBearerToken(t *testing.T) {
	var mu sync.Mutex
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		req := decodeRequest(t, r)
		writeJSONResult(w, req.ID, ListToolsResult{})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil, WithHTTPOAuth(func(context.Context) (string, error) {
		return "tok-abc", nil
	}))
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "Bearer tok-abc" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer tok-abc")
	}
}

// TestHTTPClientOAuthOverridesStaticHeader verifies the token source wins over
// a static Authorization header without mutating the client's Headers map.
func TestHTTPClientOAuthOverridesStaticHeader(t *testing.T) {
	var mu sync.Mutex
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		req := decodeRequest(t, r)
		writeJSONResult(w, req.ID, ListToolsResult{})
	}))
	defer srv.Close()

	headers := map[string]string{"Authorization": "Bearer static"}
	c := NewHTTPClient("remote", srv.URL, headers, WithHTTPOAuth(func(context.Context) (string, error) {
		return "dynamic", nil
	}))
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "Bearer dynamic" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer dynamic")
	}
	if c.Headers["Authorization"] != "Bearer static" {
		t.Errorf("client Headers mutated to %q, want %q", c.Headers["Authorization"], "Bearer static")
	}
}

// studioMCPResponder is a small in-test MCP endpoint: the JSON-RPC methods a
// Studio endpoint must serve (initialize, tools/list, tools/call) behind a
// bearer token, so the production client can be driven over a real transport
// without a bridge fixture. The bridge's own handler is proven on its side of
// the boundary (web/bridge/mcp_factory_integration_test.go); this package may
// not import web/ the other way round, and web/ may not import it.
type studioMCPResponder struct {
	token string // the one bearer token this endpoint accepts

	mu     sync.Mutex
	method []string
	auth   []string
}

// studioMCPTool is the one read tool this endpoint advertises.
const studioMCPTool = "factory_projects"

// studioMCPRead is the authorized read's answer.
const studioMCPRead = `{"projects":[{"projectId":"p1","capabilities":["read"]}]}`

func (r *studioMCPResponder) ServeHTTP(w http.ResponseWriter, rq *http.Request) {
	const prefix = "Bearer "
	presented := strings.TrimPrefix(rq.Header.Get("Authorization"), prefix)
	if presented != r.token || r.token == "" {
		// The endpoint authenticates before it parses: an unknown or absent
		// token reaches no method.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req Request
	if err := json.NewDecoder(rq.Body).Decode(&req); err != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.method = append(r.method, req.Method)
	r.auth = append(r.auth, rq.Header.Get("Authorization"))
	r.mu.Unlock()

	switch req.Method {
	case "initialize":
		writeJSONResult(w, req.ID, InitializeResult{
			ProtocolVersion: "2025-06-18",
			Capabilities:    ServerCapabilities{Tools: &struct{}{}},
			ServerInfo:      Implementation{Name: "marshal-fleet", Version: "1"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeJSONResult(w, req.ID, ListToolsResult{Tools: []MCPTool{{
			Name:        studioMCPTool,
			Description: "List the projects this client holds a grant on.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}}})
	case "tools/call":
		writeJSONResult(w, req.ID, CallToolResult{Content: []MCPContent{{Type: "text", Text: studioMCPRead}}})
	default:
		raw, _ := json.Marshal(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: -32601, Message: "unknown method " + req.Method}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}
}

// seen returns the methods reached and the credentials they carried.
func (r *studioMCPResponder) seen() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.method...), append([]string(nil), r.auth...)
}

// reached reports how many requests this endpoint answered.
func (r *studioMCPResponder) reached() int {
	methods, _ := r.seen()
	return len(methods)
}

// studioSocketPath returns an absolute Unix-socket path inside an owner-only
// directory. The path is kept well clear of sun_path (104 bytes on macOS, 108
// on Linux); t.TempDir() can be long on a platform whose temp dir is
// (macOS's /var/folders/...), so a short directory is used when it would be.
func studioSocketPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir() is not owner-only on every toolchain, and sun_path's
	// directory must be: set the mode rather than assume it.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restrict the socket directory: %v", err)
	}
	if sock := filepath.Join(dir, "s.sock"); len(sock) < 100 {
		return sock
	}
	short, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatalf("short socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	if err := os.Chmod(short, 0o700); err != nil {
		t.Fatalf("restrict the short socket directory: %v", err)
	}
	return filepath.Join(short, "s.sock")
}

// studioSocketClient returns the injected *http.Client whose transport dials
// sock. The request's host is a name, not a lookup: the socket is the address,
// exactly as the daemon's own client dials it.
func studioSocketClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

// studioServeSocket serves handler on sock. It skips the test when the
// platform cannot host a Unix socket, rather than failing a platform for a
// transport it does not have.
func studioServeSocket(t *testing.T, sock string, handler http.Handler) {
	t.Helper()
	if info, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("stat the socket directory: %v", err)
	} else if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("the socket directory mode is %04o, want owner-only", perm)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("this platform cannot host a Unix socket: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// studioTools discovers tools through the production manager, so the proof
// covers RegisterTools and the handler it builds rather than a bare Call.
func studioTools(t *testing.T, c *HTTPClient, logger *slog.Logger) *registry.Registry {
	t.Helper()
	reg := registry.New()
	opts := []ManagerOption{}
	if logger != nil {
		opts = append(opts, WithManagerLogger(logger))
	}
	mgr := NewManager(&config.Config{}, opts...)
	// The manager is built here with the one client under test; production
	// fills this list from the [mcp.servers] entries it validated.
	mgr.clients = []caller{c}
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if len(reg.List()) != 1 {
		t.Fatalf("discovery registered %d tools, want the one the endpoint advertises", len(reg.List()))
	}
	return reg
}

// studioRead calls the discovered tool through the registry, which is the
// path a session's tool call takes, and returns the tool's text.
func studioRead(t *testing.T, reg *registry.Registry, serverName string) string {
	t.Helper()
	name := "mcp." + serverName + "." + studioMCPTool
	res, err := reg.Dispatch(t.Context(), registry.ToolCall{Name: name, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !strings.Contains(res.Content, `"projectId":"p1"`) {
		t.Fatalf("%s returned %q, want the authorized read's project", name, res.Content)
	}
	return res.Content
}

// TestMCPStudioIntegrationUnixSocketClient drives the production Streamable
// HTTP client over an owner-only Unix socket: initialize, tool discovery
// through the manager, and an authorized read through the registered handler.
// The transport is the one a Marshal session has on the local daemon; the
// endpoint is an in-test responder, because web/ may not be imported from
// here (see studioMCPResponder).
func TestMCPStudioIntegrationUnixSocketClient(t *testing.T) {
	sock := studioSocketPath(t)
	responder := &studioMCPResponder{token: "studio-session-token-1"}
	studioServeSocket(t, sock, responder)
	quiet := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	c := NewHTTPClient("marshal-studio", "http://marshal-agent-studio.invalid/mcp", nil,
		WithHTTPClient(studioSocketClient(sock)),
		WithHTTPOAuth(func(context.Context) (string, error) { return responder.token, nil }),
		WithHTTPClientLogger(quiet),
	)
	defer c.Close()
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("initialize over the socket: %v", err)
	}

	reg := studioTools(t, c, quiet)
	studioRead(t, reg, "marshal-studio")

	// Every request reached the socket, and each carried the token's identity
	// rather than a static header captured at construction.
	methods, auths := responder.seen()
	if len(methods) < 4 || methods[0] != "initialize" || methods[1] != "notifications/initialized" {
		t.Fatalf("the socket endpoint saw %v, want initialize then the notification", methods)
	}
	for _, auth := range auths {
		if auth != "Bearer "+responder.token {
			t.Fatalf("a request reached the endpoint with %q, want the session's bearer", auth)
		}
	}

	// Without a token nothing is sent: the endpoint's refusal is an error, and
	// never an anonymous success.
	before := responder.reached()
	anonymous := NewHTTPClient("marshal-studio", "http://marshal-agent-studio.invalid/mcp", nil,
		WithHTTPClient(studioSocketClient(sock)),
	)
	defer anonymous.Close()
	err := anonymous.Start(t.Context())
	if err == nil {
		t.Fatal("an unauthenticated initialize succeeded")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("an unauthenticated initialize = %v, want the endpoint's 401", err)
	}

	// A token the endpoint does not know is the same answer: the socket is a
	// transport, and identity is the bearer's.
	impostor := NewHTTPClient("marshal-studio", "http://marshal-agent-studio.invalid/mcp", nil,
		WithHTTPClient(studioSocketClient(sock)),
		WithHTTPOAuth(func(context.Context) (string, error) { return "not-a-session", nil }),
	)
	defer impostor.Close()
	err = impostor.Start(t.Context())
	var authErr *oauth.ErrAuthRequired
	if !errors.As(err, &authErr) {
		t.Fatalf("an unknown token = %v, want an *oauth.ErrAuthRequired", err)
	}
	if responder.reached() != before {
		t.Fatalf("an unauthenticated request reached a method: %d → %d", before, responder.reached())
	}
}

// TestMCPStudioIntegrationRemoteTLS drives the same client against a TLS
// remote: an explicit token source, an explicitly trusted certificate pool,
// the production url gate, endpoint identity, and the guarantee that the
// token's value never reaches a registered tool description or a log line.
func TestMCPStudioIntegrationRemoteTLS(t *testing.T) {
	const tokenA = "studio-remote-token-A"
	const tokenB = "studio-remote-token-B"

	instanceA := &studioMCPResponder{token: tokenA}
	tsA := httptest.NewTLSServer(instanceA)
	t.Cleanup(tsA.Close)
	instanceB := &studioMCPResponder{token: tokenB}
	tsB := httptest.NewTLSServer(instanceB)
	t.Cleanup(tsB.Close)
	tsA.Config.ErrorLog = log.New(io.Discard, "", 0)
	tsB.Config.ErrorLog = log.New(io.Discard, "", 0)

	// The configured endpoint goes through the production gate exactly once:
	// https and an acknowledged trust pass, and the acknowledgment is still
	// what a remote endpoint requires.
	server := config.MCPServerConfig{Type: "http", URL: tsA.URL, Trust: "unrestricted"}
	if err := ValidateRemoteServer(server); err != nil {
		t.Fatalf("a trusted https endpoint was refused: %v", err)
	}
	if err := ValidateRemoteServer(config.MCPServerConfig{Type: "http", URL: tsA.URL}); err == nil {
		t.Fatal("an untrusted remote endpoint was accepted")
	}

	// Certificate trust is explicit: this pool holds instance A's certificate
	// and nothing else. No environment credential is read anywhere here.
	pool := x509.NewCertPool()
	pool.AddCert(tsA.Certificate())
	trusted := func(serverName string) *http.Client {
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName},
		}}
	}
	tokenSource := func(token string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return token, nil }
	}

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	c := NewHTTPClient("marshal-studio", server.URL, server.Headers,
		WithHTTPClient(trusted("")),
		WithHTTPOAuth(tokenSource(tokenA)),
		WithHTTPClientLogger(logger),
	)
	defer c.Close()
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("initialize over TLS: %v", err)
	}
	reg := studioTools(t, c, logger)
	studioRead(t, reg, "marshal-studio")

	// The token is a request credential and nothing else: no registered tool
	// description or schema carries it, and no log line the client or manager
	// wrote does.
	for _, tool := range reg.List() {
		described := tool.Name + "\n" + tool.Description + "\n" + string(tool.Schema)
		if strings.Contains(described, tokenA) {
			t.Fatalf("a registered tool carries the token source's value: %s", described)
		}
	}
	if strings.Contains(logs.String(), tokenA) {
		t.Fatalf("a log line carries the token source's value: %s", logs.String())
	}

	// Endpoint identity: one instance's credential is no credential at the
	// other instance, even on the same trusted transport.
	wrongInstance := NewHTTPClient("marshal-studio", tsB.URL, nil,
		WithHTTPClient(trusted("")),
		WithHTTPOAuth(tokenSource(tokenA)),
		WithHTTPClientLogger(logger),
	)
	defer wrongInstance.Close()
	err := wrongInstance.Start(t.Context())
	var authErr *oauth.ErrAuthRequired
	if !errors.As(err, &authErr) {
		t.Fatalf("instance A's token at instance B = %v, want an *oauth.ErrAuthRequired", err)
	}
	if instanceB.reached() != 0 {
		t.Fatalf("instance B reached %d methods with a foreign token", instanceB.reached())
	}
	if strings.Contains(err.Error(), tokenA) {
		t.Fatalf("the refusal carries the token source's value: %v", err)
	}

	// Certificate identity: a trusted pool is not a licence to skip the name
	// check. A client that verifies the chain against another name fails
	// before any request is written.
	before := instanceA.reached()
	misnamed := NewHTTPClient("marshal-studio", server.URL, nil,
		WithHTTPClient(trusted("marshal.invalid")),
		WithHTTPOAuth(tokenSource(tokenA)),
		WithHTTPClientLogger(logger),
	)
	defer misnamed.Close()
	err = misnamed.Start(t.Context())
	if err == nil {
		t.Fatal("a certificate for another name was accepted")
	}
	if errors.As(err, &authErr) {
		t.Fatalf("a name mismatch was reported as an authorization failure: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("a name mismatch = %v, want a certificate verification failure", err)
	}
	if instanceA.reached() != before {
		t.Fatalf("a request was written before the certificate was verified: %d → %d", before, instanceA.reached())
	}
}

// TestHTTPClientCustomHeaders verifies configured headers reach the server.
func TestHTTPClientCustomHeaders(t *testing.T) {
	var mu sync.Mutex
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		req := decodeRequest(t, r)
		writeJSONResult(w, req.ID, ListToolsResult{})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, map[string]string{"Authorization": "Bearer x"})
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "Bearer x" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer x")
	}
}
