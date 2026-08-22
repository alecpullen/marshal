package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	entries       []db.SessionEntry
	nextCursor    string
	err           error
	deleteExisted bool
	lastLimit     int
}

func (f *fakeLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	f.lastLimit = limit
	if f.err != nil {
		return nil, "", f.err
	}
	if limit > 0 && limit < len(f.entries) {
		return f.entries[:limit], "", nil
	}
	return f.entries, f.nextCursor, nil
}

func (f *fakeLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.deleteExisted, nil
}

func (f *fakeLister) Close() error { return nil }

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
		Lister:       &fakeLister{deleteExisted: true},
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
		{"create additional directories over cap", "create", `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["/a","/b","/c","/d","/e","/f","/g","/h","/i"]}`, invalidParams},
		{"create additional directories relative path", "create", `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["relative/path"]}`, invalidParams},
		{"load relative cwd", "load", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"load missing cwd", "load", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"load omitted mcpServers", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x"}`, invalidParams},
		{"load non-empty mcpServers", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, invalidParams},
		{"load additional directories over cap", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/a","/b","/c","/d","/e","/f","/g","/h","/i"]}`, invalidParams},
		{"load additional directories relative path", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["relative/path"]}`, invalidParams},
		{"load missing sessionId", "load", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
		{"load happy path", "load", `{"cwd":"` + absCwd + `","sessionId":"sess_load_ok","mcpServers":[]}`, 0},
		{"create happy path", "create", `{"cwd":"` + absCwd + `","mcpServers":[]}`, 0},
		{"list missing cwd", "list", `{}`, invalidParams},
		{"list relative cwd", "list", `{"cwd":"relative/path"}`, invalidParams},
		{"resume missing sessionId", "resume", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
		{"resume relative cwd", "resume", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"resume missing cwd", "resume", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"resume non-empty mcpServers", "resume", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, invalidParams},
		{"resume additional directories over cap", "resume", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/a","/b","/c","/d","/e","/f","/g","/h","/i"]}`, invalidParams},
		{"resume additional directories relative path", "resume", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["relative/path"]}`, invalidParams},
		{"delete missing cwd", "delete", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"delete relative cwd", "delete", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"delete missing sessionId", "delete", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
		// Delete accepts the natural {cwd, sessionId} payload without mcpServers.
		{"delete natural payload", "delete", `{"cwd":"` + absCwd + `","sessionId":"sess_delete_ok"}`, 0},
		// Delete ignores mcpServers/additionalDirectories rather than rejecting them.
		{"delete non-empty mcpServers", "delete", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, 0},
		{"delete non-empty additional directories", "delete", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/tmp/extra"]}`, 0},
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
			case "resume":
				res, err = m.Resume(context.Background(), json.RawMessage(tc.params))
			case "delete":
				res, err = m.Delete(context.Background(), json.RawMessage(tc.params))
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

