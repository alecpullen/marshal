package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// controlHandshakeTimeout bounds the lazy start (initialize plus
// session/new) so one stuck control agent cannot hang every request that
// needs it.
const controlHandshakeTimeout = 30 * time.Second

// controlRuntime is the Studio's own agent. Project agents each own one
// session; the control agent owns the sessions that are not tied to a
// running project agent: global library operations, config edits, and
// Studio-level watches. It starts lazily and is rebuilt when it dies.
type controlRuntime struct {
	mu    sync.Mutex
	child *Child
	// ready is true once initialize and the control session succeeded on
	// the current child generation. Cleared when the child restarts,
	// because a restarted agent has forgotten every session.
	ready     bool
	sessionID string
	// projectSessions caches one control-agent session per project root.
	projectSessions map[string]string
	// caps holds the capability names the agent advertised in initialize,
	// from both agentCapabilities and sessionCapabilities.
	caps map[string]bool
}

func newControlRuntime() *controlRuntime {
	return &controlRuntime{projectSessions: make(map[string]string), caps: make(map[string]bool)}
}

// controlWorkDir is the bridge-side directory the control session works
// in. It holds nothing but scratch state for the agent.
func (f *Fleet) controlWorkDir() string { return filepath.Join(f.stateDir, "control") }

// buildControlChild returns the control agent's Child. The control agent is
// a host process: it has no workspace, and a workspace is what makes a
// container. Tests replace it through newControl.
func (f *Fleet) buildControlChild() (*Child, error) {
	if f.newControl != nil {
		return f.newControl()
	}
	return &Child{MarshalBin: f.marshalBin}, nil
}

// control returns the control runtime, starting it on first use and
// again after it dies. Concurrent first callers share one start.
func (f *Fleet) control(ctx context.Context) (*controlRuntime, error) {
	c := f.ctl
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return c, nil
	}
	select {
	case <-f.done:
		return nil, fmt.Errorf("bridge: fleet is closing")
	default:
	}
	if c.child == nil {
		if err := os.MkdirAll(f.controlWorkDir(), 0o700); err != nil {
			return nil, fmt.Errorf("bridge: create control dir: %w", err)
		}
		f.ensureHomes()
		child, err := f.buildControlChild()
		if err != nil {
			return nil, err
		}
		// A restarted child has lost its sessions. Mark the runtime not
		// ready so the next call re-runs the handshake against the new
		// generation.
		child.OnRestart = func() {
			c.mu.Lock()
			c.ready = false
			c.projectSessions = make(map[string]string)
			c.mu.Unlock()
			// Every listing was read from this child's sessions, and a
			// restarted child has forgotten them (P7 D10). Failures are
			// dropped too: the reason a root failed may have been this child.
			f.dropListings()
		}
		child.OnNotification = f.onControlNotification
		if err := child.Start(); err != nil {
			return nil, fmt.Errorf("bridge: start control agent: %w (stderr: %s)", err, child.StderrLog())
		}
		c.child = child
	}

	hctx, cancel := context.WithTimeout(ctx, controlHandshakeTimeout)
	defer cancel()
	if err := f.controlHandshake(hctx, c); err != nil {
		// Drop the child: a handshake that fails on a live child means it
		// is unusable, and the next call should build a fresh one.
		child := c.child
		c.child = nil
		go child.Stop()
		return nil, err
	}
	c.ready = true
	return c, nil
}

// controlHandshake runs initialize and opens the control session.
func (f *Fleet) controlHandshake(ctx context.Context, c *controlRuntime) error {
	raw, err := c.child.Request(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		return fmt.Errorf("bridge: control initialize: %w", err)
	}
	var res struct {
		AgentCapabilities   map[string]json.RawMessage `json:"agentCapabilities"`
		SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities"`
	}
	_ = json.Unmarshal(raw, &res)
	c.caps = make(map[string]bool)
	for k := range res.AgentCapabilities {
		c.caps[k] = true
	}
	for k := range res.SessionCapabilities {
		c.caps[k] = true
	}
	cwd := f.controlSessionCwd(c)
	sid, err := newSessionOn(ctx, c.child, cwd)
	if err != nil {
		return fmt.Errorf("bridge: control session: %w", err)
	}
	c.sessionID = sid
	return nil
}

