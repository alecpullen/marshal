package tunnels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/fleetprofile"
	"marshal/web/bridge/contract"
)

// remoteSocketPath is the socket the scripted status document names.
const remoteSocketPath = "/home/u/.local/share/marshal/daemon/daemon.sock"

// statusDocument is `marshal daemon status --json` as a tunnel reads it.
func statusDocument() []byte {
	return []byte(`{"schemaVersion":1,"answering":true,"daemon":{"socket":"` + remoteSocketPath + `"}}`)
}

// fixture is one test's fleet.toml, socket directory and fakes.
type fixture struct {
	t      *testing.T
	fleet  string
	dir    string
	cfg    Config
	script *script
	rec    *recorder
	probe  *prober
	stderr *syncBuffer
}

// newFixture is a fixture with production-shaped fakes and millisecond
// timings. A test overrides what it cares about before start.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:      t,
		fleet:  filepath.Join(root, "fleet.toml"),
		dir:    filepath.Join(root, "hosts"),
		script: &script{},
		rec:    &recorder{},
		probe:  &prober{},
		stderr: &syncBuffer{},
	}
	f.script.outputFn = func([]string) ([]byte, error) { return statusDocument(), nil }
	f.script.startFn = func([]string) (Process, error) {
		return f.script.addLive(), nil
	}
	f.probe.fn = func(string) error { return nil }
	f.cfg = Config{
		FleetFile:  f.fleet,
		Dir:        f.dir,
		Report:     f.rec.report,
		Probe:      f.probe.probe,
		Stderr:     f.stderr,
		Commands:   f.script,
		Poll:       10 * time.Millisecond,
		Check:      10 * time.Millisecond,
		Backoff:    20 * time.Millisecond,
		MaxBackoff: 80 * time.Millisecond,
		LiveWithin: 2 * time.Second,
	}
	return f
}

// writeFleet writes fleet.toml, mode 0600, from name/ssh pairs.
func (f *fixture) writeFleet(pairs ...string) {
	f.t.Helper()
	var body strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&body, "[hosts.%s]\nssh = %q\n", pairs[i], pairs[i+1])
	}
	if err := os.WriteFile(f.fleet, []byte(body.String()), 0o600); err != nil {
		f.t.Fatalf("write fleet.toml: %v", err)
	}
}

// running is one Run started by a fixture.
type running struct {
	cancel   context.CancelFunc
	returned chan struct{}
	err      error // read after returned is closed
}

// start runs Run until the test ends.
func (f *fixture) start() *running {
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, returned: make(chan struct{})}
	go func() {
		defer close(r.returned)
		r.err = Run(ctx, f.cfg)
	}()
	f.t.Cleanup(func() {
		r.cancel()
		select {
		case <-r.returned:
		case <-time.After(10 * time.Second):
			f.t.Errorf("Run did not return after its context ended")
		}
	})
	return r
}

// TestRunNeedsReportAndProbe covers the two required callbacks.
func TestRunNeedsReportAndProbe(t *testing.T) {
	probe := func(context.Context, string) error { return nil }
	report := func(context.Context, []contract.DaemonHost) error { return nil }

	err := Run(context.Background(), Config{Probe: probe})
	if err == nil {
		t.Fatal("Run started without a Report")
	}
	if want := "tunnels: Config.Report is required"; err.Error() != want {
		t.Fatalf("Run error = %q, want %q", err.Error(), want)
	}

	err = Run(context.Background(), Config{Report: report})
	if err == nil {
		t.Fatal("Run started without a Probe")
	}
	if want := "tunnels: Config.Probe is required"; err.Error() != want {
		t.Fatalf("Run error = %q, want %q", err.Error(), want)
	}
}

// TestTheFirstReportIsEmpty covers the report that clears a previous helper's.
func TestTheFirstReportIsEmpty(t *testing.T) {
	f := newFixture(t)
	f.writeFleet()
	f.start()

	// Several polls: a file with no hosts asks for nothing else.
	time.Sleep(150 * time.Millisecond)

	if got := f.rec.count(); got != 1 {
		t.Fatalf("%d reports, want exactly 1: %v", got, f.rec.all())
	}
	first, _ := f.rec.at(0)
	if len(first) != 0 {
		t.Fatalf("the first report holds %v, want no hosts", first)
	}
}

