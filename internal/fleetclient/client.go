// Package fleetclient reads one bridge's fleet and keeps it live from the
// fleet stream (P5 F3).
package fleetclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// readTimeout bounds every snapshot read, so a wedged bridge cannot hold the
// connect open.
const readTimeout = 10 * time.Second

// streamPath is the fleet stream's path and query.
const streamPath = "/api/events?stream=fleet"

// Transport is how the client reaches one bridge.
type Transport struct {
	Server  string       // the name the screen shows
	BaseURL string       // "http://daemon" on the socket
	HTTP    *http.Client // no overall timeout: the stream is long-lived
	// Token is the bearer token for one connect; nil sends none.
	Token func(ctx context.Context) (string, error)
	// Local marks the local daemon: GET /api/daemon is read.
	Local bool
}

// Socket is the local daemon's transport on the unix socket at path.
func Socket(path string) Transport {
	return Transport{
		Server:  "local",
		BaseURL: "http://daemon",
		// Every request goes to the socket regardless of the host in the URL:
		// the address is a name, not a lookup.
		HTTP: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
			},
		},
		Local: true,
	}
}

// Client is one bridge's fleet, read and kept live.
type Client struct {
	transport Transport

	now          func() time.Time
	backoffFirst time.Duration
	backoffMax   time.Duration
	resetAfter   time.Duration
	downAfter    time.Duration
	refetchEvery time.Duration
	daemonEvery  time.Duration

	changed chan struct{}
	done    chan struct{}

	mu          sync.Mutex
	state       acpclient.FleetState
	refetchedAt time.Time
	started     bool
	closed      bool
	cancel      context.CancelFunc
}

// Option configures a Client.
type Option func(*Client)

// WithBackoff sets the reconnect waits. It is a test seam; production uses the
// defaults.
func WithBackoff(first, max time.Duration) Option {
	return func(c *Client) {
		c.backoffFirst = first
		c.backoffMax = max
	}
}

// WithResetAfter sets how long a stream must stay up before the backoff resets.
// It is a test seam; production uses the default.
func WithResetAfter(d time.Duration) Option {
	return func(c *Client) { c.resetAfter = d }
}

// WithDownAfter sets how long attempts must fail before the state goes down.
// It is a test seam; production uses the default.
func WithDownAfter(d time.Duration) Option {
	return func(c *Client) { c.downAfter = d }
}

// WithRefetchEvery sets the agents refetch limit the inbox path obeys. It is a
// test seam; production uses the default.
func WithRefetchEvery(d time.Duration) Option {
	return func(c *Client) { c.refetchEvery = d }
}

// WithDaemonEvery sets how often the local daemon is reread. It is a test seam;
// production uses the default.
func WithDaemonEvery(d time.Duration) Option {
	return func(c *Client) { c.daemonEvery = d }
}

// WithNow replaces the clock the client stamps rows with. A nil clock is
// ignored.
func WithNow(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}

// New builds a client for t. It does not connect: Start does.
func New(t Transport, opts ...Option) *Client {
	c := &Client{
		transport:    t,
		now:          time.Now,
		backoffFirst: time.Second,
		backoffMax:   30 * time.Second,
		resetAfter:   time.Minute,
		downAfter:    time.Minute,
		refetchEvery: time.Second,
		daemonEvery:  5 * time.Second,
		changed:      make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.state = acpclient.FleetState{
		Server: t.Server,
		Conn:   acpclient.FleetConnecting,
		// Gates is non-nil from the start, so a caller can index it.
		Gates: make(map[string]contract.GateRecord),
	}
	return c
}

// Start connects in the background and keeps the stream until ctx ends or
// Close is called. It returns at once.
func (c *Client) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started || c.closed {
		c.mu.Unlock()
		return
	}
	c.started = true
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()

	go c.run(runCtx)
}

// Close ends the stream and the client's goroutine. Calling it twice is a
// no-op.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// State is the fleet as the client last read it. Every slice and map is the
// caller's own copy.
func (c *Client) State() acpclient.FleetState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyState(c.state)
}

// Changed fires after anything State returns may have changed. It coalesces: a
// signal already waiting is not queued twice, and a send never blocks.
func (c *Client) Changed() <-chan struct{} {
	return c.changed
}

// fire signals Changed without blocking. It is a coalescing signal, so a
// second change with a signal already waiting is dropped.
func (c *Client) fire() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// httpClient is the transport's HTTP client, or the default one.
func (c *Client) httpClient() *http.Client {
	if c.transport.HTTP != nil {
		return c.transport.HTTP
	}
	return http.DefaultClient
}

// run opens the stream, reads the fleet, applies what arrived while it read,
// and then keeps applying the stream's deltas until it ends or ctx is done.
func (c *Client) run(ctx context.Context) {
	defer close(c.done)

	resp, err := c.openStream(ctx)
	if err != nil {
		c.fail(ctx, err)
		return
	}
	defer resp.Body.Close()

	src := newFrameSource()
	go src.read(resp.Body)

	snap, err := c.readSnapshot(ctx)
	if err != nil {
		c.fail(ctx, err)
		return
	}
	c.install(snap)

	// The deltas that arrived while the reads were in flight are applied after
	// them, in arrival order: the reads are the snapshot, the deltas are what
	// happened since it was taken.
	frames, _, _ := src.take()
	for _, f := range frames {
		c.applyFrame(ctx, f.Data)
	}

	c.connected()
	c.consume(ctx, src)
}