func TestSessionPlumbsAdditionalDirectories(t *testing.T) {
	tmp := t.TempDir()
	extra1 := filepath.Join(tmp, "extra1")
	extra2 := filepath.Join(tmp, "extra2")
	if err := os.MkdirAll(extra1, 0o755); err != nil {
		t.Fatalf("mkdir extra1: %v", err)
	}
	if err := os.MkdirAll(extra2, 0o755); err != nil {
		t.Fatalf("mkdir extra2: %v", err)
	}
	if err := writeMarshalConfig(t, tmp); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		CloseRuntime: func(ctx context.Context, rt *app.Runtime) error { return rt.Close(ctx) },
		Notify:       func(method string, params any) error { return nil },
		Options: []app.Option{
			app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		},
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.Create(ctx, json.RawMessage(`{"cwd":"`+tmp+`","mcpServers":[],"additionalDirectories":["`+extra1+`","`+extra2+`"]}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, ok := res.(SessionResponse)
	if !ok {
		t.Fatalf("result type %T", res)
	}

	rt, ok := m.Get(resp.SessionID)
	if !ok {
		t.Fatalf("Get(%s) ok=false", resp.SessionID)
	}
	defer rt.Close(ctx)

	got := rt.AdditionalDirectories()
	if len(got) != 2 || got[0] != extra1 || got[1] != extra2 {
		t.Fatalf("AdditionalDirectories = %v, want [%s %s]", got, extra1, extra2)
	}
}

func TestSessionResumeRestoresWithoutReplay(t *testing.T) {
	var idSeq atomic.Int64
	var notifyCount atomic.Int64
	notifier := func(method string, params any) error {
		notifyCount.Add(1)
		return nil
	}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&idSeq, nil),
		CloseRuntime: noopClose(),
		Notify:       notifier,
	})
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	// Use a fixed pre-existing session id; the fake starter ignores it and
	// returns sess_<n>, so check that the published id differs from the
	// requested id only insofar as the starter controls the SessionID.
	_, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_resume_ok","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if notifyCount.Load() != 0 {
		t.Fatalf("session/update emitted %d notifications, want 0", notifyCount.Load())
	}
}

func TestSessionResumeClosesOldRuntime(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	// Use a fixed session ID so that publish-under-rt.SessionID and
	// publish-under-p.SessionID both store under the same key. The old
	// code published under p.SessionID; the refactored code publishes
	// under rt.SessionID.
	const fixedID = "sess_resume_close"
	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		return &app.Runtime{SessionID: fixedID}, nil
	}
	closer := func(ctx context.Context, rt *app.Runtime) error {
		mu.Lock()
		events = append(events, "close "+rt.SessionID)
		mu.Unlock()
		return nil
	}
	canceller := func(ctx context.Context, id string) error {
		mu.Lock()
		events = append(events, "cancel "+id)
		mu.Unlock()
		return nil
	}
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
	})
	m.SetTurnCanceller(canceller)

	// First resume publishes a runtime under the fixed session ID.
	if _, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"`+fixedID+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	// Second resume for the same id must cancel+close the prior runtime
	// before publishing the new one.
	if _, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"`+fixedID+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "cancel "+fixedID) {
		t.Fatalf("expected turn cancel for %s, events=%q", fixedID, joined)
	}
	if !strings.Contains(joined, "close "+fixedID) {
		t.Fatalf("expected a close event, events=%q", joined)
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

	// Pre-seed the on-disk DB with a project, session, and two messages.
	dbPath := filepath.Join(tmp, ".marshal", "marshal.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := d.Migrate(); err != nil {
		d.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := d.GetOrCreateProject(tmp, "load-test")
	if err != nil {
		d.Close()
		t.Fatalf("create project: %v", err)
	}
	sessionID := "sess_load_test"
	now := time.Unix(100, 0)
	if err := d.CreateSession(sessionID, projectID, "", now); err != nil {
		d.Close()
		t.Fatalf("create session: %v", err)
	}
	if _, err := d.SaveMessage(sessionID, "user", "hello", "plain", now, "", 0, false, 0); err != nil {
		d.Close()
		t.Fatalf("save msg1: %v", err)
	}
	if _, err := d.SaveMessage(sessionID, "assistant", "hi there", "plain", now.Add(time.Second), "", 0, false, 0); err != nil {
		d.Close()
		t.Fatalf("save msg2: %v", err)
	}
	d.Close()

	// Snapshot DB counts.
	initialProjects, initialSessions, initialMessages := countRows(t, tmp)

	// Build a Runtime with the expected state already populated.
	st := session.New(config.Default(), tmp, now, session.Persistence{})
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	st.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	expectedMessages := st.Messages()
	rt := &app.Runtime{SessionID: sessionID, State: st}

	// Wire the SessionManager with the fake start.
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeStartFixed(rt),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(noopCancel())

	ctx := context.Background()
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

func TestSessionListRespectsClientLimit(t *testing.T) {
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	entries := []db.SessionEntry{
		{SessionID: "sess_a", Cwd: absCwd, MessageCount: 1},
		{SessionID: "sess_b", Cwd: absCwd, MessageCount: 2},
	}
	lister := &fakeLister{entries: entries}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       lister,
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.List(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","limit":1}`))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if lister.lastLimit != 1 {
		t.Fatalf("ListSessions limit = %d, want 1", lister.lastLimit)
	}
	obj, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", res)
	}
	sessions, ok := obj["sessions"].([]map[string]any)
	if !ok {
		t.Fatalf("sessions type %T", obj["sessions"])
	}
	if len(sessions) != 1 {
		t.Fatalf("len = %d, want 1 (limit=1)", len(sessions))
	}
	if sessions[0]["sessionId"] != "sess_a" {
		t.Fatalf("sessions[0] = %+v", sessions[0])
	}
}

func TestSessionListDefaultsLimitWhenZeroOrNegative(t *testing.T) {
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	entries := []db.SessionEntry{
		{SessionID: "sess_a", Cwd: absCwd},
		{SessionID: "sess_b", Cwd: absCwd},
		{SessionID: "sess_c", Cwd: absCwd},
	}
	lister := &fakeLister{entries: entries}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       lister,
	})
	m.SetTurnCanceller(noopCancel())

	for _, limit := range []int{0, -5} {
		body := `{"cwd":"` + absCwd + `"`
		if limit != 0 {
			body += fmt.Sprintf(`,"limit":%d`, limit)
		}
		body += `}`
		if _, err := m.List(context.Background(), json.RawMessage(body)); err != nil {
			t.Fatalf("List(limit=%d): %v", limit, err)
		}
		if lister.lastLimit != defaultSessionListLimit {
			t.Fatalf("limit=%d: ListSessions limit = %d, want %d", limit, lister.lastLimit, defaultSessionListLimit)
		}
	}
}

func TestSessionListRejectsUntrustedRoot(t *testing.T) {
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       &fakeLister{},
	})
	m.SetTurnCanceller(noopCancel())

	_, err := m.List(context.Background(), json.RawMessage(`{"cwd":"/"}`))
	if err == nil {
		t.Fatal("expected error for cwd outside trusted roots")
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
		t.Fatalf("expected invalidParams, got %v", err)
	}
}

