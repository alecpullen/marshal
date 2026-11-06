package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
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
	msgs := stateOf(m).Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages recorded")
	}
	return msgs[len(msgs)-1].Content
}

// mcpAuthFixture docks an authorization panel holding a long local URL, the
// way /mcp auth does, without an engine, browser or network.
func mcpAuthFixture(t *testing.T, m Model) (Model, *mcpauth.Panel, string) {
	t.Helper()
	url := "https://auth.example.test/authorize?response_type=code&client_id=marshal" +
		"&code_challenge=0123456789abcdefghijklmnopqrstuvwxyz0123456789&code_challenge_method=S256" +
		"&scope=mcp%3Aread%20mcp%3Awrite&resource=https%3A%2F%2Fmcp.example.test%2Fmcp&state=fedcba9876543210"
	disp := mcpauth.NewDisplay()
	disp.ShowURL(url)
	panel := mcpauth.NewPanel("linear", disp)
	m.dock.Open(panel)
	return m, panel, url
}

// runCopyMsgs executes a command and returns the formatted messages it saw,
// feeding every message back through Update so a multi-hop copy
// (link request → clipboard write → flash) completes. Flash timers are never
// waited on: flattenCmd abandons a command that does not answer promptly.
func runCopyMsgs(t *testing.T, m *Model, cmd tea.Cmd) []string {
	t.Helper()
	var seen []string
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		for _, msg := range flattenCmd(c) {
			seen = append(seen, fmt.Sprintf("%T %v", msg, msg))
			updated, follow := m.Update(msg)
			*m = asModel(t, updated)
			run(follow)
		}
	}
	run(cmd)
	return seen
}

func TestMCPAuthCopyThroughModel(t *testing.T) {
	newModel := func(t *testing.T) Model {
		m := newTestModel(t)
		// A stub opener, so nothing ever launches a real browser.
		m.openBrowser = func(string) error { return nil }
		return m
	}

	t.Run("copies the exact URL through the local clipboard", func(t *testing.T) {
		m := newModel(t)
		m, _, url := mcpAuthFixture(t, m)
		clip := &fakeClipboard{available: true}
		m.copyWriter = clip
		m.copyRemote = func() bool { return false }

		updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = asModel(t, updated)
		if cmd == nil {
			t.Fatal("c produced no copy command")
		}
		seen := runCopyMsgs(t, &m, cmd)

		if len(clip.writes) != 1 || clip.writes[0] != url {
			t.Fatalf("clipboard writes = %q, want the exact authorization URL", clip.writes)
		}
		if sawOSC52(seen, url) {
			t.Errorf("a confirmed local write must not also emit OSC 52: %v", seen)
		}
		if !strings.HasPrefix(m.flash, "Copied") {
			t.Errorf("flash = %q, want the confirmed-copy flash", m.flash)
		}
	})

	t.Run("a failing local writer reports an unconfirmed OSC 52 copy", func(t *testing.T) {
		m := newModel(t)
		m, _, url := mcpAuthFixture(t, m)
		m.copyWriter = &fakeClipboard{available: true, err: errors.New("pbcopy: exit 1")}

		updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = asModel(t, updated)
		seen := runCopyMsgs(t, &m, cmd)

		if !sawOSC52(seen, url) {
			t.Fatalf("a failed local write must still reach the terminal: %v", seen)
		}
		if strings.HasPrefix(m.flash, "Copied") {
			t.Errorf("flash = %q, must not claim the terminal confirmed the copy", m.flash)
		}
		if !strings.Contains(m.flash, "OSC 52") || !strings.Contains(m.flash, "pbcopy: exit 1") {
			t.Errorf("flash = %q, want the OSC 52 fallback and its cause", m.flash)
		}
	})

	t.Run("no URL yet copies nothing", func(t *testing.T) {
		m := newModel(t)
		m.dock.Open(mcpauth.NewPanel("linear", mcpauth.NewDisplay()))
		clip := &fakeClipboard{available: true}
		m.copyWriter = clip

		updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = asModel(t, updated)
		if cmd != nil {
			runCopyMsgs(t, &m, cmd)
		}
		if len(clip.writes) != 0 {
			t.Fatalf("clipboard writes = %q, want none before the URL exists", clip.writes)
		}
	})

	t.Run("a cancelled flow refuses a late copy", func(t *testing.T) {
		m := newModel(t)
		m, _, _ = mcpAuthFixture(t, m)
		clip := &fakeClipboard{available: true}
		m.copyWriter = clip

		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = asModel(t, updated)

		updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = asModel(t, updated)
		if cmd != nil {
			runCopyMsgs(t, &m, cmd)
		}
		if len(clip.writes) != 0 {
			t.Fatalf("clipboard writes = %q, want none after cancellation", clip.writes)
		}
	})

	t.Run("a delayed copy from a replaced panel is dropped", func(t *testing.T) {
		m := newModel(t)
		var old *mcpauth.Panel
		var url string
		m, old, url = mcpAuthFixture(t, m)
		clip := &fakeClipboard{available: true}
		m.copyWriter = clip

		// A second authorization flow for the same server replaces the panel
		// and shows an identical URL, so only panel identity can tell them
		// apart.
		m, _, _ = mcpAuthFixture(t, m)

		updated, cmd := m.Update(mcpauth.CopyLinkMsg{Panel: old, URL: url})
		m = asModel(t, updated)
		if cmd != nil {
			runCopyMsgs(t, &m, cmd)
		}
		if len(clip.writes) != 0 {
			t.Fatalf("clipboard writes = %q, want none from a replaced panel", clip.writes)
		}
	})

	t.Run("the live panel still accepts its own copy request", func(t *testing.T) {
		m := newModel(t)
		var panel *mcpauth.Panel
		var url string
		m, panel, url = mcpAuthFixture(t, m)
		clip := &fakeClipboard{available: true}
		m.copyWriter = clip

		updated, cmd := m.Update(mcpauth.CopyLinkMsg{Panel: panel, URL: url})
		m = asModel(t, updated)
		if cmd == nil {
			t.Fatal("the live panel's copy request should be honoured")
		}
		runCopyMsgs(t, &m, cmd)

		if len(clip.writes) != 1 || clip.writes[0] != url {
			t.Fatalf("clipboard writes = %q, want the exact URL", clip.writes)
		}
	})
}

