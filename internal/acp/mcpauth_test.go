package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/tools/mcp"
)

// --- fake authorization server -------------------------------------------

// fakeAuthorizeServer is a minimal RFC 8414 / RFC 7591 / RFC 6749 endpoint
// set standing in for a remote MCP server's authorization server. It lets the
// headless /mcp auth flow run end to end over the ACP command surface without
// touching the network.
type fakeAuthorizeServer struct {
	server *httptest.Server
}

func newFakeAuthorizeServer(t *testing.T) *fakeAuthorizeServer {
	t.Helper()
	fa := &fakeAuthorizeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server"),
			strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                           fa.origin(),
				"authorization_endpoint":           fa.origin() + "/authorize",
				"token_endpoint":                   fa.origin() + "/token",
				"registration_endpoint":            fa.origin() + "/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case strings.HasSuffix(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id":                  "client-acp-test",
				"token_endpoint_auth_method": "none",
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-acp",
				"refresh_token": "refresh-token-acp",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	fa.server = srv
	t.Cleanup(srv.Close)
	return fa
}

func (fa *fakeAuthorizeServer) origin() string { return fa.server.URL }

// completeLoopback drives the redirect the user's browser would make, using
// code and state lifted from the authorization URL the display emitted.
func (fa *fakeAuthorizeServer) completeLoopback(t *testing.T, authURL string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorization url %q: %v", authURL, err)
	}
	redirect := u.Query().Get("redirect_uri")
	state := u.Query().Get("state")
	if redirect == "" || state == "" {
		t.Fatalf("authorization url %q is missing redirect_uri or state", authURL)
	}
	cb := redirect + "?code=" + url.QueryEscape("code-acp-test") + "&state=" + url.QueryEscape(state)
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatalf("drive loopback callback: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// --- notice sink ----------------------------------------------------------

// noticeSink records the session/update notifications a CommandManager emits
// and lets the test wait for a notice containing a given substring.
type noticeSink struct {
	mu     sync.Mutex
	frames []map[string]any
	ch     chan struct{}
}

func newNoticeSink() *noticeSink {
	return &noticeSink{ch: make(chan struct{}, 32)}
}

func (s *noticeSink) notify(method string, params any) error {
	up, ok := params.(SessionUpdateParams)
	if !ok {
		return fmt.Errorf("unexpected notification params type %T", params)
	}
	s.mu.Lock()
	s.frames = append(s.frames, up.Update)
	s.mu.Unlock()
	select {
	case s.ch <- struct{}{}:
	default:
	}
	return nil
}

// waitForText blocks until a recorded notice contains want, and returns its
// text.
func (s *noticeSink) waitForText(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		s.mu.Lock()
		var texts []string
		for _, up := range s.frames {
			text := noticeText(up)
			texts = append(texts, text)
			if strings.Contains(text, want) {
				s.mu.Unlock()
				return text
			}
		}
		s.mu.Unlock()
		select {
		case <-s.ch:
		case <-deadline:
			t.Fatalf("no notice containing %q arrived; saw:\n%s", want, strings.Join(texts, "\n---\n"))
		}
	}
}

func noticeText(up map[string]any) string {
	content, _ := up["content"].(map[string]any)
	text, _ := content["text"].(string)
	return text
}

// authorizationURLFromNotice extracts the authorization URL from a notice
// whose human-readable text ends with the raw URL on its own line.
func authorizationURLFromNotice(t *testing.T, notice string) string {
	t.Helper()
	i := strings.LastIndex(notice, "\nhttp")
	if i < 0 {
		t.Fatalf("notice did not contain an authorization URL line: %q", notice)
	}
	u := strings.TrimSpace(notice[i+1:])
	if _, err := url.Parse(u); err != nil {
		t.Fatalf("extracted authorization URL %q is not parseable: %v", u, err)
	}
	return u
}

// --- manager harness ------------------------------------------------------

// newMCPAuthTestManager builds a CommandManager over the real command
// registry (so /mcp is registered exactly as production registers it) with an
// in-memory credential store and a recording notice sink.
func newMCPAuthTestManager(t *testing.T, servers map[string]config.MCPServerConfig) (*CommandManager, *noticeSink, *credentials.MemStore) {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = servers
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})

	sink := newNoticeSink()
	store := credentials.NewMemStore()
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			if sessionID != "sess_mcp" {
				return nil, false
			}
			return &CommandRuntime{State: state, Registry: reg}, true
		},
		HasActive: func(string) bool { return false },
		Notify:    sink.notify,
		OpenStore: func() (credentials.Store, error) { return store, nil },
	})
	return mgr, sink, store
}

