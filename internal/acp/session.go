package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/worktree"
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

// SessionLister exposes per-cwd session discovery for the session/list
// and session/delete handlers. The production implementation opens the
// matching per-cwd database; tests inject a fake.
type SessionLister interface {
	ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error)
	DeleteSession(ctx context.Context, cwd, sessionID string) (existed bool, err error)
	Close() error
}

// SessionManagerOption configures a SessionManager.
type SessionManagerOption func(*SessionManager)

// WithSessionManagerLogger sets the logger on a SessionManager. When nil
// (or unset), the manager uses slog.Default().
func WithSessionManagerLogger(l *slog.Logger) SessionManagerOption {
	return func(m *SessionManager) { m.Logger = l }
}

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
	// headless defaults like WithTrustResolver.
	Options []app.Option
	// Lister provides session discovery for session/list. When nil,
	// List returns a server error.
	Lister SessionLister
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
	Logger *slog.Logger // nil → slog.Default()

	start   RuntimeStarter
	close   RuntimeCloser
	notify  NotifyFunc
	options []app.Option

	cancel TurnCanceller
	lister SessionLister

	mu          sync.RWMutex
	sessions    map[string]*app.Runtime
	lifecycleMu sync.Mutex

	// closeOnce guards each runtime's closer so a given *app.Runtime
	// pointer is torn down at most once, regardless of which lifecycle
	// path (replaceExisting, publishReplacement, Close, CloseAll,
	// teardownIfStillCurrent) races to close it first. Keyed by runtime
	// pointer. A dedicated closeMu (never lifecycleMu) guards the map so
	// the guard can be consulted from paths that already hold lifecycleMu.
	//
	// Known limitation (documented at closeRuntimeOnce): entries are never
	// removed, so the map grows by one small *sync.Once per runtime ever
	// recycled over the lifetime of a long-running server. This is the
	// deliberate price of the at-most-once pointer guarantee; removing an
	// entry after the first close would let a later caller on a stale
	// pointer create a fresh guard and re-close the runtime.
	closeMu   sync.Mutex
	closeOnce map[*app.Runtime]*sync.Once

	// services holds each session's client-seam pair, keyed by the runtime
	// pointer it was built from: the host and the session service.New returns
	// together, because they are one view of that runtime. A session outlives
	// its connection, so its service does too: closeRuntimeOnce releases it
	// before it closes the runtime, and a runtime replaced on load or resume
	// gets a fresh one.
	// servicesMu is a lock of its own; the only order it takes part in is
	// servicesMu then closeMu.
	servicesMu sync.Mutex
	services   map[*app.Runtime]*servicePair
}

// servicePair is what service.New builds for one runtime: the host a
// session's settings calls act through, and the session itself.
type servicePair struct {
	host *service.Host
	sess *service.Session
}

// SessionResponse is the successful result shape for session/new.
// session/load returns a null result once replay completes.
type SessionResponse struct {
	SessionID string         `json:"sessionId"`
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
	// InteractionState is the full effective tuple committed for the new
	// session. Every field is always present; a client bootstraps from this
	// (or session/access_state) rather than inferring grants from defaults.
	InteractionState
}

// sessionParams is the subset of ACP session/new and session/load params
// we honor. mcpServers must be an explicit empty array; additional
// directories are accepted (max 8, must be absolute).
type sessionParams struct {
	Cwd                   string             `json:"cwd"`
	SessionID             string             `json:"sessionId"`
	MCPServers            *[]json.RawMessage `json:"mcpServers"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
	// Isolation, when present, puts the new session in a git worktree.
	// Honored by session/new only.
	Isolation *IsolationParams `json:"isolation,omitempty"`
	// Name is an optional display name used to derive a branch when
	// isolation.branch is omitted.
	Name string `json:"name,omitempty"`
	// Routing gives the new session its own profile and per-role preset
	// overrides. Honored by session/new only.
	Routing *RoutingParams `json:"routing,omitempty"`
	// Policy, when present, sets the new session's approval mode and
	// pre-approved commands. Honored by session/new only.
	Policy *PolicyParams `json:"policy,omitempty"`
}

// RoutingParams is the session/new routing parameter. Overrides maps role
// names to preset names.
type RoutingParams struct {
	Profile   string            `json:"profile,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
}