// TestALiveTunnelIsReported covers a forward that answers: connecting, then
// live, with the socket and destination it was made for.
func TestALiveTunnelIsReported(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.start()

	idx := f.rec.wait(t, 0, func(hosts []contract.DaemonHost) bool { return len(hosts) > 0 })
	if idx != 1 {
		t.Fatalf("the first report with a host is report %d, want 1 (after the empty one)", idx)
	}
	first, _ := f.rec.at(0)
	if len(first) != 0 {
		t.Fatalf("report 0 holds %v, want no hosts", first)
	}
	connecting, _ := f.rec.at(1)
	if h, ok := hostNamed(connecting, "box"); !ok || h.State != contract.HostConnecting {
		t.Fatalf("report 1 = %v, want box connecting", connecting)
	}

	liveIdx := f.rec.wait(t, 2, hasState("box", contract.HostLive, ""))
	report, _ := f.rec.at(liveIdx)
	h, _ := hostNamed(report, "box")
	if h.SSH != "me@box" {
		t.Errorf("SSH = %q, want %q", h.SSH, "me@box")
	}
	wantSocket := filepath.Join(f.dir, "box.sock")
	if h.Socket != wantSocket {
		t.Errorf("Socket = %q, want %q", h.Socket, wantSocket)
	}
	if h.Since == 0 {
		t.Error("Since is 0, want the time the state last changed")
	}
	if h.LastError != "" {
		t.Errorf("LastError = %q, want it clear on a live host", h.LastError)
	}

	starts := f.script.startArgvs()
	if len(starts) != 1 {
		t.Fatalf("Start was called %d times, want 1", len(starts))
	}
	assertArgv(t, starts[0],
		forwardArgv("", fleetprofile.Host{Name: "box", SSH: "me@box"}, wantSocket, remoteSocketPath))
}

// TestAFailedStatusCallIsDownWithItsLine covers ssh's own message becoming the
// host's last error.
func TestAFailedStatusCallIsDownWithItsLine(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Backoff = time.Second
	f.cfg.MaxBackoff = time.Second
	f.script.outputFn = func([]string) ([]byte, error) {
		return nil, &CommandError{Code: 255, Line: "Permission denied (publickey)."}
	}
	f.start()

	idx := f.rec.wait(t, 0, hasState("box", contract.HostDown, "Permission denied (publickey)."))
	report, _ := f.rec.at(idx)
	h, _ := hostNamed(report, "box")
	if want := "Permission denied (publickey)."; h.LastError != want {
		t.Errorf("LastError = %q, want %q", h.LastError, want)
	}
	if n := f.script.startCount(); n != 0 {
		t.Errorf("Start was called %d times, want none", n)
	}
}

// TestAMissingMarshalNamesTheSetting covers ssh's 127: the host names the
// setting that fixes it.
func TestAMissingMarshalNamesTheSetting(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Backoff = time.Second
	f.cfg.MaxBackoff = time.Second
	f.script.outputFn = func([]string) ([]byte, error) {
		return nil, &CommandError{Code: 127, Line: "sh: 1: marshal: not found"}
	}
	f.start()

	idx := f.rec.wait(t, 0, hasState("box", contract.HostDown, "set marshal ="))
	report, _ := f.rec.at(idx)
	h, _ := hostNamed(report, "box")
	if !strings.Contains(h.LastError, "set marshal =") {
		t.Fatalf("LastError = %q, want it to say how to set marshal", h.LastError)
	}
	if !strings.Contains(h.LastError, "[hosts.box]") {
		t.Errorf("LastError = %q, want it to name [hosts.box]", h.LastError)
	}
}