// --- tests ---------------------------------------------------------------

// /mcp auth is headless-capable: session/command_list must report the command
// as "headless" even though its registry entry is TUIOnly, mirroring the
// kinds the manager reports for other config-mutating commands.
func TestCommandManagerCommandListMarksMCPAuthHeadless(t *testing.T) {
	mgr, _, _ := newMCPAuthTestManager(t, nil)

	raw, err := json.Marshal(map[string]any{"sessionId": "sess_mcp"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr.CommandList(context.Background(), raw)
	if err != nil {
		t.Fatalf("CommandList: %v", err)
	}
	list, ok := res.(CommandListResult)
	if !ok {
		t.Fatalf("CommandList result type = %T, want CommandListResult", res)
	}

	kinds := map[string]string{}
	for _, c := range list.Commands {
		kinds[c.Name] = c.Kind
	}
	if kinds["mcp"] != "headless" {
		t.Errorf(`kinds["mcp"] = %q, want "headless"`, kinds["mcp"])
	}
	// Regression guard: a TUIOnly command the manager does not implement
	// stays tui_only, so this change did not widen the surface wholesale.
	if kinds["settings"] != "tui_only" {
		t.Errorf(`kinds["settings"] = %q, want "tui_only"`, kinds["settings"])
	}
}

// The headless display's ShowURL must reach the client: running /mcp auth
// over session/command opens the flow and emits the authorization URL as a
// session/update notice, and the loopback receiver still completes the flow
// for a same-machine browser.
func TestCommandManagerMCPAuthEmitsURLAndCompletesFlow(t *testing.T) {
	// The engine writes its on-disk metadata cache relative to the working
	// directory; chdir into a temp dir so the test is hermetic.
	t.Chdir(t.TempDir())

	// The fixture is a loopback httptest.Server serving plain HTTP; the auth
	// path now enforces the same remote-endpoint gate as startup, so the test
	// seam and the trust acknowledgement are required to reach the flow.
	mcp.AllowInsecureHTTP(true)
	t.Cleanup(func() { mcp.AllowInsecureHTTP(false) })

	as := newFakeAuthorizeServer(t)
	mgr, sink, store := newMCPAuthTestManager(t, map[string]config.MCPServerConfig{
		"linear": {URL: as.origin(), Type: "http", Auth: "oauth", Trust: "unrestricted"},
	})

	raw, _ := json.Marshal(CommandParams{SessionID: "sess_mcp", Name: "mcp", Args: []string{"auth", "linear"}})
	res, err := mgr.Command(context.Background(), raw)
	if err != nil {
		t.Fatalf("Command(mcp auth linear): %v", err)
	}
	cr, ok := res.(CommandResult)
	if !ok {
		t.Fatalf("Command result type = %T, want CommandResult", res)
	}
	if !strings.Contains(cr.Text, "linear") {
		t.Errorf("Command result.Text = %q, want it to name the server", cr.Text)
	}

	// ShowURL's notice must carry the authorization URL.
	urlNotice := sink.waitForText(t, "Authorize MCP server")
	if !strings.Contains(urlNotice, "linear") {
		t.Errorf("URL notice = %q, want it to name the server", urlNotice)
	}
	authURL := authorizationURLFromNotice(t, urlNotice)

	// The loopback receiver is still up: completing the redirect here is what
	// a browser on the same machine would do.
	as.completeLoopback(t, authURL)

	// Success is reported back over the wire and the token set is persisted.
	sink.waitForText(t, "Authorized MCP server")
	stored, err := store.Get("marshal:mcp:" + as.origin())
	if err != nil {
		t.Fatalf("tokens were not persisted: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("persisted token blob is empty")
	}
}

// Resolution failures surface a clear sentence over the wire naming the fix.
func TestCommandManagerMCPAuthResolutionFailures(t *testing.T) {
	mgr, _, _ := newMCPAuthTestManager(t, map[string]config.MCPServerConfig{
		"linear": {URL: "https://mcp.example.com/mcp", Type: "http", Auth: "oauth"},
		"static": {URL: "https://mcp.example.com/mcp", Type: "http"},
	})

	cases := []struct {
		name     string
		args     []string
		wantText string
	}{
		{"unknown server", []string{"auth", "gitlab"}, "gitlab"},
		{"non-oauth server", []string{"auth", "static"}, "oauth"},
		{"missing name", []string{"auth"}, "Usage:"},
		{"missing subcommand", []string{"nope"}, "Usage:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(CommandParams{SessionID: "sess_mcp", Name: "mcp", Args: tc.args})
			_, err := mgr.Command(context.Background(), raw)
			if err == nil {
				t.Fatal("got nil error, want a resolution failure")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantText)
			}
		})
	}
}

// A host with no OS keyring must get an actionable error, never a crash and
// never a nil Store.
func TestCommandManagerMCPAuthKeyringUnavailable(t *testing.T) {
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: "https://mcp.example.com/mcp", Type: "http", Auth: "oauth", Trust: "unrestricted"},
	}
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})

	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: state, Registry: reg}, true
		},
		HasActive: func(string) bool { return false },
		OpenStore: func() (credentials.Store, error) {
			return nil, fmt.Errorf("keyring: %w", credentials.ErrKeyringUnavailable)
		},
	})

	raw, _ := json.Marshal(CommandParams{SessionID: "sess_mcp", Name: "mcp", Args: []string{"auth", "linear"}})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("got nil error, want a keyring-unavailable error")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Errorf("error = %q, want it to explain the keychain is unavailable", err.Error())
	}
}