// NewSessionManager constructs a SessionManager.
func NewSessionManager(cfg SessionManagerConfig, opts ...SessionManagerOption) *SessionManager {
	closeFn := cfg.CloseRuntime
	if closeFn == nil {
		closeFn = func(ctx context.Context, rt *app.Runtime) error {
			if rt == nil {
				return nil
			}
			return rt.Close(ctx)
		}
	}
	m := &SessionManager{
		start:     cfg.StartRuntime,
		close:     closeFn,
		notify:    cfg.Notify,
		lister:    cfg.Lister,
		options:   cfg.Options,
		sessions:  map[string]*app.Runtime{},
		closeOnce: map[*app.Runtime]*sync.Once{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// closeRuntimeOnce runs m.close for rt at most once per runtime pointer,
// regardless of which lifecycle path races to tear rt down. Concurrent
// callers block on the sync.Once; the winning caller performs the close
// and everyone else returns its error.
//
// The close is the noninteractive end path, and that is load-bearing: every
// caller here is headless (an ACP session close, close-all, or a
// load/resume/delete replacement), so there is no user to ask. rt.Close runs
// its terminal closure through the runtime's own end controller — freeze
// ordinary admission, then the shared stop/join pass — and that controller
// consults no UI: it freezes exactly the work it found, so no approval prompt,
// warning panel or postmortem question can block a shutdown that has nobody
// attached to answer it.
func (m *SessionManager) closeRuntimeOnce(ctx context.Context, rt *app.Runtime) error {
	if rt == nil {
		return nil
	}
	m.closeMu.Lock()
	if m.closeOnce == nil {
		m.closeOnce = map[*app.Runtime]*sync.Once{}
	}
	once, ok := m.closeOnce[rt]
	if !ok {
		once = &sync.Once{}
		m.closeOnce[rt] = once
	}
	m.closeMu.Unlock()

	var closeErr error
	once.Do(func() {
		// Release the session's client-seam service before the runtime
		// closes: the service's event loop reads the state, so it must stop
		// first. A runtime whose service was never built releases nothing.
		m.releaseService(rt)
		closeErr = m.close(ctx, rt)
	})
	return closeErr
	// Known limitation: this map (SessionManager.closeOnce) never drops
	// entries, so it grows by one entry per distinct runtime pointer ever
	// passed here. It is bounded per recycle (a single *sync.Once per
	// runtime) and is the deliberate cost of guaranteeing that no stale
	// pointer can ever be torn down twice. The entry must NOT be removed
	// after the first Do completes: a later call on the same pointer would
	// allocate a fresh guard and re-close an already-closed runtime, which
	// is the exact double-teardown this guard exists to prevent. Reclaiming
	// the memory would require reworking teardown to be id-serialized
	// rather than pointer-keyed; out of scope for the transport-robustness
	// batch (see .docs-archive/superpowers/plans/2026-08-19-batch11-*).
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

// log returns the configured logger or slog.Default() when Logger is nil.
func (m *SessionManager) log() *slog.Logger {
	if m.Logger == nil {
		return slog.Default()
	}
	return m.Logger
}

// connectionShutdownTimeout is the maximum time allowed for cancelling or
// closing a runtime during shutdown operations. All shutdown paths are
// bounded at this duration.
const connectionShutdownTimeout = 5 * time.Second

// shutdownCtx returns a context that is cancelled after
// connectionShutdownTimeout. Used to bound all shutdown operations.
func shutdownCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), connectionShutdownTimeout)
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

// validateDeleteParams validates only the fields that matter for
// session/delete: cwd and sessionId. It enforces the trusted-roots boundary
// (F-SEC-33) without demanding irrelevant mcpServers/additionalDirectories
// constraints carried by the shared lifecycle validator.
func validateDeleteParams(p *sessionParams) error {
	if strings.TrimSpace(p.Cwd) == "" {
		return invalidParamsError("cwd is required")
	}
	if !filepath.IsAbs(p.Cwd) {
		return invalidParamsError("cwd must be an absolute path")
	}
	if err := validateWorkingPaths(p.Cwd, nil); err != nil {
		return invalidParamsError("%v", err)
	}
	if p.SessionID == "" {
		return invalidParamsError("sessionId is required")
	}
	return nil
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
	// Validate cwd and additionalDirectories against the trusted-roots
	// allow-list (F-SEC-33).
	if err := validateWorkingPaths(p.Cwd, p.AdditionalDirectories); err != nil {
		return invalidParamsError("%v", err)
	}
	if p.MCPServers == nil {
		return invalidParamsError("mcpServers is required and must be an explicit empty array")
	}
	if len(*p.MCPServers) > 0 {
		return invalidParamsError("mcpServers is not yet supported")
	}
	if len(p.AdditionalDirectories) > 8 {
		return invalidParamsError("additionalDirectories max 8 entries")
	}
	// De-duplicate additionalDirectories by resolved path. Non-existent
	// paths (which may be created later by the agent) resolve to their
	// nearest existing ancestor plus the remaining components, so the
	// dedup key is stable and never errors out (ACP-MOD-#12).
	seen := map[string]bool{}
	for _, raw := range p.AdditionalDirectories {
		resolved := raw
		if _, err := os.Lstat(raw); err == nil {
			resolved, err = filepath.EvalSymlinks(raw)
			if err != nil {
				return invalidParamsError("additionalDirectory %q: %v", raw, err)
			}
		} else {
			resolved = resolveNearestAncestor(raw)
		}
		if seen[resolved] {
			return invalidParamsError("additionalDirectory %q duplicates %q after symlink resolution", raw, resolved)
		}
		seen[resolved] = true
	}
	if requireSessionID && p.SessionID == "" {
		return invalidParamsError("sessionId is required")
	}
	return nil
}

// decodeParams unmarshals a JSON-RPC params blob into v, tolerating an
// empty blob. method is used in the error message.
func decodeParams(params json.RawMessage, v any, method string) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("acp: parse %s params: %w", method, err)
	}
	return nil
}