func TestMCPAuthDoneReconnectsRuntime(t *testing.T) {
	var reloaded []config.Config
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(cfg config.Config) error {
			reloaded = append(reloaded, cfg)
			return nil
		}
	})
	m.mcpAuthServer = "linear"

	updated, settle := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)
	// The rebuild runs from the call queue, so settle before counting reloads.
	m = settleCalls(t, m, settle)

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
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(config.Config) error { return errors.New("provider build failed") }
	})
	m.mcpAuthServer = "linear"

	updated, settle := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)
	m = settleCalls(t, m, settle)

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
			reloads := 0
			m := newTestModelDeps(t, func(d *inproc.Deps) {
				d.ReloadConfig = func(config.Config) error {
					reloads++
					return nil
				}
			})
			m.mcpAuthServer = "linear"

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
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		// Stand in for the real rebuild: it starts the server, it fails again
		// for a different reason, and buildAgentRunnerWithLock sets a fresh
		// notice.
		d.ReloadConfig = func(config.Config) error {
			d.State.SetNotice(session.Notice{
				Category: session.NoticeConfig,
				Severity: session.SeverityWarn,
				Message:  "MCP server 'linear' is unavailable: 403 forbidden",
				Source:   "mcp",
			})
			return nil
		}
	})
	// The stale notice the flow is supposed to resolve.
	stateOf(m).SetNotice(session.Notice{
		Category: session.NoticeConfig,
		Severity: session.SeverityWarn,
		Message:  "MCP server 'linear' needs OAuth — run /mcp auth linear",
		Source:   "mcp",
	})

	m.reconnectMCPServer("linear")
	// The rebuild runs from the call queue, so its notes land when it returns.
	m = settleCalls(t, m)

	notice, ok := stateOf(m).Notice()
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
	before := len(stateOf(m).Messages())

	m.reconnectMCPServer("linear")

	if got := len(stateOf(m).Messages()); got != before {
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
// with one OAuth-enabled MCP server configured. The seam is rebuilt over the
// caller's resolver, because /mcp auth takes its token store from the session.
func mcpAuthModel(t *testing.T, auth provider.AuthResolver, open func(string) error) Model {
	t.Helper()
	m := newTestModel(t)
	host, sess := inproc.New(inproc.Deps{State: stateOf(m), Auth: auth})
	m.host, m.sess = host, sess
	m.openBrowser = open
	stateOf(m).Config.MCP.Servers = map[string]config.MCPServerConfig{
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

// stubLoginSession overrides LoginMCP: it reports one authorization URL and
// then blocks until its context ends, as the agent side's sign-in does.
type stubLoginSession struct {
	acpclient.Session
	url string
}

func (s *stubLoginSession) LoginMCP(ctx context.Context, _ string, show func(url string)) error {
	show(s.url)
	<-ctx.Done()
	return ctx.Err()
}

// TestMCPAuthCommandOpensTheURL: the authorize command hands the URL to the
// opener and to the panel's display, and cancelling it ends the command with a
// cancellation rather than a failure.
func TestMCPAuthCommandOpensTheURL(t *testing.T) {
	const wantURL = "https://auth.example/authorize?x=1"
	opened := &urlRecorder{}
	m := mcpAuthModel(t, newMemAuth(), func(u string) error { opened.record(u); return nil })

	sess := &stubLoginSession{Session: m.sess, url: wantURL}
	disp := mcpauth.NewDisplay()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan mcpauth.AuthDoneMsg, 1)
	cmd := authorizeMCPCmd(ctx, sess, "linear", disp, m.openBrowser, cancel)
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
		if !errors.Is(msg.Err, context.Canceled) {
			t.Fatalf("err = %v, want a cancellation", msg.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("authorize command did not return after cancellation")
	}

	urls := opened.snapshot()
	if len(urls) != 1 || urls[0] != wantURL {
		t.Fatalf("opener calls = %v, want exactly [%s]", urls, wantURL)
	}
	if disp.URL() != wantURL {
		t.Fatalf("display holds %q, want %q", disp.URL(), wantURL)
	}
}
