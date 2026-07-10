package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"marshal/internal/app"
)

// RuntimeStarter abstracts app.StartRuntime so SessionManager can be tested
// without booting the full headless runtime.
type RuntimeStarter func(context.Context, ...app.Option) (*app.Runtime, error)

// SessionManagerConfig configures a SessionManager.
type SessionManagerConfig struct {
	// StartRuntime is called to build a new *app.Runtime for a session.
	// Required. Tests inject a fake; production wires app.StartRuntime.
	StartRuntime RuntimeStarter
	// Options appended to every StartRuntime call. Useful for headless
	// defaults like WithSkipOnboarding.
	Options []app.Option
}

// SessionManager owns the map from ACP session IDs to *app.Runtime. The map
// is concurrency-safe: SessionManager may receive session/new and
// session/load requests on the JSON-RPC server's goroutine while
// session/prompt turns run on the agent's goroutine.
//
// F21 v1 keeps turns serial per session; the lock guards the map, not turn
// execution. The single-pending approval slot documented as a known risk
// applies to prompt turn handlers added in later tasks, not here.
type SessionManager struct {
	start   RuntimeStarter
	options []app.Option

	mu       sync.Mutex
	sessions map[string]*app.Runtime
}

// SessionResponse is the successful result shape for session/new and
// session/load. ACP spec v1 uses sessionId for session/new; session/load
// returns a null result once replay completes, so this type is reused for
// the create path and the load path encodes its body separately.
type SessionResponse struct {
	SessionID string `json:"sessionId"`
}

// sessionParams is the subset of ACP session/new and session/load params
// we honor. MCP server list and additional directories are parsed but not
// yet wired to runtime; see later tasks.
type sessionParams struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
}

// NewSessionManager constructs a SessionManager.
func NewSessionManager(cfg SessionManagerConfig) *SessionManager {
	return &SessionManager{
		start:    cfg.StartRuntime,
		options:  cfg.Options,
		sessions: map[string]*app.Runtime{},
	}
}

// Create handles session/new. It calls StartRuntime with no session-id
// option so the runtime allocates a fresh sess_<unixnano> id, then stores
// the resulting runtime under that id.
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
	opts := append([]app.Option{}, m.options...)
	if p.Cwd != "" {
		opts = append(opts, app.WithWorkingDir(p.Cwd))
	}
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	m.put(rt.SessionID, rt)
	return SessionResponse{SessionID: rt.SessionID}, nil
}

// Load handles session/load. It pins the session id supplied by the client
// via WithSessionID so the runtime reuses it instead of generating a new
// one. The ACP v1 session/load response is a null result once history
// replay finishes, but that replay is the job of session/prompt work in
// later tasks; this handler only allocates the runtime and stores it.
func (m *SessionManager) Load(ctx context.Context, params json.RawMessage) (any, error) {
	if m.start == nil {
		return nil, fmt.Errorf("acp: SessionManager has no StartRuntime configured")
	}
	var p sessionParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/load params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/load requires sessionId")
	}
	opts := append([]app.Option{}, m.options...)
	if p.Cwd != "" {
		opts = append(opts, app.WithWorkingDir(p.Cwd))
	}
	opts = append(opts, app.WithSessionID(p.SessionID))
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	m.put(rt.SessionID, rt)
	return SessionResponse{SessionID: rt.SessionID}, nil
}

// Get returns the runtime registered for an ACP session id. The boolean is
// false if the id is unknown.
func (m *SessionManager) Get(id string) (*app.Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.sessions[id]
	return rt, ok
}

func (m *SessionManager) put(id string, rt *app.Runtime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[id] = rt
}