// TestAnExitedForwardIsRestartedWithBackoff covers a forward that dies at once,
// every time: it is started again, waiting at least Backoff and backing off as
// it goes.
func TestAnExitedForwardIsRestartedWithBackoff(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Backoff = 20 * time.Millisecond
	f.cfg.MaxBackoff = 80 * time.Millisecond
	f.script.startFn = func([]string) (Process, error) {
		return newExitedProcess(&CommandError{Code: 255, Line: "Connection closed by remote host"}), nil
	}
	f.start()

	deadline := time.Now().Add(5 * time.Second)
	for f.script.startCount() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("Start was called %d times in 5s, want 5", f.script.startCount())
		}
		time.Sleep(time.Millisecond)
	}

	times := f.script.startTimes()
	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		gaps = append(gaps, gap)
		if gap < f.cfg.Backoff {
			t.Errorf("start %d came after %v, below Backoff %v", i, gap, f.cfg.Backoff)
		}
	}
	// The delay doubles, so the gaps grow and never shrink. The tolerance is
	// for a timer that fired late, not for a shrinking delay.
	for i := 1; i < len(gaps); i++ {
		if gaps[i]+20*time.Millisecond < gaps[i-1] {
			t.Errorf("the gap between starts shrank: %v then %v", gaps[i-1], gaps[i])
		}
	}
	if last := gaps[len(gaps)-1]; last < 2*f.cfg.Backoff {
		t.Errorf("the last gap %v is under twice Backoff %v; the delay never doubled", last, f.cfg.Backoff)
	}
}

// TestARetryStaysDownUntilLive covers D7's rule: a retry after down is not
// connecting again.
func TestARetryStaysDownUntilLive(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Backoff = 10 * time.Millisecond
	f.cfg.MaxBackoff = 10 * time.Millisecond
	var calls atomic.Int32
	f.script.outputFn = func([]string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, &CommandError{Code: 255, Line: "Connection refused"}
		}
		return statusDocument(), nil
	}
	f.start()

	down := f.rec.wait(t, 0, hasState("box", contract.HostDown, "Connection refused"))
	live := f.rec.wait(t, down+1, hasState("box", contract.HostLive, ""))
	for i := down + 1; i <= live; i++ {
		report, _ := f.rec.at(i)
		if h, ok := hostNamed(report, "box"); ok && h.State == contract.HostConnecting {
			t.Fatalf("report %d, after the first down, says connecting: %v", i, report)
		}
	}
}

// TestAForwardThatNeverAnswersIsStopped covers a start that never becomes a
// tunnel: LiveWithin passes, the process is stopped, and the host is down.
func TestAForwardThatNeverAnswersIsStopped(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.LiveWithin = 400 * time.Millisecond
	f.cfg.Backoff = time.Second
	f.cfg.MaxBackoff = time.Second
	f.probe.fn = func(string) error { return errors.New("no answer") }
	f.start()

	f.rec.wait(t, 0, hasState("box", contract.HostDown, "did not answer"))
	if p := f.script.process(0); p == nil || !p.stopped() {
		t.Fatal("the process was not stopped after LiveWithin passed")
	}
}

// TestALiveTunnelWhoseDaemonStopsIsDown covers a remote daemon that stops
// answering: the host is down, ssh stays up, and a later answer is live again.
func TestALiveTunnelWhoseDaemonStopsIsDown(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	var failing atomic.Bool
	f.probe.fn = func(string) error {
		if failing.Load() {
			return errors.New("no route to the daemon")
		}
		return nil
	}
	f.start()

	live := f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))
	failing.Store(true)
	down := f.rec.wait(t, live+1, hasState("box", contract.HostDown, "daemon stopped answering"))
	if down <= live {
		t.Fatalf("the down report is report %d, at or before the live one %d", down, live)
	}
	if p := f.script.process(0); p == nil || p.stopped() {
		t.Fatal("the process was stopped while the host was only down")
	}
	failing.Store(false)
	f.rec.wait(t, down+1, hasState("box", contract.HostLive, ""))
}

// TestAHostAddedToTheFileStartsATunnel covers the re-read: a host written after
// Run started is tunneled.
func TestAHostAddedToTheFileStartsATunnel(t *testing.T) {
	f := newFixture(t)
	f.writeFleet()
	f.start()
	f.rec.wait(t, 0, func(hosts []contract.DaemonHost) bool { return len(hosts) == 0 })

	f.writeFleet("box", "me@box")
	f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))
}

// TestAHostRemovedFromTheFileIsStopped covers the other way: its process is
// stopped, its socket file is removed, and the next report lacks it.
func TestAHostRemovedFromTheFileIsStopped(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Poll = 5 * time.Millisecond
	f.start()

	live := f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatalf("make the socket directory: %v", err)
	}
	socket := filepath.Join(f.dir, "box.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("write the socket file: %v", err)
	}

	f.writeFleet()
	f.rec.wait(t, live+1, func(hosts []contract.DaemonHost) bool {
		_, kept := hostNamed(hosts, "box")
		return !kept
	})

	if p := f.script.process(0); p == nil || !p.stopped() {
		t.Fatal("the removed host's process was not stopped")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file is still there: %v", err)
	}
}

