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

	"marshal/internal/app"
	"marshal/internal/app/session"
	"marshal/internal/db"
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
}

// SessionResponse is the successful result shape for session/new.
// session/load returns a null result once replay completes.
type SessionResponse struct {
	SessionID string         `json:"sessionId"`
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
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
		start:    cfg.StartRuntime,
		close:    closeFn,
		notify:   cfg.Notify,
		lister:   cfg.Lister,
		options:  cfg.Options,
		sessions: map[string]*app.Runtime{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
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
	// De-duplicate additionalDirectories by resolved path.
	seen := map[string]bool{}
	for _, raw := range p.AdditionalDirectories {
		resolved, err := filepath.EvalSymlinks(raw)
		if err != nil {
			return invalidParamsError("additionalDirectory %q: %v", raw, err)
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
func (m *SessionManager) Create(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := m.decodeLifecycleParams(params, "session/new", false)
	if err != nil {
		return nil, err
	}
	rt, err := m.startRuntime(ctx, p)
	if err != nil {
		return nil, err
	}
	m.publishReplacement(ctx, rt.SessionID, rt)
	resp := SessionResponse{SessionID: rt.SessionID}
	if p.Isolation != nil {
		ws, ierr := isolateSession(worktree.CLIGitOps{}, rt.State, p.Cwd, *p.Isolation, p.Name)
		if ierr != nil {
			// The session exists and is usable at the project root; report
			// the isolation failure rather than discarding it.
			return nil, serverErrorf("session created but isolation failed: %v", ierr)
		}
		resp.Workspace = &ws
	}
	return resp, nil
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
	err2 := m.close(sCtx, rt)
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

// listParams is the parameter shape for session/list.
type listParams struct {
	Cwd    string `json:"cwd"`
	Cursor string `json:"cursor,omitempty"`
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

	entries, next, err := m.lister.ListSessions(ctx, p.Cwd, p.Cursor, 0)
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
		if err := m.close(sCtx, rt); err != nil {
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
func (m *SessionManager) replaceExisting(ctx context.Context, id string, cancel TurnCanceller) error {
	old, ok := m.detach(id)
	if !ok {
		return nil
	}
	var errs []error
	if err := cancel(ctx, id); err != nil {
		errs = append(errs, err)
	}
	if err := m.close(ctx, old); err != nil {
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
	_ = m.close(sCtx, prior)
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
		return m.close(sCtx, rt)
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
		res = p
	}
	for _, root := range trusted {
		rel, err := filepath.Rel(root, res)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%q is outside the trusted-roots allow-list", p)
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