func TestSessionDeleteWithLoadedRuntime(t *testing.T) {
	var idSeq atomic.Int64
	var closeCount atomic.Int64
	starter := fakeRuntimeStart(&idSeq, nil)
	closer := func(ctx context.Context, rt *app.Runtime) error {
		closeCount.Add(1)
		return nil
	}
	canceller := func(ctx context.Context, id string) error { return nil }

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
		Notify:       func(method string, params any) error { return nil },
		Lister:       &fakeLister{deleteExisted: true},
	})
	m.SetTurnCanceller(canceller)

	if _, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sessID := "sess_1"
	if _, ok := m.Get(sessID); !ok {
		t.Fatalf("expected runtime for %s", sessID)
	}

	res, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"`+sessID+`","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	obj, ok := res.(map[string]any)
	if !ok || len(obj) != 0 {
		t.Fatalf("result = %v (type %T), want empty map", res, res)
	}
	if _, ok := m.Get(sessID); ok {
		t.Fatalf("runtime still reachable after Delete")
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closer called %d times, want 1", got)
	}
}

func TestSessionDeleteWithoutLoadedRuntime(t *testing.T) {
	canceller := func(ctx context.Context, id string) error { return nil }
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
		Lister:       &fakeLister{deleteExisted: true},
	})
	m.SetTurnCanceller(canceller)

	res, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_orphan","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	obj, ok := res.(map[string]any)
	if !ok || len(obj) != 0 {
		t.Fatalf("result = %v (type %T), want empty map", res, res)
	}
}