// controlSessionCwd is the control session's working directory: the
// bridge-side control directory, which a host-process control agent shares.
func (f *Fleet) controlSessionCwd(c *controlRuntime) string {
	return f.controlWorkDir()
}

// newSessionOn opens a session on child and returns its id.
func newSessionOn(ctx context.Context, child *Child, cwd string) (string, error) {
	raw, err := child.Request(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.SessionID == "" {
		return "", fmt.Errorf("decode session/new result: %v", err)
	}
	return out.SessionID, nil
}

// controlHas reports whether the control agent advertised a capability.
func (f *Fleet) controlHas(c *controlRuntime, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps[name]
}

// featureOf names the feature behind an ACP method: the part after the
// last "/", so "session/skills_list" is "skills_list".
func featureOf(method string) string {
	if i := strings.LastIndex(method, "/"); i >= 0 {
		return method[i+1:]
	}
	return method
}

// controlRequest sends one request to the control agent exactly as given.
// A method-not-found reply becomes ErrUnsupported{feature}.
func (f *Fleet) controlRequest(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	c, err := f.control(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	child := c.child
	c.mu.Unlock()
	if child == nil {
		return nil, fmt.Errorf("bridge: control agent is not running")
	}
	raw, err := child.Request(ctx, method, params)
	if isMethodNotFound(err) {
		return nil, ErrUnsupported{Feature: featureOf(method)}
	}
	return raw, err
}

// controlCall sends one request to the control agent. With session set it
// addresses the control session; otherwise the method takes no session
// (the config methods).
func (f *Fleet) controlCall(ctx context.Context, method string, params map[string]any, session bool) (json.RawMessage, error) {
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	if session {
		c, err := f.control(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		p["sessionId"] = c.sessionID
		c.mu.Unlock()
	}
	return f.controlRequest(ctx, method, p)
}

// controlSessionID returns the control session's id, starting the agent
// if needed.
func (f *Fleet) controlSessionID(ctx context.Context) (string, error) {
	c, err := f.control(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID, nil
}

// projectSession returns the control-agent session for a project root,
// opening and caching it on first use.
func (f *Fleet) projectSession(ctx context.Context, root string) (string, error) {
	c, err := f.control(ctx)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	c.mu.Lock()
	if sid, ok := c.projectSessions[root]; ok {
		c.mu.Unlock()
		return sid, nil
	}
	child := c.child
	c.mu.Unlock()
	if child == nil {
		return "", fmt.Errorf("bridge: control agent is not running")
	}
	sid, err := newSessionOn(ctx, child, root)
	if err != nil {
		if isMethodNotFound(err) {
			return "", ErrUnsupported{Feature: "project_library"}
		}
		return "", err
	}
	c.mu.Lock()
	// A concurrent caller may have opened one meanwhile; keep the first so
	// the cache never maps a root to two sessions.
	if existing, ok := c.projectSessions[root]; ok {
		sid = existing
	} else {
		c.projectSessions[root] = sid
	}
	c.mu.Unlock()
	return sid, nil
}

// controlChild is the control agent's child, started if needed.
//
// It is the live child itself, not a Registry over it: a caller that builds
// a Registry here would install its hooks on a child other code already
// owns, and a second call would replace them again.
func (f *Fleet) controlChild(ctx context.Context) (*Child, error) {
	c, err := f.control(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	child := c.child
	c.mu.Unlock()
	if child == nil {
		return nil, fmt.Errorf("bridge: control agent is not running")
	}
	return child, nil
}

// stopControl shuts the control agent down with the fleet. Unlike project
// agents it is not detached: it holds no work worth reattaching to.
func (f *Fleet) stopControl() {
	c := f.ctl
	c.mu.Lock()
	child := c.child
	c.child, c.ready = nil, false
	c.mu.Unlock()
	if child != nil {
		child.Stop()
	}
}
