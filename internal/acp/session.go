package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"marshal/internal/app"
)

// RuntimeStarter abstracts app.StartRuntime so SessionManager can be tested
// without booting the full headless runtime.
type RuntimeStarter func(context.Context, ...app.Option) (*app.Runtime, error)

// RuntimeCloser closes a *app.Runtime. The default is rt.Close(ctx) when
// rt is non-nil. Tests may inject a recording closer.
type RuntimeCloser func(context.Context, *app.Runtime) error

// TurnCanceller cancels any in-flight turn for a session. Required before
// Load, Close, or CloseAll.
type TurnCanceller func(context.Context, string) error

// SessionManagerConfig configures a SessionManager.
type SessionManagerConfig struct {
	// StartRuntime builds a new *app.Runtime for a session. Required.
	StartRuntime RuntimeStarter
	// CloseRuntime tears down a runtime. If nil, defaults to rt.Close(ctx)
	// with a nil-safe guard.
	CloseRuntime RuntimeCloser
	// Notify sends JSON-RPC notifications. Required for Load (replay).
	Notify NotifyFunc
	// Options are appended to every StartRuntime call. Useful for
	// headless defaults like WithSkipOnboarding.
	Options []app.Option
}

// SessionManager owns the map from ACP session IDs to *app.Runtime. It
// validates session lifecycle parameters, replays the active branch on
// load, replaces prior runtimes, and tears them down on close.
//
// Concurrency model:
//   - mu (sync.RWMutex) protects the sessions map for fast lookups.
//   - lifecycleMu serialises structural mutations (replace, close-all).
//     The lock is taken in a fixed order: lifecycleMu before mu, never
//     the reverse, to prevent deadlock with concurrent Get.
type SessionManager struct {
	start   RuntimeStarter
	close   RuntimeCloser
	notify  NotifyFunc
	options []app.Option

	cancel TurnCanceller

	mu          sync.RWMutex
	sessions    map[string]*app.Runtime
	lifecycleMu sync.Mutex
}

// SessionResponse is the successful result shape for session/new.
// session/load returns a null result once replay completes.
type SessionResponse struct {
	SessionID string `json:"sessionId"`
}

