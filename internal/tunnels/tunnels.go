package tunnels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/fleetprofile"
	"marshal/web/bridge/contract"
)

const (
	// statusTimeout bounds the remote status command a tunnel reads its
	// destination from.
	statusTimeout = 30 * time.Second
	// reportTimeout bounds one report to the daemon.
	reportTimeout = 5 * time.Second
	// probeTimeout bounds one GET /api/daemon through a tunnel.
	probeTimeout = 2 * time.Second
	// probeInterval is how often a forward that has not answered yet is
	// probed.
	probeInterval = 250 * time.Millisecond
	// liveReset is how long an attempt has to have been live for its failure
	// to restart the backoff at its first delay.
	liveReset = time.Minute
)

// Config is what Run needs. A zero tuning field is the production default.
type Config struct {
	FleetFile string // fleet.toml, re-read every Poll
	Dir       string // where the tunnel sockets are made, mode 0700
	// Report sends every host's state to the daemon, replacing its last
	// report. Required.
	Report func(ctx context.Context, hosts []contract.DaemonHost) error
	// Probe asks the daemon at socket for GET /api/daemon, and answers its
	// preview address. Required.
	Probe  func(ctx context.Context, socket string) (previewAddr string, err error)
	Stderr io.Writer // where load and report failures are written; nil discards them
	SSH    string    // the ssh binary; "" is "ssh" on PATH
	// FreePort answers a port free on this machine's loopback for a preview
	// forward. Required of each attempt, so nil is freeLoopbackPort.
	FreePort func() (int, error)

	Commands Commands         // nil: Exec()
	Now      func() time.Time // nil: time.Now

	Poll       time.Duration // fleet.toml re-read; zero is 2s
	Check      time.Duration // a live tunnel's re-probe; zero is 15s
	Backoff    time.Duration // the first restart's delay; zero is 1s
	MaxBackoff time.Duration // the longest delay; zero is 30s
	LiveWithin time.Duration // how long a started forward has to answer; zero is 30s
}

// withDefaults is cfg with every zero field set to its production default.
func (c Config) withDefaults() Config {
	if c.Commands == nil {
		c.Commands = Exec()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.FreePort == nil {
		c.FreePort = freeLoopbackPort
	}
	if c.Poll <= 0 {
		c.Poll = 2 * time.Second
	}
	if c.Check <= 0 {
		c.Check = 15 * time.Second
	}
	if c.Backoff <= 0 {
		c.Backoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.LiveWithin <= 0 {
		c.LiveWithin = 30 * time.Second
	}
	return c
}

// Run keeps one tunnel per host in cfg.FleetFile until ctx ends. Then it stops
// every ssh it started and returns nil. A missing Report or Probe is an error.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Report == nil {
		return errors.New("tunnels: Config.Report is required")
	}
	if cfg.Probe == nil {
		return errors.New("tunnels: Config.Probe is required")
	}
	r := &runner{
		cfg:    cfg.withDefaults(),
		hosts:  map[string]*tunnel{},
		signal: make(chan struct{}, 1),
	}
	return r.run(ctx)
}

// runner is one Run's state: the hosts it keeps a tunnel for, and the reporter
// that sends them to the daemon.
type runner struct {
	cfg Config

	mu    sync.Mutex // guards hosts and every tunnel's state
	hosts map[string]*tunnel

	signal chan struct{} // a report is wanted; at most one is pending

	stderrMu    sync.Mutex // serialises writes to cfg.Stderr
	lastLoadErr string     // the fleet.toml error already written; run loop only
}

// tunnel is one host's forward and what the daemon is told about it.
type tunnel struct {
	name    string
	host    fleetprofile.Host
	state   contract.DaemonHost
	ctx     context.Context // ends the host's goroutine
	cancel  context.CancelFunc
	stopped chan struct{} // closed once the goroutine has stopped its process
	// liveFrom is when the current attempt first answered, zero when it has
	// not answered yet. The goroutine that owns the attempt is the only
	// reader and writer.
	liveFrom time.Time
}