func TestSessionDeleteUnknownIdReturnsServerError(t *testing.T) {
	canceller := func(ctx context.Context, id string) error { return nil }
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
		Lister:       &fakeLister{deleteExisted: false},
	})
	m.SetTurnCanceller(canceller)

	_, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_nonexistent","mcpServers":[]}`))
	if err == nil {
		t.Fatalf("expected error for unknown session")
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error is not jsonRPCError: %v", err)
	}
	if rpcErr.Code != serverError {
		t.Fatalf("code = %d, want %d", rpcErr.Code, serverError)
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

// TestPublishReplacementDoesNotDoubleClose reproduces F-BUG-49: a
// concurrent publishReplacement with the same id must not call
// close(prior) twice. Post-fix, the second call sees rt == prior and
// short-circuits.
func TestPublishReplacementDoesNotDoubleClose(t *testing.T) {
	var closeCount atomic.Int32
	m := &SessionManager{
		sessions:    map[string]*app.Runtime{},
		mu:          sync.RWMutex{},
		lifecycleMu: sync.Mutex{},
		close: func(ctx context.Context, rt *app.Runtime) error {
			closeCount.Add(1)
			return nil
		},
		cancel: nil,
	}
	rt1 := &app.Runtime{}
	rt2 := &app.Runtime{}
	m.sessions["s1"] = rt1

	// Two concurrent publishes — both observe the same prior.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { m.publishReplacement(context.Background(), "s1", rt2); wg.Done() }()
	go func() { m.publishReplacement(context.Background(), "s1", rt2); wg.Done() }()
	wg.Wait()

	// Only one close should have happened.
	if got := closeCount.Load(); got > 1 {
		t.Errorf("close was called %d times, want at most 1", got)
	}
}

// TestSessionManagerLogsReplacement verifies that publishReplacement emits
// an Info-level log line when a prior runtime is replaced.
func TestPublishReplacementSyncOnceGuard(t *testing.T) {
	var closeCount atomic.Int32
	m := &SessionManager{
		sessions:    map[string]*app.Runtime{},
		mu:          sync.RWMutex{},
		lifecycleMu: sync.Mutex{},
		close: func(ctx context.Context, rt *app.Runtime) error {
			closeCount.Add(1)
			return nil
		},
		cancel: func(ctx context.Context, id string) error { return nil },
	}
	rt1 := &app.Runtime{}
	rt2 := &app.Runtime{}
	m.sessions["s1"] = rt1

	// Three concurrent publishes — all observe the same prior.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { m.publishReplacement(context.Background(), "s1", rt2); wg.Done() }()
	go func() { m.publishReplacement(context.Background(), "s1", rt2); wg.Done() }()
	go func() { m.publishReplacement(context.Background(), "s1", rt2); wg.Done() }()
	wg.Wait()

	if got := closeCount.Load(); got > 1 {
		t.Errorf("close was called %d times, want at most 1", got)
	}
}

func TestReplaceExistingAndPublishReplacementNoDoubleClose(t *testing.T) {
	// The two lifecycle paths may both observe the same prior runtime rt1
	// (publishReplacement replacing it, and a concurrent replaceExisting
	// that already detached it). The same pointer must be closed at most
	// once even when the paths race.
	var closeCount atomic.Int32
	m := &SessionManager{
		sessions:    map[string]*app.Runtime{},
		mu:          sync.RWMutex{},
		lifecycleMu: sync.Mutex{},
		close: func(ctx context.Context, rt *app.Runtime) error {
			closeCount.Add(1)
			return nil
		},
		cancel: func(ctx context.Context, id string) error { return nil },
	}
	rt1 := &app.Runtime{}
	rt2 := &app.Runtime{}
	m.sessions["s1"] = rt1

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		// publishReplacement tears down the prior pointer rt1.
		m.publishReplacement(context.Background(), "s1", rt2)
		wg.Done()
	}()
	go func() {
		// Simulate replaceExisting having already detached rt1 and racing
		// to close the very same pointer.
		_ = m.closeRuntimeOnce(context.Background(), rt1)
		wg.Done()
	}()
	wg.Wait()

	if got := closeCount.Load(); got > 1 {
		t.Errorf("close was called %d times for the same runtime, want at most 1", got)
	}
}

func TestSessionManagerLogsReplacement(t *testing.T) {
	var buf bytes.Buffer
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	}, WithSessionManagerLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	// First Create publishes a runtime.
	res, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	sid := res.(SessionResponse).SessionID

	// Second Create with the same id forces a replacement. The fake starter
	// always returns a fresh id, so we cannot force a same-id replacement
	// through Create. Instead, publish a second runtime directly.
	rt2 := &app.Runtime{SessionID: "sess_replacement"}
	m.publishReplacement(context.Background(), sid, rt2)

	if !strings.Contains(buf.String(), "publishReplacement") {
		t.Fatalf("expected replacement log, got %q", buf.String())
	}
}