// sessionParams is the subset of ACP session/new and session/load params
// we honor. mcpServers must be an explicit empty array; additional
// directories are reserved for a future task and currently rejected.
type sessionParams struct {
	Cwd                   string             `json:"cwd"`
	SessionID             string             `json:"sessionId"`
	MCPServers            *[]json.RawMessage `json:"mcpServers"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
}

// NewSessionManager constructs a SessionManager.
func NewSessionManager(cfg SessionManagerConfig) *SessionManager {
	closeFn := cfg.CloseRuntime
	if closeFn == nil {
		closeFn = func(ctx context.Context, rt *app.Runtime) error {
			if rt == nil {
				return nil
			}
			return rt.Close(ctx)
		}
	}
	return &SessionManager{
		start:    cfg.StartRuntime,
		close:    closeFn,
		notify:   cfg.Notify,
		options:  cfg.Options,
		sessions: map[string]*app.Runtime{},
	}
}

// SetTurnCanceller registers the per-session turn cancellation function.
// Must be set before Load, Close, or CloseAll. Stored as a reference so
// tests can swap it after construction; the function is read with the
// lifecycleMu held.
func (m *SessionManager) SetTurnCanceller(cancel TurnCanceller) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.cancel = cancel
}

// requireReady returns the configured canceller if non-nil. Used by Load,
// Close, and CloseAll to fail fast on a misconfigured manager.
func (m *SessionManager) requireReady() (TurnCanceller, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.cancel == nil {
		return nil, serverErrorf("SessionManager has no TurnCanceller configured")
	}
	return m.cancel, nil
}

// validateLifecycleParams enforces the F21 v1 supported-parameter matrix.
// Returns an *jsonRPCError with code invalidParams on the first failure.
func validateLifecycleParams(p *sessionParams, requireSessionID bool) error {
	if strings.TrimSpace(p.Cwd) == "" {
		return invalidParamsError("cwd is required")
	}
	if !filepath.IsAbs(p.Cwd) {
		return invalidParamsError("cwd must be an absolute path")
	}
	if p.MCPServers == nil {
		return invalidParamsError("mcpServers is required and must be an explicit empty array")
	}
	if len(*p.MCPServers) > 0 {
		return invalidParamsError("mcpServers is not yet supported")
	}
	if len(p.AdditionalDirectories) > 0 {
		return invalidParamsError("additionalDirectories is not yet supported")
	}
	if requireSessionID && p.SessionID == "" {
		return invalidParamsError("sessionId is required")
	}
	return nil
}

// Create handles session/new. It calls StartRuntime with no session-id
// option so the runtime allocates a fresh id, then publishes the pointer
// under that id. If a pointer is already registered for the generated id
// (an unlikely race in tests), the existing runtime is cancelled and
// closed before the new one is published.
func (m *SessionManager) Create(ctx context.Context, params json.RawMessage) (any, error) {
	if m.start == nil {
		return nil, fmt.Errorf("acp: SessionManager has no StartRuntime configured")
	}
	var p sessionParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/new params: %w", err)
		}
	}
	if err := validateLifecycleParams(&p, false); err != nil {
		return nil, err
	}

	opts := append([]app.Option{}, m.options...)
	opts = append(opts, app.WithWorkingDir(p.Cwd))
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	m.publishReplacement(rt.SessionID, rt)
	return SessionResponse{SessionID: rt.SessionID}, nil
}

// Load handles session/load. It validates the params, cancels and closes
// any pre-existing runtime for the same id, starts a new runtime with
// WithExistingSession, publishes it, and synchronously replays the active
// branch of the transcript via session/update notifications. Returns nil
// on success — ACP v1 expects a null result.
func (m *SessionManager) Load(ctx context.Context, params json.RawMessage) (any, error) {
	if m.start == nil {
		return nil, fmt.Errorf("acp: SessionManager has no StartRuntime configured")
	}
	if m.notify == nil {
		return nil, serverErrorf("SessionManager has no Notify configured")
	}
	cancel, err := m.requireReady()
	if err != nil {
		return nil, err
	}
	var p sessionParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/load params: %w", err)
		}
	}
	if err := validateLifecycleParams(&p, true); err != nil {
		return nil, err
	}

	old, loadErr := m.replaceExisting(ctx, p.SessionID, cancel)
	if loadErr != nil {
		return nil, loadErr
	}
	_ = old // already torn down

	opts := append([]app.Option{}, m.options...)
	opts = append(opts, app.WithWorkingDir(p.Cwd), app.WithExistingSession(p.SessionID))
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	m.publishReplacement(rt.SessionID, rt)
	if replayErr := m.replay(ctx, rt); replayErr != nil {
		return nil, replayErr
	}
	return nil, nil
}

// Close tears down the runtime for id. The runtime is removed from the
// map before any canceller or closer is invoked, so concurrent Get calls
// return false during teardown.
func (m *SessionManager) Close(ctx context.Context, id string) error {
	cancel, err := m.requireReady()
	if err != nil {
		return err
	}
	rt, ok := m.detach(id)
	if !ok {
		return serverErrorf("unknown session: %s", id)
	}
	err1 := cancel(ctx, id)
	err2 := m.close(ctx, rt)
	return errors.Join(err1, err2)
}

// CloseSession is the JSON-RPC handler for session/close. It parses the
// params, calls Close, and returns an empty object on success.
func (m *SessionManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/close params: %w", err)
		}
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	if err := m.Close(ctx, p.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// CloseAll tears down every runtime. It swaps the map for a fresh empty
// map, sorts the captured ids for deterministic logging, and then
// attempts cancel + close for each id using the caller context. The
// returned error is the joined result of every per-id cancel and close.
func (m *SessionManager) CloseAll(ctx context.Context) error {
	cancel, err := m.requireReady()
	if err != nil {
		return err
	}
	m.lifecycleMu.Lock()
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	old := make(map[string]*app.Runtime, len(m.sessions))
	for id, rt := range m.sessions {
		ids = append(ids, id)
		old[id] = rt
	}
	m.sessions = map[string]*app.Runtime{}
	m.mu.Unlock()
	m.lifecycleMu.Unlock()

	sort.Strings(ids)
	var errs []error
	for _, id := range ids {
		rt := old[id]
		if err := cancel(ctx, id); err != nil {
			errs = append(errs, err)
		}
		if err := m.close(ctx, rt); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Get returns the runtime registered for an ACP session id.
func (m *SessionManager) Get(id string) (*app.Runtime, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.sessions[id]
	return rt, ok
}

// detach removes the runtime for id from the map under both locks and
// returns the removed pointer (or false if id was unknown).
func (m *SessionManager) detach(id string) (*app.Runtime, bool) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	delete(m.sessions, id)
	return rt, true
}

// replaceExisting detaches the runtime for id, cancels its in-flight
// turn, and closes it. If id has no prior runtime, both calls are
// skipped and the returned error is nil. Any error from cancel or close
// is returned; the manager does not start a replacement.
func (m *SessionManager) replaceExisting(ctx context.Context, id string, cancel TurnCanceller) (*app.Runtime, error) {
	old, ok := m.detach(id)
	if !ok {
		return nil, nil
	}
	var errs []error
	if err := cancel(ctx, id); err != nil {
		errs = append(errs, err)
	}
	if err := m.close(ctx, old); err != nil {
		errs = append(errs, err)
	}
	return old, errors.Join(errs...)
}

// publishReplacement installs rt under id, cancelling and closing any
// prior pointer with the same id. The replacement is then visible to
// Get and any subsequent lifecycle operation.
func (m *SessionManager) publishReplacement(id string, rt *app.Runtime) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	prior, had := m.sessions[id]
	m.sessions[id] = rt
	m.mu.Unlock()
	if had && prior != nil && prior != rt {
		if m.cancel != nil {
			_ = m.cancel(context.Background(), id)
		}
		_ = m.close(context.Background(), prior)
	}
}

// replay synchronously projects rt.State.Messages() through messageUpdate
// and emits each one as a session/update notification. The runtime must
// already be installed in the map before this is called (so that any
// concurrent teardown via Close finds it). On any notification error
// the runtime is removed and closed and the joined error is returned.
func (m *SessionManager) replay(ctx context.Context, rt *app.Runtime) error {
	if rt == nil || rt.State == nil {
		return nil
	}
	for _, msg := range rt.State.Messages() {
		update := messageUpdate(msg)
		if err := m.notify("session/update", SessionUpdateParams{
			SessionID: rt.SessionID,
			Update:    update,
		}); err != nil {
			teardownErr := m.teardownIfStillCurrent(rt)
			return errors.Join(err, teardownErr)
		}
	}
	return nil
}

// teardownIfStillCurrent removes rt from the map only if the current
// pointer at rt.SessionID is still rt, then closes it. Used by replay
// on notification failure to avoid racing with a concurrent close.
func (m *SessionManager) teardownIfStillCurrent(rt *app.Runtime) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	cur, ok := m.sessions[rt.SessionID]
	if ok && cur == rt {
		delete(m.sessions, rt.SessionID)
		m.mu.Unlock()
		return m.close(context.Background(), rt)
	}
	m.mu.Unlock()
	return nil
}
