package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/trust"
)

// newFakeState returns a fresh session.State with three messages (user,
// assistant, system) so replay tests have something to project.
func newFakeState(t *testing.T) *session.State {
	t.Helper()
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	st.AddMessage(session.RoleUser, "user-1", session.ContentTypePlain)
	st.AddMessage(session.RoleAssistant, "assistant-1", session.ContentTypePlain)
	st.AddMessage(session.RoleSystem, "system-1", session.ContentTypePlain)
	return st
}

// fakeRuntimeStart builds a RuntimeStarter that returns a new *app.Runtime
// per call. Each call increments idSeq; the SessionID is "sess_<n>". If
// state is non-nil it is installed on the returned runtime.
func fakeRuntimeStart(idSeq *atomic.Int64, state *session.State) RuntimeStarter {
	return func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		id := idSeq.Add(1)
		return &app.Runtime{SessionID: "sess_" + strconv.FormatInt(id, 10), State: state}, nil
	}
}

// fakeStartFixed returns a StartRuntime that ignores the supplied id and
// always returns rt. Used to control what StartRuntime produces.
func fakeStartFixed(rt *app.Runtime) RuntimeStarter {
	return func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		return rt, nil
	}
}

// noopClose returns a CloseRuntime that always succeeds.
func noopClose() RuntimeCloser {
	return func(ctx context.Context, rt *app.Runtime) error { return nil }
}

// noopCancel returns a TurnCanceller that always succeeds.
func noopCancel() TurnCanceller {
	return func(ctx context.Context, id string) error { return nil }
}

// fakeLister implements SessionLister for testing.
type fakeLister struct {
	entries    []db.SessionEntry
	nextCursor string
	err        error
}

func (f *fakeLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.entries, f.nextCursor, nil
}

func TestSessionLifecycleValidation(t *testing.T) {
	tmpDir := t.TempDir()
	absCwd, err := filepath.Abs(tmpDir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	var idSeq atomic.Int64
	// nil State is acceptable for the load happy path because replay
	// short-circuits when rt.State is nil (replay is exercised fully by
	// TestSessionLoadReplaysActiveBranchBeforeReturning).
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&idSeq, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
		Lister:       &fakeLister{},
	})
	m.SetTurnCanceller(noopCancel())

	cases := []struct {
		name     string
		method   string
		params   string
		wantCode int
	}{
		{"create relative cwd", "create", `{"cwd":"relative/path","mcpServers":[]}`, invalidParams},
		{"create missing cwd", "create", `{"mcpServers":[]}`, invalidParams},
		{"create omitted mcpServers", "create", `{"cwd":"` + absCwd + `"}`, invalidParams},
		{"create non-empty mcpServers", "create", `{"cwd":"` + absCwd + `","mcpServers":[{"name":"x"}]}`, invalidParams},
		{"create non-empty additional directories", "create", `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["/tmp"]}`, invalidParams},
		{"load relative cwd", "load", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"load missing cwd", "load", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"load omitted mcpServers", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x"}`, invalidParams},
		{"load non-empty mcpServers", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, invalidParams},
		{"load non-empty additional directories", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/tmp"]}`, invalidParams},
		{"load missing sessionId", "load", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
		{"load happy path", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_load_ok","mcpServers":[]}`, 0},
		{"create happy path", "create", `{"cwd":"` + absCwd + `","mcpServers":[]}`, 0},
		{"list missing cwd", "list", `{}`, invalidParams},
		{"list relative cwd", "list", `{"cwd":"relative/path"}`, invalidParams},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				res any
				err error
			)
			switch tc.method {
			case "create":
				res, err = m.Create(context.Background(), json.RawMessage(tc.params))
			case "load":
				res, err = m.Load(context.Background(), json.RawMessage(tc.params))
			case "list":
				res, err = m.List(context.Background(), json.RawMessage(tc.params))
			}
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.method == "create" && res == nil {
					t.Fatalf("expected non-nil result for happy path")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got result=%v", res)
			}
			var rpcErr *jsonRPCError
			if !errors.As(err, &rpcErr) {
				t.Fatalf("error is not jsonRPCError: %v", err)
			}
			if rpcErr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (err=%v)", rpcErr.Code, tc.wantCode, err)
			}
		})
	}
}