// decodeLifecycleParams decodes and validates session lifecycle params.
func (m *SessionManager) decodeLifecycleParams(params json.RawMessage, method string, requireID bool) (sessionParams, error) {
	var p sessionParams
	if err := decodeParams(params, &p, method); err != nil {
		return p, err
	}
	if err := validateLifecycleParams(&p, requireID); err != nil {
		return p, err
	}
	return p, nil
}

// startRuntime starts a runtime bound to the params' cwd, applying extra
// options before the additional-directories option.
func (m *SessionManager) startRuntime(ctx context.Context, p sessionParams, extra ...app.Option) (*app.Runtime, error) {
	if m.start == nil {
		return nil, fmt.Errorf("acp: SessionManager has no StartRuntime configured")
	}
	opts := append([]app.Option{}, m.options...)
	opts = append(opts, app.WithWorkingDir(p.Cwd))
	opts = append(opts, extra...)
	if len(p.AdditionalDirectories) > 0 {
		opts = append(opts, app.WithAdditionalDirectories(p.AdditionalDirectories))
	}
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	return rt, nil
}

// Create handles session/new. It calls StartRuntime with no session-id
// option so the runtime allocates a fresh id, then publishes the pointer
// under that id. If a pointer is already registered for the generated id
// (an unlikely race in tests), the existing runtime is cancelled and
// closed before the new one is published.
//
// A params blob carrying replaces names a session to swap inside its own
// runtime instead (P3 R7, plan 6c decision 5): the runtime builds the new
// session as the in-process host does and stays registered under the new id.
func (m *SessionManager) Create(ctx context.Context, params json.RawMessage) (any, error) {
	// A decode failure falls through to the lifecycle decoding below, which
	// reports it as it reports every other bad session/new blob.
	var replacing acpwire.NewSessionParams
	if err := json.Unmarshal(params, &replacing); err == nil && replacing.Replaces != "" {
		return m.replace(ctx, replacing.Replaces, replacing.Name)
	}
	p, err := m.decodeLifecycleParams(params, "session/new", false)
	if err != nil {
		return nil, err
	}
	var policyPatch interaction.Patch
	if p.Policy != nil {
		if policyPatch, err = p.Policy.validate(); err != nil {
			return nil, err
		}
	}
	var extra []app.Option
	if p.Routing != nil && (p.Routing.Profile != "" || len(p.Routing.Overrides) > 0) {
		overrides := make(map[routing.AgentRole]string, len(p.Routing.Overrides))
		for role, preset := range p.Routing.Overrides {
			overrides[routing.AgentRole(role)] = preset
		}
		extra = append(extra, app.WithSessionRouting(p.Routing.Profile, overrides))
	}
	rt, err := m.startRuntime(ctx, p, extra...)
	if err != nil {
		if errors.Is(err, app.ErrInvalidSessionRouting) {
			return nil, invalidParamsError("%v", err)
		}
		return nil, err
	}
	if p.Policy != nil {
		if perr := applyPolicy(rt, p.Policy, policyPatch); perr != nil {
			sCtx, sCancel := shutdownCtx()
			defer sCancel()
			_ = m.closeRuntimeOnce(sCtx, rt)
			return nil, perr
		}
	}
	resp := SessionResponse{SessionID: rt.SessionID, InteractionState: sessionInteractionState(rt)}
	if p.Isolation != nil {
		// Worktree setup is resolved per session from the session's loaded
		// config: the ACP host never holds a config.Config (config is per-cwd,
		// loaded inside app.StartRuntime and carried on session.State), so
		// this call site — not the host wiring — is where Setup comes from.
		// A nil State (possible in embedded/test runtimes) falls back to a
		// zero config; isolation itself still fails cleanly on a non-git cwd.
		var setup config.WorktreeConfig
		if rt.State != nil {
			setup = rt.State.Config.Worktree
		}
		ws, ierr := isolateSession(worktree.CLIGitOps{}, rt.State, p.Cwd, *p.Isolation, p.Name, setup)
		if ierr != nil {
			// The runtime was started but isolation failed. Tear it down
			// rather than publishing a live session at the project root with
			// no usable id to clean it up.
			sCtx, sCancel := shutdownCtx()
			defer sCancel()
			_ = m.closeRuntimeOnce(sCtx, rt)
			return nil, serverErrorf("session created but isolation failed: %v", ierr)
		}
		resp.Workspace = &ws
	}
	m.publishReplacement(ctx, rt.SessionID, rt)
	return resp, nil
}