// A runtime with no configured MCP servers must produce the resolution
// sentence (not a panic), exercising the nil/empty Config path the
// resolution helper tolerates.
func TestCommandManagerMCPAuthUnconfiguredRuntimeResolvesCleanly(t *testing.T) {
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
		},
		HasActive: func(string) bool { return false },
		OpenStore: func() (credentials.Store, error) { return credentials.NewMemStore(), nil },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_no_id", Name: "mcp", Args: []string{"auth", "x"}})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("got nil error, want a resolution failure")
	}
	if !strings.Contains(err.Error(), `No MCP server named "x"`) {
		t.Errorf("error = %q, want the unknown-server sentence", err.Error())
	}
}

// The empty-session-id guard is defensive: session/command validates
// sessionId upstream, so it is unreachable through Command. Exercise it
// directly so a future refactor that removes the validation cannot emit a
// notice addressed to nowhere.
func TestCommandManagerMCPAuthEmptySessionIDGuard(t *testing.T) {
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: "https://mcp.example.com/mcp", Type: "http", Auth: "oauth", Trust: "unrestricted"},
	}
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup:    func(string) (*CommandRuntime, bool) { return &CommandRuntime{State: state, Registry: reg}, true },
		HasActive: func(string) bool { return false },
		OpenStore: func() (credentials.Store, error) { return credentials.NewMemStore(), nil },
	})
	_, err := mgr.runHeadless(context.Background(), "", &CommandRuntime{State: state, Registry: reg}, "mcp", []string{"auth", "linear"})
	if err == nil {
		t.Fatal("got nil error, want the empty-session-id guard to refuse")
	}
	if !strings.Contains(err.Error(), "session has no id") {
		t.Errorf("error = %q, want the session-id guard message", err.Error())
	}
}

// headlessCommandNames gates the command_list kind and Command's dispatch
// decision; headlessCommands holds the implementations. They must agree, or
// a command would claim one kind and run another (or not run at all).
func TestHeadlessCommandNameSetMatchesImplementations(t *testing.T) {
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup:    func(string) (*CommandRuntime, bool) { return nil, false },
		HasActive: func(string) bool { return false },
	})
	impls := mgr.headlessCommands()
	for name := range headlessCommandNames {
		if _, ok := impls[name]; !ok {
			t.Errorf("headlessCommandNames lists %q but headlessCommands has no implementation", name)
		}
	}
	for name := range impls {
		if !headlessCommandNames[name] {
			t.Errorf("headlessCommands implements %q but headlessCommandNames does not list it", name)
		}
		if !mgr.supportsHeadless(name) {
			t.Errorf("supportsHeadless(%q) = false, want true", name)
		}
	}
	if len(impls) == 0 {
		t.Fatal("no headless command implementations registered")
	}
	if mgr.supportsHeadless("settings") {
		t.Error("supportsHeadless(settings) = true, want false")
	}
}

// The headless display must satisfy oauth.Display and treat the loopback
// callback — not Wait — as the completion signal.
func TestHeadlessMCPDisplayWaitBlocksUntilCancelled(t *testing.T) {
	disp := &headlessMCPDisplay{}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- disp.Wait(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned %v before cancellation", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait returned nil after cancellation, want context.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}