// run reconciles the tunnels until ctx ends, with the reporter alongside.
func (r *runner) run(ctx context.Context) error {
	reportCtx, stopReports := context.WithCancel(ctx)
	reportsDone := make(chan struct{})
	go func() {
		defer close(reportsDone)
		r.reportLoop(reportCtx)
	}()
	defer func() {
		stopReports()
		<-reportsDone
	}()

	ticker := time.NewTicker(r.cfg.Poll)
	defer ticker.Stop()
	r.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			r.stopAll()
			return nil
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

// reconcile starts, restarts and stops tunnels so that they match fleet.toml.
func (r *runner) reconcile(ctx context.Context) {
	file, err := fleetprofile.Load(r.cfg.FleetFile)
	if err != nil {
		// The tunnels keep running: a file that cannot be read says nothing
		// about the hosts it held a moment ago.
		msg := err.Error()
		if msg != r.lastLoadErr {
			r.lastLoadErr = msg
			r.stderrf("tunnels: %s", msg)
		}
		return
	}
	r.lastLoadErr = ""

	var stopping []*tunnel
	var removedSockets []string
	changed := false
	r.mu.Lock()
	for name, t := range r.hosts {
		h, kept := file.Hosts[name]
		if kept && h.SSH == t.host.SSH && h.Marshal == t.host.Marshal {
			continue
		}
		delete(r.hosts, name)
		stopping = append(stopping, t)
		if !kept {
			// A host that is gone leaves no socket behind; one that moved is
			// restarted under the same name, and its new forward replaces it.
			removedSockets = append(removedSockets, t.state.Socket)
		}
		changed = true
	}
	r.mu.Unlock()

	for _, t := range stopping {
		t.cancel()
		<-t.stopped
	}
	for _, socket := range removedSockets {
		if socket != "" {
			// A file that is already gone is fine.
			_ = os.Remove(socket)
		}
	}

	var starting []*tunnel
	r.mu.Lock()
	for name, h := range file.Hosts {
		if _, kept := r.hosts[name]; kept {
			continue
		}
		t := r.newTunnel(ctx, name, h)
		r.hosts[name] = t
		starting = append(starting, t)
		changed = true
	}
	r.mu.Unlock()

	for _, t := range starting {
		go func(t *tunnel) {
			defer close(t.stopped)
			r.supervise(t)
		}(t)
	}
	if changed {
		r.wake()
	}
}

// newTunnel is the state a host starts in, before it has had an outcome.
func (r *runner) newTunnel(ctx context.Context, name string, h fleetprofile.Host) *tunnel {
	socket, err := socketPath(r.cfg.Dir, name)
	if err != nil {
		// The attempt meets the same error again and reports it; there is
		// just no socket to name here.
		socket = ""
	}
	hostCtx, cancel := context.WithCancel(ctx)
	return &tunnel{
		name: name,
		host: h,
		state: contract.DaemonHost{
			Name:   name,
			SSH:    h.SSH,
			State:  contract.HostConnecting,
			Socket: socket,
			Since:  r.cfg.Now().UnixMilli(),
		},
		ctx:     hostCtx,
		cancel:  cancel,
		stopped: make(chan struct{}),
	}
}

// stopAll ends every host's goroutine and waits for each to stop its process.
// It leaves the socket files alone: the next start replaces them
// (StreamLocalBindUnlink).
func (r *runner) stopAll() {
	r.mu.Lock()
	all := make([]*tunnel, 0, len(r.hosts))
	for _, t := range r.hosts {
		all = append(all, t)
	}
	r.mu.Unlock()
	for _, t := range all {
		t.cancel()
	}
	for _, t := range all {
		<-t.stopped
	}
}

// supervise keeps one host's tunnel, one attempt at a time, until the context
// ends.
func (r *runner) supervise(t *tunnel) {
	delay := r.cfg.Backoff
	for {
		if t.ctx.Err() != nil {
			return
		}
		t.liveFrom = time.Time{}
		err := r.attempt(t)
		if t.ctx.Err() != nil {
			// The attempt ended because this host was stopped, not because it
			// failed.
			return
		}
		r.setState(t, contract.HostDown, err.Error(), 0)
		if !t.liveFrom.IsZero() && r.cfg.Now().Sub(t.liveFrom) >= liveReset {
			// A tunnel that was live for a while is not in a failure loop.
			delay = r.cfg.Backoff
		}
		timer := time.NewTimer(delay)
		select {
		case <-t.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > r.cfg.MaxBackoff {
			delay = r.cfg.MaxBackoff
		}
	}
}

// attempt makes one try at t's forward. It answers only when that try is over:
// the forward ended, never answered, or could not be made.
func (r *runner) attempt(t *tunnel) error {
	local, err := socketPath(r.cfg.Dir, t.name)
	if err != nil {
		return err
	}
	out, err := r.status(t)
	if err != nil {
		return err
	}
	remote, previewAddr, err := remoteDaemon(t.name, out)
	if err != nil {
		return err
	}
	for _, path := range []string{local, remote} {
		if strings.Contains(path, ":") {
			return fmt.Errorf(`ssh cannot forward a socket path that contains ":": %s`, path)
		}
	}
	// A port of its own each attempt: one the last attempt's ssh still holds
	// surfaces as "Address already in use", and the next attempt avoids it.
	previewPort := 0
	if previewAddr != "" {
		if previewPort, err = r.cfg.FreePort(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(r.cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("make the tunnel directory %s: %w", r.cfg.Dir, err)
	}
	proc, err := r.cfg.Commands.Start(forwardArgv(r.cfg.SSH, t.host, local, remote, previewPort, previewAddr))
	if err != nil {
		return err
	}
	if err := r.awaitLive(t, proc, local, previewPort); err != nil {
		return err
	}
	return r.watchLive(t, proc, local, previewAddr, previewPort)
}

// status reads the remote daemon's status document for t's host.
func (r *runner) status(t *tunnel) ([]byte, error) {
	ctx, cancel := context.WithTimeout(t.ctx, statusTimeout)
	defer cancel()
	out, err := r.cfg.Commands.Output(ctx, statusArgv(r.cfg.SSH, t.host))
	if err != nil {
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == 127 {
			// The remote shell has no marshal on its PATH, and the file is
			// where that is fixed.
			return nil, fmt.Errorf("%s; set marshal = %q in [hosts.%s]",
				cmdErr.Error(), "/path/to/marshal", t.name)
		}
		return nil, err
	}
	return out, nil
}

// awaitLive waits for the forward to answer through the local socket, and
// leaves it running once it does. The answer's preview address does not count
// here: this probe only learns that the forward works.
func (r *runner) awaitLive(t *tunnel, proc Process, local string, previewPort int) error {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(r.cfg.LiveWithin)
	defer deadline.Stop()
	for {
		select {
		case <-t.ctx.Done():
			proc.Stop()
			return t.ctx.Err()
		case <-proc.Done():
			return proc.Err()
		case <-deadline.C:
			proc.Stop()
			return fmt.Errorf("the tunnel to %s did not answer within %s", t.name, r.cfg.LiveWithin)
		case <-ticker.C:
			// The process exiting is the answer, even if this probe was due.
			select {
			case <-proc.Done():
				return proc.Err()
			default:
			}
			if _, err := r.probe(t, local); err == nil {
				t.liveFrom = r.cfg.Now()
				r.setState(t, contract.HostLive, "", previewPort)
				return nil
			}
		}
	}
}

// watchLive keeps probing a live tunnel. ssh stays up while its daemon is not
// answering; only a forward that exits, or a remote preview listener that moved
// out from under it, ends the attempt.
func (r *runner) watchLive(t *tunnel, proc Process, local, previewAddr string, previewPort int) error {
	ticker := time.NewTicker(r.cfg.Check)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			proc.Stop()
			return t.ctx.Err()
		case <-proc.Done():
			return proc.Err()
		case <-ticker.C:
			select {
			case <-proc.Done():
				return proc.Err()
			default:
			}
			addr, err := r.probe(t, local)
			if err != nil {
				r.setState(t, contract.HostDown,
					fmt.Sprintf("%s's daemon stopped answering: %v", t.name, err), 0)
				continue
			}
			if addr != previewAddr {
				// The forward answers a listener this tunnel does not carry:
				// the next attempt forwards the one the daemon now serves.
				proc.Stop()
				return fmt.Errorf("%s's daemon moved its preview listener to %s",
					t.name, orNowhere(addr))
			}
			r.setState(t, contract.HostLive, "", previewPort)
		}
	}
}

// orNowhere is addr, or "nowhere" when the daemon answered no preview address
// where it had one.
func orNowhere(addr string) string {
	if addr == "" {
		return "nowhere"
	}
	return addr
}

// probe asks the daemon behind the tunnel to answer, and answers the preview
// address it reports.
func (r *runner) probe(t *tunnel, socket string) (string, error) {
	ctx, cancel := context.WithTimeout(t.ctx, probeTimeout)
	defer cancel()
	return r.cfg.Probe(ctx, socket)
}

// setState records t's state and signals the reporter when it changed. Since
// moves only with the state, LastError belongs to down, and PreviewPort to
// live.
func (r *runner) setState(t *tunnel, state contract.HostState, lastErr string, previewPort int) {
	port := 0
	if state == contract.HostLive {
		port = previewPort
	}
	r.mu.Lock()
	changed := t.state.State != state || t.state.LastError != lastErr || t.state.PreviewPort != port
	if t.state.State != state {
		t.state.State = state
		t.state.Since = r.cfg.Now().UnixMilli()
	}
	t.state.PreviewPort = port
	switch state {
	case contract.HostDown:
		t.state.LastError = lastErr
	case contract.HostLive:
		t.state.LastError = ""
	}
	r.mu.Unlock()
	if changed {
		r.wake()
	}
}

// wake asks for a report, keeping at most one pending: the reporter sends the
// latest set, not every intermediate one.
func (r *runner) wake() {
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

// reportLoop reports the whole set whenever it is signalled. The first report
// is empty, so a report a previous helper left is cleared.
func (r *runner) reportLoop(ctx context.Context) {
	pending := []contract.DaemonHost{}
	for {
		// One set, retried until it lands or a newer one replaces it.
		for {
			err := r.report(ctx, pending)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			r.stderrf("tunnels: report: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.cfg.Backoff):
			case <-r.signal:
				pending = r.snapshot()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-r.signal:
			pending = r.snapshot()
		}
	}
}

// report sends one set to the daemon, bounded by reportTimeout.
func (r *runner) report(ctx context.Context, hosts []contract.DaemonHost) error {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	return r.cfg.Report(ctx, hosts)
}

// snapshot is every host's state, sorted by name.
func (r *runner) snapshot() []contract.DaemonHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.hosts))
	for name := range r.hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	hosts := make([]contract.DaemonHost, 0, len(names))
	for _, name := range names {
		hosts = append(hosts, r.hosts[name].state)
	}
	return hosts
}

// stderrf writes one line to cfg.Stderr, or discards it.
func (r *runner) stderrf(format string, args ...any) {
	if r.cfg.Stderr == nil {
		return
	}
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	fmt.Fprintf(r.cfg.Stderr, format+"\n", args...)
}
