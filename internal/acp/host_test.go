package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
	"marshal/internal/trust"
	"marshal/internal/viewmodel"
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

// TestWorkACPAvailableWithoutModelRunner pins Task 21's lookup shape: the
// session/work_* methods resolve a session's runtime as a viewmodel.WorkService
// straight from the SessionManager, without the prompt path's requirement for
// an available Runner. A session whose runtime has no Runner (its model is
// still building, or it is headless) must still serve work list/inspect/stop/
// acknowledge, and the prompt path must stay independently gated.
func TestWorkACPAvailableWithoutModelRunner(t *testing.T) {
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}

	// A runner-less runtime: no Runner, and no work coordinator either, so the
	// live snapshot is the empty projection rather than a rejection.
	h.manager.sessions["s1"] = &app.Runtime{}

	srv := NewServer(strings.NewReader(""), io.Discard)
	var alive atomic.Bool
	alive.Store(true)
	cleanup := h.registerHandlers(srv, &alive)
	defer cleanup()

	for _, method := range []string{"session/work_list", "session/work_inspect", "session/work_stop", "session/work_acknowledge"} {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("%s is not registered", method)
		}
	}

	handler, ok := srv.handlers["session/work_list"]
	if !ok {
		t.Fatal("session/work_list is not registered")
	}
	res, err := handler(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatalf("work_list for a runner-less session: %v", err)
	}
	if _, ok := res.(WorkSnapshotWire); !ok {
		t.Fatalf("work_list result = %T, want WorkSnapshotWire", res)
	}

	// The prompt path is still registered independently; the point is only that
	// work availability does not depend on it.
	if _, ok := srv.handlers["session/prompt"]; !ok {
		t.Fatal("session/prompt must stay registered")
	}

	// cleanup is idempotent: a second call is a no-op.
	cleanup()
}

// TestWorkACPFinalUpdateBeforeClose pins the connection-close ordering: the
// coordinator's final settlement snapshot is published while the transport is
// still up, the forwarder carries it out, and Close then cancels and joins the
// forwarder outside the controller mutex. Nothing is inferred from a transport
// that has already closed, and Close stays idempotent.
func TestWorkACPFinalUpdateBeforeClose(t *testing.T) {
	svc := newScriptedWorkService("e1")
	notices := &workNotices{}
	m := NewWorkManagerACP(func(id string) (viewmodel.WorkService, bool) {
		if id != "s1" {
			return nil, false
		}
		return svc, true
	}, notices.notify)
	t.Cleanup(svc.broker.Close)

	if _, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	svc.publish(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e1", Revision: 12, StoppedReceiptCount: 1})
	waitFor(t, "the final work_state before close", func() bool { return notices.count() == 1 })

	joined := make(chan struct{})
	go func() {
		m.Close()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join the forwarder")
	}

	// The transport is gone: a later publish reaches no reader, so no delivery
	// is inferred from it.
	svc.publish(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e1", Revision: 13})
	if got := notices.count(); got != 1 {
		t.Fatalf("updates after close = %d, want only the final one", got)
	}
	if got := notices.all()[0]; got.Revision != 12 || got.StoppedReceiptCount != 1 {
		t.Fatalf("final delivery = %+v", got)
	}
	m.Close()
}