// replace serves session/new with replaces: the replaced session's service
// host builds the new session inside the same runtime, and the manager then
// files the runtime under the new session's ID (P3 R7, plan 6c decision 5).
//
// The replacement holds lifecycleMu for its whole length, so a close or a load
// of the same session cannot interleave with it. It builds a runner and can
// take seconds, and the other lifecycle calls wait for it; embedded mode has
// one session, so nothing else waits on a key of its own.
func (m *SessionManager) replace(ctx context.Context, replaces, name string) (any, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	rt, ok := m.Get(replaces)
	if !ok || rt == nil {
		return nil, serverErrorf("unknown session: %s", replaces)
	}
	host, _, ok := m.servicePair(replaces)
	if !ok || host == nil {
		// A runtime with no state, or one that is already closing, refuses as
		// Service does.
		return nil, serverErrorf("unknown session: %s", replaces)
	}
	swapped, err := host.NewSession(ctx, name)
	if err != nil {
		// Nothing changed: the runtime's old session stays registered and
		// current, as in process.
		return nil, err
	}
	next, ok := swapped.(*service.Session)
	if !ok {
		return nil, serverErrorf("acp: session/new with replaces answered %T, want *service.Session", swapped)
	}
	newID := next.ID()

	m.mu.Lock()
	delete(m.sessions, replaces)
	m.sessions[newID] = rt
	m.mu.Unlock()

	// The host's swap already closed the replaced session, so the pair points
	// at the new one: Service(newID) answers it, and the replaced one is never
	// served again.
	m.servicesMu.Lock()
	if pair, ok := m.services[rt]; ok {
		pair.sess = next
	}
	m.servicesMu.Unlock()

	m.log().Debug("session replaced", "replaced_session_id", replaces, "session_id", newID)
	return SessionResponse{SessionID: newID, InteractionState: sessionInteractionState(rt)}, nil
}