func TestSessionLoadClosesOldBeforeStartingReplacement(t *testing.T) {
	var idSeq atomic.Int64
	var (
		mu        sync.Mutex
		events    []string
		createdID string
		oldSeen   = make(chan struct{}, 1)
	)
	starter := fakeRuntimeStart(&idSeq, nil)
	inner := starter
	starter = func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		rt, err := inner(ctx, opts...)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		events = append(events, "start "+rt.SessionID)
		if createdID == "" {
			createdID = rt.SessionID
		}
		mu.Unlock()
		return rt, nil
	}
	canceller := func(ctx context.Context, id string) error {
		mu.Lock()
		events = append(events, "cancel "+id)
		mu.Unlock()
		return nil
	}
	closer := func(ctx context.Context, rt *app.Runtime) error {
		mu.Lock()
		events = append(events, "close "+rt.SessionID)
		mu.Unlock()
		select {
		case oldSeen <- struct{}{}:
		default:
		}
		return nil
	}

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(canceller)

	// Pre-create: Start call #1 returns sess_1.
	if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mu.Lock()
	oldID := createdID
	mu.Unlock()
	if oldID == "" {
		t.Fatalf("Create did not record a session id")
	}

	// Load with the same id Start produced.
	_, err := m.Load(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"`+oldID+`","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	<-oldSeen

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 4 {
		t.Fatalf("events = %v, want at least 4 (start1, cancel, close, start2)", events)
	}
	last := events[len(events)-3:]
	if last[0] != "cancel "+oldID || last[1] != "close "+oldID || !strings.HasPrefix(last[2], "start sess_") {
		t.Fatalf("last three events = %v, want cancel/close/start", last)
	}
	if last[2] == "start "+oldID {
		t.Fatalf("start did not produce a fresh runtime id (old=%s)", oldID)
	}
}

func TestSessionCloseRemovesCancelsAndCloses(t *testing.T) {
	var idSeq atomic.Int64
	closeStartedCh := make(chan struct{}, 1)
	releaseCloser := make(chan struct{})

	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		_ = idSeq.Add(1)
		return &app.Runtime{SessionID: "sess_close", State: nil}, nil
	}
	canceller := func(ctx context.Context, id string) error { return nil }
	closer := func(ctx context.Context, rt *app.Runtime) error {
		select {
		case closeStartedCh <- struct{}{}:
		default:
		}
		<-releaseCloser
		return nil
	}

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(canceller)

	if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wrap Close so we can observe the moment the runtime is removed from
	// the map (Get returns false) before the closer is unblocked.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- m.Close(context.Background(), "sess_close")
	}()

	// Closer has been called and is blocked.
	<-closeStartedCh
	if _, ok := m.Get("sess_close"); ok {
		t.Fatalf("Get after Close removed entry returned ok=true; want false")
	}
	close(releaseCloser)

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not return after closer released")
	}
	if _, ok := m.Get("sess_close"); ok {
		t.Fatalf("Get after Close returned ok=true")
	}
}

func TestSessionCloseUnknownReturnsServerError(t *testing.T) {
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, nil
		},
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(noopCancel())

	err := m.Close(context.Background(), "sess_does_not_exist")
	if err == nil {
		t.Fatalf("expected error closing unknown id")
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error is not jsonRPCError: %v", err)
	}
	if rpcErr.Code != serverError {
		t.Fatalf("code = %d, want %d", rpcErr.Code, serverError)
	}
}

func TestSessionCloseAllAttemptsEveryRuntimeAndJoinsErrors(t *testing.T) {
	var idSeq atomic.Int64
	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		id := idSeq.Add(1)
		return &app.Runtime{SessionID: "sess_" + strconv.FormatInt(id, 10)}, nil
	}
	cancelErr := errors.New("cancel-1")
	closeErr := errors.New("close-2")
	canceller := func(ctx context.Context, id string) error {
		if id == "sess_1" {
			return cancelErr
		}
		return nil
	}
	closer := func(ctx context.Context, rt *app.Runtime) error {
		if rt.SessionID == "sess_2" {
			return closeErr
		}
		return nil
	}

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(canceller)

	for i := 0; i < 3; i++ {
		if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	err := m.CloseAll(context.Background())
	if err == nil {
		t.Fatalf("CloseAll: expected joined error")
	}
	if !errors.Is(err, cancelErr) {
		t.Fatalf("CloseAll err does not include cancel-1: %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("CloseAll err does not include close-2: %v", err)
	}
	// Map should be empty.
	for _, id := range []string{"sess_1", "sess_2", "sess_3"} {
		if _, ok := m.Get(id); ok {
			t.Fatalf("Get(%s) ok=true after CloseAll", id)
		}
	}
}

func TestSessionCloseAllIsIdempotent(t *testing.T) {
	var idSeq atomic.Int64
	closeCount := atomic.Int64{}
	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		_ = idSeq.Add(1)
		return &app.Runtime{SessionID: "sess_idem"}, nil
	}
	closer := func(ctx context.Context, rt *app.Runtime) error {
		closeCount.Add(1)
		return nil
	}
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(noopCancel())

	if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.CloseAll(context.Background()); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	if err := m.CloseAll(context.Background()); err != nil {
		t.Fatalf("CloseAll second: %v", err)
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closer called %d times, want 1", got)
	}
}

