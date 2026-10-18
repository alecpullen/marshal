package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
	"marshal/internal/trust"
)

func TestNotifySinkDropsWhenDetached(t *testing.T) {
	var s notifySink
	if err := s.Notify("session/update", map[string]any{"a": 1}); err != nil {
		t.Fatalf("Notify while detached = %v, want nil (dropped)", err)
	}
}

func TestNotifySinkForwardsToAttached(t *testing.T) {
	var s notifySink
	var mu sync.Mutex
	var got []string
	s.Set(func(method string, params any) error {
		mu.Lock()
		got = append(got, method)
		mu.Unlock()
		return nil
	})
	if err := s.Notify("session/update", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	s.Clear()
	if err := s.Notify("session/update", nil); err != nil {
		t.Fatalf("Notify after Clear = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "session/update" {
		t.Fatalf("got %v, want exactly one session/update", got)
	}
}

// TestChainedCancellerReachesOrphanedTurns pins follow-ups doc item #5:
// the manager's turn canceller used to be overwritten by each
// connection, so a turn that outlived its connection became
// uncancellable through the manager. The host-level canceller is now
// registered once in newAgentHost and fans out to every connection
// canceller, so registering a second connection must not strand the
// first's active turn.
func TestChainedCancellerReachesOrphanedTurns(t *testing.T) {
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}

	var mu sync.Mutex
	cancelled := map[string]bool{}

	// Connection 1 registers a canceller with an "active turn" for s1.
	h.addTurnCanceller(func(ctx context.Context, sessionID string) error {
		mu.Lock()
		cancelled[sessionID] = true
		mu.Unlock()
		return nil
	})

	// Connection 2 attaches: with the old overwrite design this
	// replaced the manager's canceller entirely, stranding s1.
	h.addTurnCanceller(func(ctx context.Context, sessionID string) error {
		return nil
	})

	// The manager's canceller must still reach connection 1's entry
	// even though connection 2 has since registered.
	if err := h.chainedCancel(context.Background(), "s1"); err != nil {
		t.Fatalf("chainedCancel: %v", err)
	}
	mu.Lock()
	got := cancelled["s1"]
	mu.Unlock()
	if !got {
		t.Fatal("second connection's registration stranded the first connection's turn: chainedCancel never reached it")
	}
}

// --- runtime auth plumbing -------------------------------------------------

// countingStoreResolver is the test boundary for the host's credential path:
// it records every OpenStore call and hands back a private in-memory store, so
// a test can prove which resolver a runtime exposes without any real keychain.
type countingStoreResolver struct {
	mu     sync.Mutex
	opens  []string
	engine *oauth.Engine
	store  credentials.Store
}

var _ provider.AuthResolver = (*countingStoreResolver)(nil)

func (r *countingStoreResolver) OpenStore(service string) (credentials.Store, error) {
	r.mu.Lock()
	r.opens = append(r.opens, service)
	store := r.store
	r.mu.Unlock()
	if store == nil {
		return nil, errors.New("counting store resolver: no store scripted")
	}
	return store, nil
}

// CodexOAuthEngine refuses: these fixtures declare no OAuth Codex provider, so
// reaching it would mean a credential path the test never intended.
func (r *countingStoreResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	return nil, errors.New("counting store resolver: codex engine must not be requested")
}

func (r *countingStoreResolver) opened() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.opens...)
}

// TestCommandRuntimeForExposesRuntimeStoreOpener pins Task 4's seam being fed
// from the owning runtime: the headless /mcp auth flow must write tokens
// through the resolver the session's runtime selected, so an isolated runtime
// never reaches the real OS keychain and the tokens land where that runtime
// reads them.
func TestCommandRuntimeForExposesRuntimeStoreOpener(t *testing.T) {
	store := credentials.NewMemStore()
	auth := &countingStoreResolver{store: store}

	rt := &app.Runtime{
		State:        &session.State{},
		AuthResolver: auth,
	}
	crt := commandRuntimeFor(rt)
	if crt.OpenStore == nil {
		t.Fatal("CommandRuntime.OpenStore is nil for a runtime that selected a resolver")
	}

	// A zero-value manager has no manager-wide override, so the runtime
	// opener is the one that must win.
	mgr := &CommandManager{}
	got, err := mgr.openCredentialStore(crt)
	if err != nil {
		t.Fatalf("openCredentialStore: %v", err)
	}
	if got != credentials.Store(store) {
		t.Fatal("the credential store did not come from the runtime's selected resolver")
	}
	if opens := auth.opened(); len(opens) != 1 || opens[0] != "marshal" {
		t.Fatalf("resolver OpenStore calls = %v, want [marshal]", opens)
	}

	// A runtime literal with no resolver (embedded/test) leaves the field nil
	// so the manager's documented fallback applies rather than a fabricated
	// opener appearing to be the runtime's choice.
	if bare := commandRuntimeFor(&app.Runtime{State: &session.State{}}); bare.OpenStore != nil {
		t.Fatal("CommandRuntime.OpenStore must stay nil when the runtime selected no resolver")
	}
}

// TestStartRuntimeCarriesInjectedResolverToCommandRuntime is the end-to-end
// half of the same contract: a runtime started through app.StartRuntime with
// an injected resolver must expose that exact resolver on Runtime.AuthResolver,
// and the host projection must then open the injected store — never the real
// keychain — for the session's /mcp auth flow.
func TestStartRuntimeCarriesInjectedResolverToCommandRuntime(t *testing.T) {
	const fixtureConfig = `[project]
name = "acp-auth-fixture"

[commands]
test = "fixture-test-cmd"
`
	home := t.TempDir()
	cfgDir := config.UserDir(home)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserConfigPath(home), []byte(fixtureConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	store := credentials.NewMemStore()
	auth := &countingStoreResolver{store: store}
	work := t.TempDir()

	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		CloseRuntime: func(ctx context.Context, rt *app.Runtime) error { return rt.Close(ctx) },
		Notify:       func(string, any) error { return nil },
		Options: []app.Option{
			app.WithHomeDir(home),
			app.WithTrustResolver(trust.FixedResolver{Decision: trust.DecisionTrustPermanent}),
			app.WithAuthResolver(auth),
		},
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.Create(context.Background(), json.RawMessage(`{"cwd":`+jsonString(work)+`,"mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := res.(SessionResponse).SessionID
	defer func() { _ = m.Close(context.Background(), id) }()

	rt, ok := m.Get(id)
	if !ok {
		t.Fatalf("Get(%s) ok=false", id)
	}
	if rt.AuthResolver == nil {
		t.Fatal("Runtime.AuthResolver is nil after an injected startup")
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("Runtime.AuthResolver is not the injected resolver")
	}

	crt := commandRuntimeFor(rt)
	got, err := (&CommandManager{}).openCredentialStore(crt)
	if err != nil {
		t.Fatalf("openCredentialStore: %v", err)
	}
	if got != credentials.Store(store) {
		t.Fatal("the session's credential store is not the injected one")
	}

	// The fixture declared no providers or MCP servers, so nothing else should
	// have requested a credential store: the only OpenStore call is the
	// explicit one above. A count above 1 would mean a startup path reached
	// credentials it did not need.
	if opens := auth.opened(); len(opens) != 1 {
		t.Fatalf("resolver OpenStore calls = %v, want exactly the explicit one", opens)
	}
}