// TestAChangedDestinationRestartsTheTunnel covers a host that now names another
// destination: its forward is made again.
func TestAChangedDestinationRestartsTheTunnel(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Poll = 5 * time.Millisecond
	f.start()
	f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))

	f.writeFleet("box", "me@other")

	idx := f.rec.wait(t, 0, func(hosts []contract.DaemonHost) bool {
		h, ok := hostNamed(hosts, "box")
		return ok && h.SSH == "me@other" && h.State == contract.HostLive
	})
	if idx < 0 {
		t.Fatal("the restarted tunnel was never reported live")
	}
	starts := f.script.startArgvs()
	if len(starts) < 2 {
		t.Fatalf("Start was called %d times, want the tunnel made again", len(starts))
	}
	last := starts[len(starts)-1]
	if got := last[len(last)-1]; got != "me@other" {
		t.Fatalf("the last Start was made for %q, want %q (argv %q)", got, "me@other", last)
	}
}

// TestAFileThatCannotLoadKeepsTheTunnels covers a file that stops being private:
// the tunnels keep running, and the load error is written once.
func TestAFileThatCannotLoadKeepsTheTunnels(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box")
	f.cfg.Poll = 5 * time.Millisecond
	f.start()
	f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))

	// Make the file non-private before rewriting it: with the rewrite first,
	// a poll can see a valid private file with no hosts in between, which
	// stops the tunnel for the right reason and masks what this test is
	// about.
	if err := os.Chmod(f.fleet, 0o644); err != nil {
		t.Fatalf("make fleet.toml readable by others: %v", err)
	}
	f.writeFleet()

	// Several polls: the same error is only written once.
	time.Sleep(150 * time.Millisecond)

	if p := f.script.process(0); p == nil || p.stopped() {
		t.Fatal("the tunnel was stopped when the file could not be loaded")
	}
	written := f.stderr.String()
	if got := strings.Count(written, "tunnels: "); got != 1 {
		t.Fatalf("%d load errors written, want 1: %q", got, written)
	}
	if !strings.Contains(written, "fleet.toml") {
		t.Fatalf("stderr = %q, want the load error", written)
	}
}

// TestRunStopsEverySSHWhenTheContextEnds covers the shutdown: Run returns nil
// once every forward it made is stopped.
func TestRunStopsEverySSHWhenTheContextEnds(t *testing.T) {
	f := newFixture(t)
	f.writeFleet("box", "me@box", "rack", "me@rack")
	run := f.start()
	f.rec.wait(t, 0, hasState("box", contract.HostLive, ""))
	f.rec.wait(t, 0, hasState("rack", contract.HostLive, ""))
	if n := f.script.startCount(); n != 2 {
		t.Fatalf("Start was called %d times, want one per host", n)
	}

	run.cancel()
	select {
	case <-run.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if run.err != nil {
		t.Fatalf("Run = %v, want nil", run.err)
	}
	for i := 0; i < 2; i++ {
		if p := f.script.process(i); p == nil || !p.stopped() {
			t.Errorf("process %d was not stopped", i)
		}
	}
}

// script is Commands that answers what the test says, and remembers the calls.
type script struct {
	mu       sync.Mutex
	outputFn func(argv []string) ([]byte, error)
	startFn  func(argv []string) (Process, error)
	outputs  [][]string
	starts   [][]string
	times    []time.Time
	procs    []*fakeProcess
}

// Output is the remote status command.
func (s *script) Output(_ context.Context, argv []string) ([]byte, error) {
	s.mu.Lock()
	s.outputs = append(s.outputs, append([]string(nil), argv...))
	fn := s.outputFn
	s.mu.Unlock()
	if fn == nil {
		return nil, &CommandError{Code: -1}
	}
	return fn(argv)
}

// Start is the forward, and remembers every process it made.
func (s *script) Start(argv []string) (Process, error) {
	s.mu.Lock()
	s.starts = append(s.starts, append([]string(nil), argv...))
	s.times = append(s.times, time.Now())
	fn := s.startFn
	s.mu.Unlock()
	if fn == nil {
		return nil, &CommandError{Code: -1}
	}
	return fn(argv)
}

// addLive records and answers a process that runs until it is stopped.
func (s *script) addLive() *fakeProcess {
	p := newLiveProcess()
	s.mu.Lock()
	s.procs = append(s.procs, p)
	s.mu.Unlock()
	return p
}

// startCount is how many forwards were started.
func (s *script) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}