// TestCreateClosesRuntimeWhenIsolationFails verifies that a session/new
// whose isolation step fails tears down the started runtime instead of
// publishing a live session at the project root with no usable id.
func TestCreateClosesRuntimeWhenIsolationFails(t *testing.T) {
	var closed atomic.Int64
	closeFn := func(ctx context.Context, rt *app.Runtime) error {
		closed.Add(1)
		return nil
	}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: closeFn,
		Notify:       func(method string, params any) error { return nil },
	})
	m.SetTurnCanceller(noopCancel())

	// A non-git temp dir makes isolateSession's RevParse fail.
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	_, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[],"isolation":{"branch":"f"}}`))
	if err == nil {
		t.Fatal("expected an error when isolation fails")
	}
	if closed.Load() != 1 {
		t.Fatalf("runtime close count = %d, want 1 — the started runtime must be torn down", closed.Load())
	}
	if _, ok := m.Get("sess_1"); ok {
		t.Fatal("the failed session must not be published")
	}
}

// TestSessionManagerLogsCloseNoop verifies that Close logs at Debug when
// the session id is unknown.
func TestSessionManagerLogsCloseNoop(t *testing.T) {
	var buf bytes.Buffer
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	}, WithSessionManagerLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	m.SetTurnCanceller(noopCancel())

	// Close an unknown id — this should log at Debug and return an error.
	err := m.Close(context.Background(), "sess_does_not_exist")
	if err == nil {
		t.Fatalf("expected error for unknown session")
	}
	if !strings.Contains(buf.String(), "session close (no-op)") {
		t.Fatalf("expected no-op close log, got %q", buf.String())
	}
}

// TestSessionManagerLogsCloseTeardown verifies that Close logs at Debug
// when a session is successfully torn down.
func TestSessionManagerLogsCloseTeardown(t *testing.T) {
	var buf bytes.Buffer
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Notify:       func(method string, params any) error { return nil },
	}, WithSessionManagerLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	res, err := m.Create(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sid := res.(SessionResponse).SessionID

	if err := m.Close(context.Background(), sid); err != nil {
		t.Fatalf("Close: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "session close") || strings.Contains(logged, "(no-op)") {
		t.Fatalf("expected teardown close log (without no-op), got %q", logged)
	}
}

// TestValidateWorkingPathsRejectsEtc verifies that validateWorkingPaths
// rejects paths outside the trusted-roots allow-list (F-SEC-33).
func TestValidateWorkingPathsRejectsEtc(t *testing.T) {
	if err := validateWorkingPaths("/etc", nil); err == nil {
		t.Fatal("expected /etc to be rejected")
	}
	if err := validateWorkingPaths(t.TempDir(), []string{"/root"}); err == nil {
		t.Fatal("expected /root to be rejected as additionalDirectory")
	}
	home := os.Getenv("HOME")
	if home != "" {
		if err := validateWorkingPaths(home, nil); err != nil {
			t.Fatalf("expected home %q to be allowed, got %v", home, err)
		}
	}
}

// TestValidateLifecycleParamsRejectsSymlinkDuplicate reproduces
// F-POL-65: 8 symlinks pointing to the same sensitive dir must be
// rejected as duplicates.
func TestValidateLifecycleParamsRejectsSymlinkDuplicate(t *testing.T) {
	// Create a real dir and 8 symlinks pointing to it.
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	var addDirs []string
	for i := 0; i < 8; i++ {
		link := filepath.Join(tmp, fmt.Sprintf("link%d", i))
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		addDirs = append(addDirs, link)
	}
	params := sessionParams{
		Cwd:                   real,
		MCPServers:            &[]json.RawMessage{},
		AdditionalDirectories: addDirs,
	}
	err := validateLifecycleParams(&params, true)
	if err == nil {
		t.Fatal("expected error for symlink duplicates, got nil")
	}
}

func TestValidateLifecycleParamsAcceptsNonExistentAdditionalDir(t *testing.T) {
	tmp := t.TempDir()
	absCwd, err := filepath.Abs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// A non-existent subdirectory inside the cwd — should be accepted
	// (it may be created later by the agent).
	nonExistent := filepath.Join(absCwd, "future_dir")
	params := sessionParams{
		Cwd:                   absCwd,
		SessionID:             "sess_test",
		MCPServers:            &[]json.RawMessage{},
		AdditionalDirectories: []string{nonExistent},
	}
	err = validateLifecycleParams(&params, true)
	if err != nil {
		t.Fatalf("expected nil error for non-existent additional dir, got: %v", err)
	}
}

func TestCheckPathCleansUnresolvedPath(t *testing.T) {
	tmp := t.TempDir()
	// Resolve symlinks (e.g. /var -> /private/var on macOS) so the trusted
	// root matches what EvalSymlinks produces for existing paths.
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	trusted := []string{resolved}

	// A non-existent path inside the root that cleans to a location inside
	// the root should be accepted. EvalSymlinks fails (the intermediate
	// "foo" does not exist), so the fallback must clean the .. components
	// before the containment check.
	insidePath := filepath.Join(resolved, "foo", "..", "..", filepath.Base(resolved))
	if err := checkPath(insidePath, trusted); err != nil {
		t.Fatalf("path inside trusted root after cleaning should be accepted: %v", err)
	}

	// A non-existent path that cleans to a location outside the root should
	// be rejected.
	escapePath := filepath.Join(resolved, "..", "..", "..", "etc")
	if err := checkPath(escapePath, trusted); err == nil {
		t.Fatal("path outside trusted root after cleaning should be rejected")
	}
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