// Load handles session/load. It validates the params, cancels and closes
// any pre-existing runtime for the same id, starts a new runtime with
// WithExistingSession, publishes it, and synchronously replays the active
// branch of the transcript via session/update notifications. Returns nil
// on success — ACP v1 expects a null result.
func (m *SessionManager) Load(ctx context.Context, params json.RawMessage) (any, error) {
	if m.notify == nil {
		return nil, serverErrorf("SessionManager has no Notify configured")
	}
	cancel, err := m.requireReady()
	if err != nil {
		return nil, err
	}
	p, err := m.decodeLifecycleParams(params, "session/load", true)
	if err != nil {
		return nil, err
	}
	if err := m.replaceExisting(ctx, p.SessionID, cancel); err != nil {
		return nil, err
	}
	rt, err := m.startRuntime(ctx, p, app.WithExistingSession(p.SessionID))
	if err != nil {
		return nil, err
	}
	m.publishReplacement(ctx, rt.SessionID, rt)
	if err := m.replay(ctx, rt); err != nil {
		return nil, err
	}
	return nil, nil
}

// Resume handles session/resume. It restores an existing persisted session
// like Load but, per ACP v1, MUST NOT replay conversation history. It
// validates params, cancels and closes any pre-existing runtime for the
// same id, starts a new runtime with WithExistingSession, publishes it,
// and returns an empty result object.
func (m *SessionManager) Resume(ctx context.Context, params json.RawMessage) (any, error) {
	cancel, err := m.requireReady()
	if err != nil {
		return nil, err
	}
	p, err := m.decodeLifecycleParams(params, "session/resume", true)
	if err != nil {
		return nil, err
	}
	if err := m.replaceExisting(ctx, p.SessionID, cancel); err != nil {
		return nil, err
	}
	rt, err := m.startRuntime(ctx, p, app.WithExistingSession(p.SessionID))
	if err != nil {
		return nil, err
	}
	m.publishReplacement(ctx, rt.SessionID, rt)
	return map[string]any{}, nil
}

// Close tears down the runtime for id. The runtime is removed from the
// map before any canceller or closer is invoked, so concurrent Get calls
// return false during teardown.
//
// The turn canceller is the explicit one (CancelAndWait): it stops the
// session's still-attached commands before cancelling the agent, then waits for
// the turn to settle within the canceller's own bound. Cancel and close errors
// are joined rather than short-circuited, so a failed stop still gets its
// runtime closed and the caller sees both.
func (m *SessionManager) Close(ctx context.Context, id string) error {
	turnCancel, err := m.requireReady()
	if err != nil {
		return err
	}
	rt, ok := m.detach(id)
	if !ok {
		m.mu.RLock()
		n := len(m.sessions)
		m.mu.RUnlock()
		m.log().Debug("session close (no-op)", "session_id", id, "count", n)
		return serverErrorf("unknown session: %s", id)
	}
	m.mu.RLock()
	n := len(m.sessions)
	m.mu.RUnlock()
	m.log().Debug("session close", "session_id", id, "count", n)
	sCtx, sCancel := shutdownCtx()
	defer sCancel()
	err1 := turnCancel(sCtx, id)
	err2 := m.closeRuntimeOnce(sCtx, rt)
	return errors.Join(err1, err2)
}

