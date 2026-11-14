// Package fleetclient reads one bridge's fleet and keeps it live from the
// fleet stream (P5 F3).
package fleetclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
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

	// lastEventID is the position a reconnect resumes from: the id of the last
	// frame the client applied. Only the run goroutine reads and writes it.
	lastEventID string
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

// Close ends the stream, every timer and the client's goroutine, and waits for
// the goroutine to return. Calling it twice is a no-op.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel, started := c.cancel, c.started
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if started {
		<-c.done
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

// errStreamEnded is what ends an attempt when the stream closes without an
// error.
var errStreamEnded = errors.New("the fleet stream ended")

// run connects, and reconnects when the stream ends, until ctx is done or
// Close is called. A rejected token ends it: a 401 is never retried.
func (c *Client) run(ctx context.Context) {
	defer close(c.done)

	// failures counts the attempts that have failed since the backoff last
	// reset; a stream that stayed up for resetAfter resets it.
	failures := 0
	// failingSince is when the current run of failures began. Every attempt
	// that connects restarts it; once the failures have lasted downAfter, the
	// state goes down.
	var failingSince time.Time

	for {
		upAt, err := c.attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if unauthorized(err) {
			c.reject()
			return
		}
		if err == nil {
			err = errStreamEnded
		}

		// A stream that stayed up long enough earns a fresh backoff; any
		// connect at all restarts the clock the down state is measured from.
		if !upAt.IsZero() {
			failingSince = time.Time{}
			if c.now().Sub(upAt) >= c.resetAfter {
				failures = 0
			}
		}
		if failingSince.IsZero() {
			failingSince = c.now()
		}
		failures++

		down := c.now().Sub(failingSince) >= c.downAfter
		c.fail(down, err)

		wait := c.backoffWait(failures)
		if down {
			// Down keeps retrying for as long as the failures last: every
			// attempt waits the maximum.
			wait = c.backoffMax
		}
		if waitCtx(ctx, wait) != nil {
			return
		}
	}
}

// attempt is one connect: it opens the stream, reads the fleet, applies what
// arrived while it read, and consumes the stream until it ends. It reports
// when the stream came up (zero when it never did) and why the attempt ended;
// a nil error means the stream closed cleanly.
func (c *Client) attempt(ctx context.Context) (time.Time, error) {
	resp, err := c.openStream(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	src := newFrameSource()
	go src.read(resp.Body)

	snap, err := c.readSnapshot(ctx)
	if err != nil {
		return time.Time{}, err
	}
	c.install(snap)

	// The deltas that arrived while the reads were in flight are applied after
	// them, in arrival order: the reads are the snapshot, the deltas are what
	// happened since it was taken.
	frames, _, _ := src.take()
	for _, f := range frames {
		c.observe(f)
		c.applyFrame(ctx, f.Data)
	}

	c.connected()
	upAt := c.now()

	// The local daemon's info is kept current while the stream is up.
	stopDaemon := c.watchDaemon(ctx)
	err = c.consume(ctx, src)
	stopDaemon()

	return upAt, err
}

// consume applies the stream's frames until it ends. It returns why the stream
// ended: nil means it ended cleanly, or that ctx ended the attempt.
func (c *Client) consume(ctx context.Context, src *frameSource) error {
	for {
		frames, done, err := src.take()
		for _, f := range frames {
			c.observe(f)
			c.applyFrame(ctx, f.Data)
		}
		if done {
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			return errStreamEnded
		}
		if err := src.wait(ctx); err != nil {
			return nil
		}
	}
}

// observe records the position a reconnect resumes from: a frame carries its
// own id, and the overflow nudge carries none.
func (c *Client) observe(f eventFrame) {
	if f.ID != "" {
		c.lastEventID = f.ID
	}
}

// backoffWait is the wait after failures consecutive failures: backoffFirst,
// then doubling, capped at backoffMax.
func (c *Client) backoffWait(failures int) time.Duration {
	wait := c.backoffFirst
	for i := 1; i < failures; i++ {
		if wait >= c.backoffMax {
			break
		}
		wait *= 2
	}
	if wait > c.backoffMax {
		wait = c.backoffMax
	}
	return wait
}

// waitCtx waits for d, and returns early when ctx ends. A zero or negative
// wait is no wait at all.
func waitCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
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

// statusError is a non-200 answer. It carries the status, so the client can
// tell a rejected token from a transient failure.
type statusError struct {
	status int
	msg    string
}

// Error is the answer's message.
func (e *statusError) Error() string { return e.msg }

// unauthorized reports whether err is the bridge's 401. The bridge answers it
// only when a token is rejected, so a 401 anywhere means the same thing: the
// token is wrong, and nothing is retried.
func unauthorized(err error) bool {
	var rejected *statusError
	return errors.As(err, &rejected) && rejected.status == http.StatusUnauthorized
}

// openStream opens GET /api/events?stream=fleet. Only a 200 answer is a
// connected stream.
func (c *Client) openStream(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.transport.BaseURL+streamPath, nil)
	if err != nil {
		return nil, fmt.Errorf("fleet stream: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.lastEventID != "" {
		// A reconnect resumes from the last frame applied: the bridge replays
		// what happened after it.
		req.Header.Set("Last-Event-ID", c.lastEventID)
	}
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
		return nil, &statusError{
			status: resp.StatusCode,
			msg:    fmt.Sprintf("fleet stream: %s: %s", resp.Status, detail),
		}
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
		return &statusError{
			status: resp.StatusCode,
			msg:    fmt.Sprintf("fleet read %s: %s: %s", path, resp.Status, responseDetail(resp)),
		}
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
	} else if unauthorized(err) {
		// A rejected token is not a missing budget report: it stops the client.
		return snapshot{}, err
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
// value. The second answer reports whether one of the reads was rejected, so
// the caller can stop the client.
func (c *Client) readOverflow(ctx context.Context) (snapshot, bool) {
	var snap snapshot
	rejected := false

	if err := c.read(ctx, "/api/agents", &snap.agents); err == nil {
		snap.agentsOK = true
	} else if unauthorized(err) {
		rejected = true
	}
	if err := c.read(ctx, "/api/inbox", &snap.inbox); err == nil {
		snap.inboxOK = true
	} else if unauthorized(err) {
		rejected = true
	}
	if err := c.read(ctx, "/api/projects", &snap.projects); err == nil {
		snap.projectsOK = true
	} else if unauthorized(err) {
		rejected = true
	}
	var budgets contract.BudgetReport
	if err := c.read(ctx, "/api/budgets", &budgets); err == nil {
		snap.budgets = &budgets
	} else if unauthorized(err) {
		rejected = true
	}
	if c.transport.Local {
		var daemon contract.DaemonInfo
		if err := c.read(ctx, "/api/daemon", &daemon); err == nil {
			snap.daemon, snap.daemonOK = &daemon, true
		} else if unauthorized(err) {
			rejected = true
		}
	}
	return snap, rejected
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

// fail records why an attempt ended: the state is reconnecting while the
// failures have lasted less than downAfter, and down once they have lasted
// longer. The retries never stop on their own.
func (c *Client) fail(down bool, err error) {
	c.mu.Lock()
	if down {
		c.state.Conn = acpclient.FleetDown
	} else {
		c.state.Conn = acpclient.FleetReconnecting
	}
	c.state.ConnErr = err.Error()
	c.mu.Unlock()
	c.fire()
}

// reject records the bridge's 401 and stops everything: the token is wrong, so
// the stream, the retries and the timers all end, and no further request is
// made.
func (c *Client) reject() {
	c.mu.Lock()
	c.state.Conn = acpclient.FleetRejected
	c.state.ConnErr = "token rejected for " + c.transport.Server
	cancel := c.cancel
	c.mu.Unlock()
	c.fire()
	if cancel != nil {
		cancel()
	}
}

// watchDaemon starts the local daemon's reread while the stream is up. The
// returned function stops it and waits for it to finish.
func (c *Client) watchDaemon(ctx context.Context) func() {
	if !c.transport.Local || c.daemonEvery <= 0 {
		return func() {}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pollDaemon(watchCtx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// pollDaemon rereads GET /api/daemon on the interval. A failed reread leaves
// the info as it was and tries again on the next tick; a 401 stops the client.
func (c *Client) pollDaemon(ctx context.Context) {
	ticker := time.NewTicker(c.daemonEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var info contract.DaemonInfo
		if err := c.read(ctx, "/api/daemon", &info); err != nil {
			if unauthorized(err) {
				c.reject()
				return
			}
			continue
		}
		if c.setDaemon(&info) {
			c.fire()
		}
	}
}

// setDaemon replaces the daemon's info when the answer differs from the one
// held, and reports whether it did.
func (c *Client) setDaemon(info *contract.DaemonInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reflect.DeepEqual(c.state.Daemon, info) {
		return false
	}
	c.state.Daemon = info
	return true
}

// refetchAgents replaces every row with the snapshot's, which wins over
// anything a delta set. A failed refetch leaves the rows alone; a rejected
// token stops the client.
func (c *Client) refetchAgents(ctx context.Context) {
	var rows []contract.AgentStatus
	if err := c.read(ctx, "/api/agents", &rows); err != nil {
		if unauthorized(err) {
			c.reject()
		}
		return
	}
	c.mu.Lock()
	c.state.Agents = rows
	c.mu.Unlock()
	c.fire()
}

// refetchBudgets replaces the report. A failed refetch leaves it as it was; a
// rejected token stops the client.
func (c *Client) refetchBudgets(ctx context.Context) {
	var report contract.BudgetReport
	if err := c.read(ctx, "/api/budgets", &report); err != nil {
		if unauthorized(err) {
			c.reject()
		}
		return
	}
	c.mu.Lock()
	c.state.Budgets = &report
	c.mu.Unlock()
	c.fire()
}

// refetchEverything rereads everything the connect read, after a
// replay_overflow, and bumps the epoch. A rejected token stops the client
// instead: nothing is installed and the epoch does not move.
func (c *Client) refetchEverything(ctx context.Context) {
	snap, rejected := c.readOverflow(ctx)
	if rejected {
		c.reject()
		return
	}
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
			if p := out.Agents[i].Pending; p != nil {
				pending := *p
				if p.Params != nil {
					pending.Params = make(json.RawMessage, len(p.Params))
					copy(pending.Params, p.Params)
				}
				out.Agents[i].Pending = &pending
			}
			if w := out.Agents[i].Workspace; w != nil {
				workspace := *w
				out.Agents[i].Workspace = &workspace
			}
			if g := out.Agents[i].GateOverride; g != nil {
				override := *g
				out.Agents[i].GateOverride = &override
			}
			if t := out.Agents[i].PushedAt; t != nil {
				pushed := *t
				out.Agents[i].PushedAt = &pushed
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

// copyQueue copies the inbox queue, keeping a nil slice nil. Each item's
// submission is its own copy.
func copyQueue(v []contract.InboxItem) []contract.InboxItem {
	if v == nil {
		return nil
	}
	out := make([]contract.InboxItem, len(v))
	copy(out, v)
	for i := range out {
		if s := out[i].Submission; s != nil {
			submission := *s
			out[i].Submission = &submission
		}
	}
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