// consume applies the stream's frames until it ends or ctx is done.
func (c *Client) consume(ctx context.Context, src *frameSource) {
	for {
		frames, done, err := src.take()
		for _, f := range frames {
			c.applyFrame(ctx, f.Data)
		}
		if done {
			if ctx.Err() == nil {
				if err == nil {
					err = fmt.Errorf("the fleet stream ended")
				}
				c.fail(ctx, err)
			}
			return
		}
		if err := src.wait(ctx); err != nil {
			return
		}
	}
}

// applyFrame applies one event payload and performs the refetch it asks for.
func (c *Client) applyFrame(ctx context.Context, payload []byte) {
	out := c.applyDelta(payload)
	if out.changed {
		c.fire()
	}
	switch out.refetch {
	case refetchEverything:
		c.refetchEverything(ctx)
	case refetchAgents:
		c.refetchAgents(ctx)
	case refetchBudgets:
		c.refetchBudgets(ctx)
	}
}

// openStream opens GET /api/events?stream=fleet. Only a 200 answer is a
// connected stream.
func (c *Client) openStream(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.transport.BaseURL+streamPath, nil)
	if err != nil {
		return nil, fmt.Errorf("fleet stream: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fleet stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		detail := responseDetail(resp)
		return nil, fmt.Errorf("fleet stream: %s: %s", resp.Status, detail)
	}
	return resp, nil
}