// CloseSession is the JSON-RPC handler for session/close. It parses the
// params, calls Close, and returns an empty object on success.
func (m *SessionManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(params, &p, "session/close"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	if err := m.Close(ctx, p.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (m *SessionManager) Delete(ctx context.Context, params json.RawMessage) (any, error) {
	if m.lister == nil {
		return nil, serverErrorf("acp: SessionManager has no SessionLister configured")
	}
	cancel, err := m.requireReady()
	if err != nil {
		return nil, err
	}
	var p sessionParams
	if err := decodeParams(params, &p, "session/delete"); err != nil {
		return nil, err
	}
	if err := validateDeleteParams(&p); err != nil {
		return nil, err
	}
	if replaceErr := m.replaceExisting(ctx, p.SessionID, cancel); replaceErr != nil {
		return nil, replaceErr
	}
	existed, err := m.lister.DeleteSession(ctx, p.Cwd, p.SessionID)
	if err != nil {
		return nil, serverErrorf("acp: delete session: %v", err)
	}
	if !existed {
		return nil, serverErrorf("acp: no such session %q in %q", p.SessionID, p.Cwd)
	}
	return map[string]any{}, nil
}

// defaultSessionListLimit is the page size used when the client does not
// provide a positive limit for session/list.
const defaultSessionListLimit = 50

// listParams is the parameter shape for session/list.
type listParams struct {
	Cwd    string `json:"cwd"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// List handles session/list. It validates cwd (required, absolute),
// queries the injected SessionLister, and projects the result into the
// ACP SessionInfo[] shape with cursor pagination metadata.
func (m *SessionManager) List(ctx context.Context, params json.RawMessage) (any, error) {
	if m.lister == nil {
		return nil, serverErrorf("acp: SessionManager has no SessionLister configured")
	}
	var p listParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/list params: %v", err)
		}
	}
	if strings.TrimSpace(p.Cwd) == "" {
		return nil, invalidParamsError("cwd is required for session/list")
	}
	if !filepath.IsAbs(p.Cwd) {
		return nil, invalidParamsError("cwd must be an absolute path")
	}
	// Enforce the same trusted-roots boundary as every other lifecycle method
	// (F-SEC-33).
	if err := validateWorkingPaths(p.Cwd, nil); err != nil {
		return nil, invalidParamsError("%v", err)
	}

	limit := p.Limit
	if limit <= 0 {
		limit = defaultSessionListLimit
	}

	entries, next, err := m.lister.ListSessions(ctx, p.Cwd, p.Cursor, limit)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{
			"sessionId": e.SessionID,
			"cwd":       e.Cwd,
			"_meta":     map[string]any{"messageCount": float64(e.MessageCount)},
		}
		if e.Title != "" {
			item["title"] = e.Title
		}
		if !e.UpdatedAt.IsZero() {
			item["updatedAt"] = e.UpdatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	result := map[string]any{"sessions": items}
	if next != "" {
		result["nextCursor"] = next
	}
	return result, nil
}

// CloseAll tears down every runtime. It swaps the map for a fresh empty
// map, sorts the captured ids for deterministic logging, and then
// attempts cancel + close for each id using the caller context. The
// returned error is the joined result of every per-id cancel and close.
//
// Each per-id step is bounded by its own shutdown context, so one slow session
// cannot consume the whole shutdown budget, and a failing session is joined
// rather than aborting the rest — a headless shutdown must close every runtime
// it owns even when one of them refuses to settle.
func (m *SessionManager) CloseAll(ctx context.Context) error {
	turnCancel, err := m.requireReady()
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
		sCtx, sCancel := shutdownCtx()
		if err := turnCancel(sCtx, id); err != nil {
			errs = append(errs, err)
		}
		if err := m.closeRuntimeOnce(sCtx, rt); err != nil {
			errs = append(errs, err)
		}
		sCancel()
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

// Service returns the client-seam service of the session's current
// runtime, building it with the runtime's ServiceDeps on first use. It
// reports false for an unknown session, a runtime with no state, and a
// runtime that is closing.
func (m *SessionManager) Service(id string) (*service.Session, bool) {
	_, sess, ok := m.servicePair(id)
	return sess, ok
}

// ServiceHost returns the service host built with the session's service: the
// host its settings calls act through. It builds both on first use, as
// Service does.
func (m *SessionManager) ServiceHost(id string) (*service.Host, bool) {
	host, _, ok := m.servicePair(id)
	return host, ok
}

// adopt registers rt under rt.SessionID with the pair its embedding built, so
// the pair is never built from rt.ServiceDeps. It refuses an empty ID, an ID
// already registered, and a runtime that already has a pair: an adopted pair
// is never overwritten, because the pair it would replace is unreachable by
// then and would leak its service.
func (m *SessionManager) adopt(rt *app.Runtime, host *service.Host, sess *service.Session) error {
	if rt == nil || rt.SessionID == "" {
		return errors.New("acp: adopt requires a runtime with a session ID")
	}
	if host == nil || sess == nil {
		return fmt.Errorf("acp: adopt %s requires the service pair the embedding built", rt.SessionID)
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	// Install the pair before the session becomes visible. Registering the ID
	// first would let a concurrent Service(id) find the session with no pair
	// and build one from rt.ServiceDeps, which is exactly what adopt exists to
	// prevent (P3 plan 8 decision 5).
	m.servicesMu.Lock()
	if m.services == nil {
		m.services = map[*app.Runtime]*servicePair{}
	}
	_, paired := m.services[rt]
	if !paired {
		m.services[rt] = &servicePair{host: host, sess: sess}
	}
	m.servicesMu.Unlock()
	if paired {
		return fmt.Errorf("acp: adopt %s: the runtime already has a service pair", rt.SessionID)
	}

	m.mu.Lock()
	_, taken := m.sessions[rt.SessionID]
	if !taken {
		m.sessions[rt.SessionID] = rt
	}
	m.mu.Unlock()
	if taken {
		// The pair is the caller's and no reader ever saw it, so drop it and
		// release its session: a refused adopt must not leave a service
		// running that nothing can reach.
		m.releaseService(rt)
		return fmt.Errorf("acp: session %s is already registered", rt.SessionID)
	}
	return nil
}

// servicePair returns the pair built for the session's current runtime,
// building it on first use under Service's rules: an unknown session, a
// runtime with no state and a runtime that is closing report false.
func (m *SessionManager) servicePair(id string) (*service.Host, *service.Session, bool) {
	rt, ok := m.Get(id)
	if !ok || rt == nil || rt.State == nil {
		return nil, nil, false
	}
	m.servicesMu.Lock()
	defer m.servicesMu.Unlock()
	if m.services == nil {
		m.services = map[*app.Runtime]*servicePair{}
	}
	if pair, ok := m.services[rt]; ok {
		return pair.host, pair.sess, true
	}
	// Refuse a runtime that is already closing. closeRuntimeOnce registers its
	// guard under closeMu before the close runs, and its release takes
	// servicesMu, so a build that races a close is either refused here or
	// released there.
	m.closeMu.Lock()
	_, closing := m.closeOnce[rt]
	m.closeMu.Unlock()
	if closing {
		return nil, nil, false
	}
	host, sess := service.New(rt.ServiceDeps(context.Background()))
	m.services[rt] = &servicePair{host: host, sess: sess}
	return host, sess, true
}

// releaseService removes rt's pair from the registry and releases its
// session. A runtime whose service was never built releases nothing.
func (m *SessionManager) releaseService(rt *app.Runtime) {
	m.servicesMu.Lock()
	pair := m.services[rt]
	delete(m.services, rt)
	m.servicesMu.Unlock()
	if pair != nil && pair.sess != nil {
		pair.sess.Release()
	}
}

// All returns every published runtime. Used by worktree orphan detection,
// which must know every session's active root.
func (m *SessionManager) All() []*app.Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*app.Runtime, 0, len(m.sessions))
	for _, rt := range m.sessions {
		out = append(out, rt)
	}
	return out
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
//
// This is an explicit replacement, so the cancel is the explicit one: the
// old conversation's still-attached commands are stopped, while commands that
// already detached stay conversation-owned and are stopped by the runtime's own
// terminal closure. Nothing is transferred to the replacement.
func (m *SessionManager) replaceExisting(ctx context.Context, id string, cancel TurnCanceller) error {
	old, ok := m.detach(id)
	if !ok {
		return nil
	}
	var errs []error
	if err := cancel(ctx, id); err != nil {
		errs = append(errs, err)
	}
	if err := m.closeRuntimeOnce(ctx, old); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// publishReplacement installs rt under id. When a prior pointer exists it is
// torn down (cancelled and closed) *outside* the sessions map lock, but the
// lock is held continuously across the read of the prior pointer and the write
// of the new pointer so that two concurrent calls cannot both observe the same
// prior and double-close it.
//
// The caller context is used for the teardown operations (F-POL-62). Lifecycle
// serialisation is provided by lifecycleMu.
func (m *SessionManager) publishReplacement(ctx context.Context, id string, rt *app.Runtime) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	prior, had := m.sessions[id]
	samePointer := prior == rt
	if !samePointer {
		m.sessions[id] = rt
	}
	m.mu.Unlock()

	if !had || samePointer || prior == nil {
		return
	}
	m.log().Info("session publishReplacement",
		"session_id", id, "prior_runtime", prior.SessionID, "new_runtime", rt.SessionID)
	if m.cancel != nil {
		_ = m.cancel(ctx, id)
	}
	sCtx, sCancel := context.WithTimeout(ctx, 5*time.Second)
	defer sCancel()
	_ = m.closeRuntimeOnce(sCtx, prior)
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
		// Skill bodies are model context, not user-facing text; replaying
		// them would dump the full skill file into the client's transcript.
		if msg.ContentType == session.ContentTypeSkillBody {
			continue
		}
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
		sCtx, sCancel := shutdownCtx()
		defer sCancel()
		return m.closeRuntimeOnce(sCtx, rt)
	}
	m.mu.Unlock()
	return nil
}

// validateWorkingPaths verifies that cwd and each additionalDirectory
// are within an allow-list of trusted roots (the user's home, the
// system temp, and the process's working directory). See F-SEC-33.
func validateWorkingPaths(cwd string, additional []string) error {
	trusted, err := trustedRoots()
	if err != nil {
		return fmt.Errorf("internal: trusted roots: %v", err)
	}
	if err := checkPath(cwd, trusted); err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	for _, p := range additional {
		if err := checkPath(p, trusted); err != nil {
			return fmt.Errorf("additionalDirectory: %w", err)
		}
	}
	return nil
}

// checkPath verifies that p is an absolute path within one of the
// trusted roots after resolving symlinks.
func checkPath(p string, trusted []string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q is not absolute", p)
	}
	res, err := filepath.EvalSymlinks(p)
	if err != nil {
		// p (or one of its ancestors) does not exist. Resolve the deepest
		// existing ancestor and append the remaining components so the
		// containment check runs against the fully resolved path. This keeps
		// non-existent paths inside a symlinked root (e.g. macOS /var ->
		// /private/var) accepted, matching checkPath's documented fallback.
		res = resolveNearestAncestor(p)
	}
	for _, root := range trusted {
		rel, err := filepath.Rel(root, res)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%q is outside the trusted-roots allow-list", p)
}

// resolveNearestAncestor walks up from p until it finds an existing path,
// resolves that ancestor through EvalSymlinks, and appends the remaining
// components. It returns the cleaned result. Used when p itself does not
// exist yet (e.g. an additional directory the agent will create later).
func resolveNearestAncestor(p string) string {
	cur := filepath.Clean(p)
	var rest []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			resolved, rerr := filepath.EvalSymlinks(cur)
			if rerr != nil {
				return filepath.Clean(p)
			}
			for i := len(rest) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, rest[i])
			}
			return resolved
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(p)
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// trustedRoots returns directories that are considered trusted for
// session lifecycle path validation: the user's home directory,
// the system temp directory, and the process working directory.
// Each path is resolved through EvalSymlinks so that comparisons
// against resolved session paths are consistent.
func trustedRoots() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	roots := []string{home}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		resolved, err := filepath.EvalSymlinks(tmp)
		if err != nil {
			return nil, err
		}
		roots = append(roots, resolved)
	} else {
		// Fall back to /tmp when TMPDIR is unset.
		if resolved, err := filepath.EvalSymlinks("/tmp"); err == nil {
			roots = append(roots, resolved)
		}
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		if resolved, err := filepath.EvalSymlinks(wd); err == nil {
			roots = append(roots, resolved)
		}
	}
	return roots, nil
}