// startArgvs is every forward's argv, oldest first.
func (s *script) startArgvs() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.starts))
	copy(out, s.starts)
	return out
}

// startTimes is when each forward was started, oldest first.
func (s *script) startTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Time, len(s.times))
	copy(out, s.times)
	return out
}

// process is the nth started process, or nil.
func (s *script) process(i int) *fakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.procs) {
		return nil
	}
	return s.procs[i]
}

// fakeProcess is one started ssh -N.
type fakeProcess struct {
	done  chan struct{}
	err   error
	once  sync.Once
	stops atomic.Int32
}

// newLiveProcess is a process that runs until it is stopped.
func newLiveProcess() *fakeProcess {
	return &fakeProcess{done: make(chan struct{})}
}

// newExitedProcess is a process that has already exited with err.
func newExitedProcess(err error) *fakeProcess {
	p := &fakeProcess{done: make(chan struct{}), err: err}
	close(p.done)
	return p
}

// Done is closed once the process has exited.
func (p *fakeProcess) Done() <-chan struct{} { return p.done }

// Err is why the process exited, once it has.
func (p *fakeProcess) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

// Stop ends the process and counts how often it was asked to.
func (p *fakeProcess) Stop() {
	p.stops.Add(1)
	p.once.Do(func() { close(p.done) })
}

// stopped is whether Stop was called.
func (p *fakeProcess) stopped() bool { return p.stops.Load() > 0 }

// recorder is Report that keeps every set it was given.
type recorder struct {
	mu      sync.Mutex
	reports [][]contract.DaemonHost
}

// report records one set.
func (r *recorder) report(_ context.Context, hosts []contract.DaemonHost) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, append([]contract.DaemonHost(nil), hosts...))
	return nil
}

// count is how many reports have been made.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reports)
}

// at is report i, if it has been made.
func (r *recorder) at(i int) ([]contract.DaemonHost, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.reports) {
		return nil, false
	}
	return r.reports[i], true
}

// all is every report, oldest first.
func (r *recorder) all() [][]contract.DaemonHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]contract.DaemonHost, len(r.reports))
	copy(out, r.reports)
	return out
}

// wait waits for a report at or after from that matches, and answers its index.
func (r *recorder) wait(t *testing.T, from int, matches func([]contract.DaemonHost) bool) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		reports := make([][]contract.DaemonHost, len(r.reports))
		copy(reports, r.reports)
		r.mu.Unlock()
		for i := from; i < len(reports); i++ {
			if matches(reports[i]) {
				return i
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no report from %d on matched within 5s; reports = %v", from, r.all())
	return -1
}

// prober is Probe, answering what the test says and remembering the sockets.
type prober struct {
	mu     sync.Mutex
	fn     func(socket string) error
	calls  int
	socket []string
}

// probe asks the daemon at socket.
func (p *prober) probe(_ context.Context, socket string) error {
	p.mu.Lock()
	p.calls++
	p.socket = append(p.socket, socket)
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(socket)
}

// syncBuffer is an io.Writer a test can read while Run writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

// Write adds p to the buffer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String is what has been written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hostNamed is the host called name in hosts, if it is there.
func hostNamed(hosts []contract.DaemonHost, name string) (contract.DaemonHost, bool) {
	for _, h := range hosts {
		if h.Name == name {
			return h, true
		}
	}
	return contract.DaemonHost{}, false
}

// hasState matches a report whose host name is in state, with a last error
// holding lastErr when that is set.
func hasState(name string, state contract.HostState, lastErr string) func([]contract.DaemonHost) bool {
	return func(hosts []contract.DaemonHost) bool {
		h, ok := hostNamed(hosts, name)
		if !ok || h.State != state {
			return false
		}
		return lastErr == "" || strings.Contains(h.LastError, lastErr)
	}
}