// authorize sets the bearer token, when the transport has one.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.transport.Token == nil {
		return nil
	}
	token, err := c.transport.Token(ctx)
	if err != nil {
		return fmt.Errorf("fleet token: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

// get reads one GET answer, bounded by ctx.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.transport.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("fleet request: %w", err)
	}
	if err := c.authorize(ctx, req); err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("fleet read %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fleet read %s: %s: %s", path, resp.Status, responseDetail(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("fleet read %s: %w", path, err)
	}
	return nil
}

// responseDetail is the answer's body, trimmed, for an error message.
func responseDetail(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return strings.TrimSpace(string(body))
}

// read is one snapshot read, bounded by readTimeout.
func (c *Client) read(ctx context.Context, path string, out any) error {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return c.get(rctx, path, out)
}

// snapshot is everything the connect reads.
type snapshot struct {
	agents   []contract.AgentStatus
	inbox    contract.Inbox
	projects []contract.ProjectStatus
	budgets  *contract.BudgetReport
	daemon   *contract.DaemonInfo

	// ok reports which of the optional reads succeeded; the connect fails on
	// anything but budgets, but an overflow keeps what it could read.
	agentsOK   bool
	inboxOK    bool
	projectsOK bool
	daemonOK   bool
}

// readSnapshot reads everything the connect reads. A failed budgets read leaves
// Budgets nil and does not fail the connect; any other failed read fails it.
func (c *Client) readSnapshot(ctx context.Context) (snapshot, error) {
	var snap snapshot

	if err := c.read(ctx, "/api/agents", &snap.agents); err != nil {
		return snapshot{}, err
	}
	snap.agentsOK = true

	if err := c.read(ctx, "/api/inbox", &snap.inbox); err != nil {
		return snapshot{}, err
	}
	snap.inboxOK = true

	if err := c.read(ctx, "/api/projects", &snap.projects); err != nil {
		return snapshot{}, err
	}
	snap.projectsOK = true

	var budgets contract.BudgetReport
	if err := c.read(ctx, "/api/budgets", &budgets); err == nil {
		snap.budgets = &budgets
	}

	if c.transport.Local {
		var daemon contract.DaemonInfo
		if err := c.read(ctx, "/api/daemon", &daemon); err != nil {
			return snapshot{}, err
		}
		snap.daemon, snap.daemonOK = &daemon, true
	}

	return snap, nil
}

// readOverflow reads everything the connect reads, keeping what it can: a
// failed budgets read leaves Budgets nil, and the others keep their previous
// value.
func (c *Client) readOverflow(ctx context.Context) snapshot {
	var snap snapshot

	if err := c.read(ctx, "/api/agents", &snap.agents); err == nil {
		snap.agentsOK = true
	}
	if err := c.read(ctx, "/api/inbox", &snap.inbox); err == nil {
		snap.inboxOK = true
	}
	if err := c.read(ctx, "/api/projects", &snap.projects); err == nil {
		snap.projectsOK = true
	}
	var budgets contract.BudgetReport
	if err := c.read(ctx, "/api/budgets", &budgets); err == nil {
		snap.budgets = &budgets
	}
	if c.transport.Local {
		var daemon contract.DaemonInfo
		if err := c.read(ctx, "/api/daemon", &daemon); err == nil {
			snap.daemon, snap.daemonOK = &daemon, true
		}
	}
	return snap
}

// install puts a read snapshot into the state.
func (c *Client) install(snap snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if snap.agentsOK {
		c.state.Agents = snap.agents
	}
	if snap.inboxOK {
		c.state.Inbox = snap.inbox
	}
	if snap.projectsOK {
		c.state.Projects = snap.projects
	}
	c.state.Budgets = snap.budgets
	if snap.daemonOK {
		c.state.Daemon = snap.daemon
	}
}

// connected marks the first full read landed and the stream up.
func (c *Client) connected() {
	c.mu.Lock()
	c.state.Loaded = true
	c.state.Epoch++
	c.state.Conn = acpclient.FleetConnected
	c.state.ConnErr = ""
	c.mu.Unlock()
	c.fire()
}

// fail records an unexpected end. A cancelled context is not a failure: Close
// and the caller's own cancellation end the client quietly.
func (c *Client) fail(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.state.Conn = acpclient.FleetDown
	c.state.ConnErr = err.Error()
	c.mu.Unlock()
	c.fire()
}

// refetchAgents replaces every row with the snapshot's, which wins over
// anything a delta set. A failed refetch leaves the rows alone.
func (c *Client) refetchAgents(ctx context.Context) {
	var rows []contract.AgentStatus
	if err := c.read(ctx, "/api/agents", &rows); err != nil {
		return
	}
	c.mu.Lock()
	c.state.Agents = rows
	c.mu.Unlock()
	c.fire()
}

// refetchBudgets replaces the report. A failed refetch leaves it as it was.
func (c *Client) refetchBudgets(ctx context.Context) {
	var report contract.BudgetReport
	if err := c.read(ctx, "/api/budgets", &report); err != nil {
		return
	}
	c.mu.Lock()
	c.state.Budgets = &report
	c.mu.Unlock()
	c.fire()
}

// refetchEverything rereads everything the connect read, after a
// replay_overflow, and bumps the epoch.
func (c *Client) refetchEverything(ctx context.Context) {
	snap := c.readOverflow(ctx)
	c.install(snap)
	c.mu.Lock()
	c.state.Epoch++
	c.mu.Unlock()
	c.fire()
}

// copyState is the caller's own copy of s: every slice and map, and the values
// the pointers point at.
func copyState(s acpclient.FleetState) acpclient.FleetState {
	out := s

	if s.Agents != nil {
		out.Agents = make([]contract.AgentStatus, len(s.Agents))
		copy(out.Agents, s.Agents)
		for i := range out.Agents {
			if a := out.Agents[i].Access; a != nil {
				access := *a
				out.Agents[i].Access = &access
			}
		}
	}
	if s.Projects != nil {
		out.Projects = make([]contract.ProjectStatus, len(s.Projects))
		copy(out.Projects, s.Projects)
	}

	out.Inbox = contract.Inbox{
		Groups: contract.InboxGroups{
			NeedsYou: copyStrings(s.Inbox.Groups.NeedsYou),
			Running:  copyStrings(s.Inbox.Groups.Running),
			Ready:    copyStrings(s.Inbox.Groups.Ready),
			Earlier:  copyStrings(s.Inbox.Groups.Earlier),
		},
		Queue: copyQueue(s.Inbox.Queue),
	}

	if s.Gates != nil {
		out.Gates = make(map[string]contract.GateRecord, len(s.Gates))
		for id, record := range s.Gates {
			out.Gates[id] = record
		}
	}

	if s.Budgets != nil {
		budgets := *s.Budgets
		if s.Budgets.Agents != nil {
			budgets.Agents = make([]contract.BudgetAgentSpend, len(s.Budgets.Agents))
			copy(budgets.Agents, s.Budgets.Agents)
		}
		out.Budgets = &budgets
	}

	if s.Daemon != nil {
		daemon := *s.Daemon
		daemon.Projects = copyStrings(s.Daemon.Projects)
		daemon.Pinned = copyStrings(s.Daemon.Pinned)
		if s.Daemon.Children != nil {
			daemon.Children = make([]contract.DaemonChild, len(s.Daemon.Children))
			copy(daemon.Children, s.Daemon.Children)
			for i := range daemon.Children {
				daemon.Children[i].Sessions = copySessions(s.Daemon.Children[i].Sessions)
			}
		}
		out.Daemon = &daemon
	}

	return out
}

// copyStrings copies a string slice, keeping nil nil.
func copyStrings(v []string) []string {
	if v == nil {
		return nil
	}
	out := make([]string, len(v))
	copy(out, v)
	return out
}

// copyQueue copies the inbox queue, keeping a nil slice nil.
func copyQueue(v []contract.InboxItem) []contract.InboxItem {
	if v == nil {
		return nil
	}
	out := make([]contract.InboxItem, len(v))
	copy(out, v)
	return out
}

// copySessions copies one daemon child's sessions.
func copySessions(v []contract.DaemonSession) []contract.DaemonSession {
	if v == nil {
		return nil
	}
	out := make([]contract.DaemonSession, len(v))
	copy(out, v)
	return out
}