func TestSessionLoadReplaysActiveBranchBeforeReturning(t *testing.T) {
	state := newFakeState(t)
	rt := &app.Runtime{SessionID: "sess_replay", State: state}

	starter := fakeStartFixed(rt)
	var (
		mu      sync.Mutex
		updates []map[string]any
	)
	notify := func(method string, params any) error {
		if method != "session/update" {
			return nil
		}
		sup, ok := params.(SessionUpdateParams)
		if !ok {
			return nil
		}
		mu.Lock()
		updates = append(updates, sup.Update)
		mu.Unlock()
		return nil
	}

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: noopClose(),
		Notify:       notify,
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.Load(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_replay","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res != nil {
		t.Fatalf("Load result = %v, want nil", res)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(updates))
	}
	want := []string{"user_message_chunk", "agent_message_chunk", "agent_message_chunk"}
	for i, u := range updates {
		if u["kind"] != want[i] {
			t.Fatalf("updates[%d].kind = %v, want %s", i, u["kind"], want[i])
		}
	}
}

func TestSessionLoadReplayFailureRemovesAndClosesRuntime(t *testing.T) {
	state := newFakeState(t)
	rt := &app.Runtime{SessionID: "sess_replay_fail", State: state}
	starter := fakeStartFixed(rt)

	notifyErr := errors.New("notify-fail")
	notifyCalls := atomic.Int64{}
	notify := func(method string, params any) error {
		if method != "session/update" {
			return nil
		}
		if notifyCalls.Add(1) == 2 {
			return notifyErr
		}
		return nil
	}

	closeCount := atomic.Int64{}
	closer := func(ctx context.Context, r *app.Runtime) error {
		closeCount.Add(1)
		return nil
	}

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       notify,
	})
	m.SetTurnCanceller(noopCancel())

	_, err := m.Load(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_replay_fail","mcpServers":[]}`))
	if err == nil {
		t.Fatalf("Load: expected error")
	}
	if !errors.Is(err, notifyErr) {
		t.Fatalf("Load err = %v, want includes notifyErr", err)
	}
	if _, ok := m.Get("sess_replay_fail"); ok {
		t.Fatalf("Get after replay failure ok=true; want false")
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closer called %d times, want 1", got)
	}
}

func TestSessionLoadUsesExistingSessionOption(t *testing.T) {
	tmp := t.TempDir()
	if err := writeMarshalConfig(t, tmp); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	now := time.Unix(100, 0)

	// First runtime: create a session and persist messages.
	rt1, err := app.StartRuntime(ctx, app.WithWorkingDir(tmp), app.WithSkipOnboarding(true),
		app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		app.WithNow(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("first StartRuntime: %v", err)
	}
	rt1.State.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	rt1.State.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	expectedMessages := rt1.State.Messages()
	sessionID := rt1.SessionID
	rt1.Close(ctx)

	// Snapshot DB counts.
	initialProjects, initialSessions, initialMessages := countRows(t, tmp)

	// Build a SessionManager that wraps real StartRuntime.
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		CloseRuntime: func(ctx context.Context, rt *app.Runtime) error { return rt.Close(ctx) },
		Notify:       func(method string, params any) error { return nil },
		Options: []app.Option{
			app.WithSkipOnboarding(true),
			app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		},
	})
	m.SetTurnCanceller(func(ctx context.Context, id string) error { return nil })

	res, err := m.Load(ctx, json.RawMessage(`{"cwd":"`+tmp+`","sessionId":"`+sessionID+`","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("manager Load: %v", err)
	}
	if res != nil {
		t.Fatalf("manager Load result = %v, want nil", res)
	}

	loaded, ok := m.Get(sessionID)
	if !ok {
		t.Fatalf("Get(%s) ok=false after Load", sessionID)
	}
	defer loaded.Close(ctx)

	gotMessages := loaded.State.Messages()
	if len(gotMessages) != len(expectedMessages) {
		t.Fatalf("messages = %d, want %d", len(gotMessages), len(expectedMessages))
	}
	for i, m := range gotMessages {
		if m.Content != expectedMessages[i].Content || m.Role != expectedMessages[i].Role {
			t.Fatalf("messages[%d] = %+v, want %+v", i, m, expectedMessages[i])
		}
	}

	projects, sessions, messages := countRows(t, tmp)
	if projects != initialProjects || sessions != initialSessions || messages != initialMessages {
		t.Fatalf("counts changed: projects %d->%d, sessions %d->%d, messages %d->%d",
			initialProjects, projects, initialSessions, sessions, initialMessages, messages)
	}
}

func TestSessionCloseSessionParsesAndReturnsEmptyObject(t *testing.T) {
	var idSeq atomic.Int64
	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		_ = idSeq.Add(1)
		return &app.Runtime{SessionID: "sess_xx"}, nil
	}
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(noopCancel())

	if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := m.CloseSession(context.Background(), json.RawMessage(`{"sessionId":"sess_xx"}`))
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	m2, ok := res.(map[string]any)
	if !ok || len(m2) != 0 {
		t.Fatalf("CloseSession result = %v, want empty map[string]any{}", res)
	}
}

func TestSessionListProjectsFromLister(t *testing.T) {
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	want := []db.SessionEntry{
		{SessionID: "sess_a", Cwd: absCwd, Title: "A", UpdatedAt: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), MessageCount: 2},
		{SessionID: "sess_b", Cwd: absCwd, Title: "", UpdatedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), MessageCount: 0},
	}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       &fakeLister{entries: want, nextCursor: ""},
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.List(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`"}`))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	obj, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", res)
	}
	sessions, ok := obj["sessions"].([]map[string]any)
	if !ok {
		t.Fatalf("sessions type %T", obj["sessions"])
	}
	if len(sessions) != 2 {
		t.Fatalf("len = %d", len(sessions))
	}
	if sessions[0]["sessionId"] != "sess_a" {
		t.Fatalf("sessions[0] = %+v", sessions[0])
	}
	if sessions[0]["updatedAt"] != "2026-07-03T00:00:00Z" {
		t.Fatalf("updatedAt = %v", sessions[0]["updatedAt"])
	}
	if sessions[0]["cwd"] != absCwd {
		t.Fatalf("cwd = %v", sessions[0]["cwd"])
	}
	if _, hasTitle := sessions[0]["title"].(string); hasTitle && sessions[0]["title"] != "A" {
		t.Fatalf("title = %v", sessions[0]["title"])
	}
	meta, _ := sessions[0]["_meta"].(map[string]any)
	if meta == nil || meta["messageCount"] != float64(2) {
		t.Fatalf("_meta = %+v", meta)
	}
	if sessions[1]["title"] != "" && sessions[1]["title"] != nil {
		t.Fatalf("empty title should be omitted, got %v", sessions[1]["title"])
	}
	if _, hasNext := obj["nextCursor"]; hasNext {
		t.Fatalf("unexpected nextCursor: %+v", obj["nextCursor"])
	}
}

// fakeTrustResolver is duplicated here to avoid pulling in app_test's
// internal test helpers.
type fakeTrustResolver struct {
	decision trust.Decision
}

func (f *fakeTrustResolver) Resolve(workingDir string, hasProjectConfig bool) (trust.Decision, error) {
	return f.decision, nil
}

func (f *fakeTrustResolver) Record(workingDir string, decision trust.Decision) error {
	return nil
}

func writeMarshalConfig(t *testing.T, tmp string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		return err
	}
	content := `[project]
name = "load-test"
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true
[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`
	return os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(content), 0644)
}

func countRows(t *testing.T, tmp string) (int, int, int) {
	t.Helper()
	d, err := db.Open(filepath.Join(tmp, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	var p, s, m int
	if err := d.SQLDB().QueryRow("SELECT COUNT(*) FROM projects").Scan(&p); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := d.SQLDB().QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&s); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := d.SQLDB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&m); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return p, s, m
}
