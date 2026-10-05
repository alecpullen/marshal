package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// maxTerminalsPerOwner caps concurrent terminals per agent.
	maxTerminalsPerOwner = 2
	// maxTerminalInput is the largest single input body, decoded.
	maxTerminalInput = 64 << 10
	// terminalChunk is the largest output chunk streamed in one event.
	terminalChunk = 32 << 10
	// terminalIdleRelease is how long a held agent waits without input
	// before the terminal hands it back.
	terminalIdleRelease = 2 * time.Minute
	// terminalIdleTick is how often idle terminals are checked.
	terminalIdleTick = 15 * time.Second
	// terminalHoldTimeout bounds one hold call to the agent.
	terminalHoldTimeout = 10 * time.Second
	// terminalHoldBackoff is how long typing waits before trying a failed
	// hold again.
	terminalHoldBackoff = 30 * time.Second
)

// defaultReleaseRetryDelays are the waits before each further attempt to hand an
// agent back after the final release failed, so an agent that was briefly
// unreachable is not left held.
var defaultReleaseRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// ErrTooManyTerminals is returned when an owner already has the maximum
// number of open terminals. It maps to 429.
var ErrTooManyTerminals = errors.New("bridge: too many terminals open for this agent")

// ErrUnknownTerminal is returned for a terminal id that is not open.
var ErrUnknownTerminal = errors.New("bridge: unknown terminal")

// errTerminalInput is a malformed input or resize body.
var errTerminalInput = errors.New("bridge: invalid terminal input")

// terminalKeyPrefix namespaces terminal output in the terminal event log.
const terminalKeyPrefix = "term:"

// terminal is one open shell: a streaming runtime process, its recording,
// and the hold it keeps on its agent while someone types.
type terminal struct {
	id string
	// owner is the agent id, or "workspace:<name>" for a test shell.
	owner string
	// agentID is set only for agent terminals. A test shell has no agent
	// to hold.
	agentID string
	key     string
	proc    streamProc
	done    chan struct{}

	// resize applies a size to the shell's PTY from outside the stream.
	resize func(rows, cols int) error

	// holdMu serializes session/hold calls for this terminal so on and off
	// reach the agent in the order they were wanted.
	holdMu sync.Mutex

	mu  sync.Mutex
	log *os.File
	// want is the hold state the terminal is after; held is what the agent
	// last acknowledged. syncHold drives held toward want.
	want, held bool
	// holdUnsupported is set once the agent answers hold with 501; holdRetryAt
	// backs off after any other failure.
	holdUnsupported bool
	holdRetryAt     time.Time
	lastInput       time.Time
	bytesIn         int64
	bytesOut        int64
	once            sync.Once
	// rmTTY removes the PTY record file the wrapper left behind. It is
	// nil for a test shell, whose container is thrown away.
	rmTTY func()
	// cleanup runs once after the process ends, for a test shell's
	// container and egress registration.
	cleanup func()
}

// terminalState is the fleet's terminal registry, created on first use.
type terminalState struct {
	mu   sync.Mutex
	byID map[string][]*terminal // owner -> open terminals
	// unreleased holds agents whose last terminal closed without the agent
	// acknowledging the hand-back, so the agent is still held.
	unreleased map[string]bool
	log        *EventLog
}

func (f *Fleet) terminals() *terminalState {
	f.termOnce.Do(func() {
		f.term = &terminalState{byID: make(map[string][]*terminal), unreleased: make(map[string]bool), log: NewEventLog()}
		if f.done != nil {
			go f.idleTerminalLoop()
		}
	})
	return f.term
}

// TerminalLog is the event log carrying terminal output, keyed
// "term:<terminalId>".
func (f *Fleet) TerminalLog() *EventLog { return f.terminals().log }

func (f *Fleet) idleTerminalLoop() {
	tick := time.NewTicker(terminalIdleTick)
	defer tick.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-tick.C:
			f.releaseIdleTerminals(f.now())
		}
	}
}

// releaseIdleTerminals hands back every held agent whose terminal has had
// no input for terminalIdleRelease.
func (f *Fleet) releaseIdleTerminals(now time.Time) {
	st := f.terminals()
	st.mu.Lock()
	var idle []*terminal
	for _, ts := range st.byID {
		for _, t := range ts {
			t.mu.Lock()
			if (t.held || t.want) && now.Sub(t.lastInput) >= terminalIdleRelease {
				idle = append(idle, t)
			}
			t.mu.Unlock()
		}
	}
	st.mu.Unlock()
	for _, t := range idle {
		f.releaseTerminalHold(t)
	}
}

func newTerminalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func clampTerm(v, def int) int {
	if v <= 0 {
		return def
	}
	if v > 500 {
		return 500
	}
	return v
}

// terminalScript is the shell command `script` runs inside the PTY it
// allocates. cols and rows are integers, so nothing here is injectable.
func terminalScript(cols, rows int, shell, tid string) string {
	return fmt.Sprintf("tty > %s; stty rows %d cols %d; exec %s", ttyFile(tid), rows, cols, shell)
}

// ttyFile is where the shell's wrapper records its PTY device, so a resize
// can address the PTY from a separate process.
func ttyFile(tid string) string { return "/tmp/.marshal-tty-" + tid }

// resizeCommand is the shell command that sets the PTY's size. stty on the
// device changes the kernel's window size, which signals the foreground
// process group (SIGWINCH) instead of typing into it.
func resizeCommand(tid string, rows, cols int) string {
	return fmt.Sprintf(`stty -F "$(cat %s)" rows %d cols %d`, ttyFile(tid), rows, cols)
}

// runStream runs a short command to completion through the streamer.
func (f *Fleet) runStream(dir, bin string, args ...string) error {
	p, err := f.startStream(dir, bin, args...)
	if err != nil {
		return err
	}
	_ = p.Stdin().Close()
	done := make(chan error, 1)
	go func() {
		_, _ = io.Copy(io.Discard, p.Stdout())
		done <- p.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		p.Kill()
		return errors.New("bridge: terminal resize timed out")
	}
}

// reserve claims a terminal slot for owner and registers t. It fails with
// ErrTooManyTerminals before anything is started.
func (st *terminalState) reserve(owner string, t *terminal) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.byID[owner]) >= maxTerminalsPerOwner {
		return ErrTooManyTerminals
	}
	st.byID[owner] = append(st.byID[owner], t)
	return nil
}

func (st *terminalState) remove(t *terminal) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ts := st.byID[t.owner]
	for i, x := range ts {
		if x == t {
			ts = append(ts[:i:i], ts[i+1:]...)
			break
		}
	}
	if len(ts) == 0 {
		delete(st.byID, t.owner)
	} else {
		st.byID[t.owner] = ts
	}
}

func (st *terminalState) get(owner, tid string) (*terminal, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, t := range st.byID[owner] {
		if t.id == tid {
			return t, nil
		}
	}
	return nil, ErrUnknownTerminal
}

// openLog creates the recording file <state>/terminal/<owner>/<unix>.log
// with mode 0600.
func (f *Fleet) openTerminalLog(owner string) (*os.File, error) {
	dir := filepath.Join(f.stateDir, "terminal", strings.ReplaceAll(owner, ":", "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create terminal dir: %w", err)
	}
	unix := f.now().Unix()
	for n := 0; n < 100; n++ {
		name := fmt.Sprintf("%d.log", unix)
		if n > 0 {
			name = fmt.Sprintf("%d-%d.log", unix, n)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create terminal log: %w", err)
		}
	}
	return nil, errors.New("bridge: could not create a terminal log")
}

// OpenTerminal opens a shell in an agent's container (or, in process
// mode, in its active root) and returns the terminal id.
func (f *Fleet) OpenTerminal(ctx context.Context, agentID string, cols, rows int) (string, error) {
	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		return "", err
	}
	cols, rows = clampTerm(cols, 80), clampTerm(rows, 24)
	t := &terminal{id: newTerminalID(), owner: agentID, agentID: agentID, done: make(chan struct{})}
	t.key = terminalKeyPrefix + t.id
	script := terminalScript(cols, rows, "${SHELL:-sh}", t.id)

	var dir, bin string
	var args []string
	if rt.child != nil && rt.child.Containerized {
		path, _, ok := f.runtimeInfo()
		if !ok {
			return "", ErrWorkspaceNeedsRuntime
		}
		name := containerNameFor(agentID)
		if a, ok := f.ws.Agent(agentID); ok && a.ContainerName != "" {
			name = a.ContainerName
		}
		bin = path
		args = []string{"exec", "-i", "-w", "/work", "-e", "TERM=xterm-256color", name, "script", "-qfc", script, "/dev/null"}
		t.resize = func(rows, cols int) error {
			return f.runStream("", path, "exec", name, "sh", "-c", resizeCommand(t.id, rows, cols))
		}
		t.rmTTY = func() { _ = f.runStream("", path, "exec", name, "rm", "-f", ttyFile(t.id)) }
	} else {
		root, err := f.agentActiveRoot(ctx, agentID)
		if err != nil {
			return "", err
		}
		bin, dir = "script", root
		args = []string{"-qfc", script, "/dev/null"}
		t.resize = func(rows, cols int) error {
			return f.runStream(root, "sh", "-c", resizeCommand(t.id, rows, cols))
		}
		t.rmTTY = func() { _ = os.Remove(ttyFile(t.id)) }
	}
	st := f.terminals()
	if err := st.reserve(agentID, t); err != nil {
		return "", err
	}
	if err := f.startTerminal(t, dir, bin, args); err != nil {
		st.remove(t)
		return "", err
	}
	f.auditf(AuditEvent{Event: AuditTerminalOpened, AgentID: agentID, Detail: t.id})
	return t.id, nil
}

// agentActiveRoot is the root `session/files` reports for an empty path.
func (f *Fleet) agentActiveRoot(ctx context.Context, agentID string) (string, error) {
	raw, err := f.Files(ctx, agentID, "")
	if err != nil {
		return "", err
	}
	var out struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Root == "" {
		return "", errors.New("bridge: the agent did not report its root")
	}
	return out.Root, nil
}

// startTerminal starts the process, opens the recording and begins
// streaming output.
func (f *Fleet) startTerminal(t *terminal, dir, bin string, args []string) error {
	logFile, err := f.openTerminalLog(t.owner)
	if err != nil {
		return err
	}
	proc, err := f.startStream(dir, bin, args...)
	if err != nil {
		logFile.Close()
		return fmt.Errorf("start terminal: %w", err)
	}
	t.proc, t.log = proc, logFile
	t.lastInput = f.now()
	go f.pumpTerminal(t)
	return nil
}

// pumpTerminal streams the process's output to the event log and the
// recording, then reports the exit and tears the terminal down.
func (f *Fleet) pumpTerminal(t *terminal) {
	defer close(t.done)
	log := f.terminals().log
	buf := make([]byte, terminalChunk)
	var first bytes.Buffer
	for {
		n, err := t.proc.Stdout().Read(buf)
		if n > 0 {
			chunk := buf[:n]
			t.mu.Lock()
			t.bytesOut += int64(n)
			if t.log != nil {
				_, _ = t.log.Write(chunk)
			}
			t.mu.Unlock()
			if first.Len() < 512 {
				first.Write(chunk)
			}
			_, _ = log.Append(t.key, map[string]any{"data": base64.StdEncoding.EncodeToString(chunk)})
		}
		if err != nil {
			break
		}
	}
	code := exitCodeOf(t.proc.Wait())
	ev := map[string]any{"exit": code}
	if t.bytesOutLoad() < 512 {
		if msg := scriptMissing(first.String()); msg != "" {
			ev["error"] = msg
		}
	}
	_, _ = log.Append(t.key, ev)
	f.finishTerminal(t)
}

func (t *terminal) bytesOutLoad() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytesOut
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// scriptMissing recognises the shell's complaint when the image has no
// `script` binary.
func scriptMissing(out string) string {
	l := strings.ToLower(out)
	if strings.Contains(l, "script") && (strings.Contains(l, "not found") || strings.Contains(l, "no such file")) {
		return "script not found in image"
	}
	return ""
}

// finishTerminal runs once per terminal: it hands the agent back, closes
// the recording, drops the terminal and audits the close.
func (f *Fleet) finishTerminal(t *terminal) {
	t.once.Do(func() {
		f.releaseTerminalHold(t)
		if in := t.proc.Stdin(); in != nil {
			_ = in.Close()
		}
		t.proc.Kill()
		if t.rmTTY != nil {
			go t.rmTTY() // off the close path: a hung exec must not stall it
		}
		t.mu.Lock()
		if t.log != nil {
			_ = t.log.Close()
		}
		in, out := t.bytesIn, t.bytesOut
		t.mu.Unlock()
		st := f.terminals()
		st.remove(t)
		st.log.Forget(t.key)
		if t.cleanup != nil {
			t.cleanup()
		}
		f.auditf(AuditEvent{Event: AuditTerminalClosed, AgentID: t.agentID, Detail: fmt.Sprintf("in=%d out=%d", in, out)})
	})
}

// closeTerminals ends every terminal an owner has open.
func (f *Fleet) closeTerminals(owner string) {
	st := f.terminals()
	st.mu.Lock()
	ts := append([]*terminal(nil), st.byID[owner]...)
	st.mu.Unlock()
	for _, t := range ts {
		f.killTerminal(t)
	}
}

// killTerminal closes stdin, kills the process and waits briefly for the
// output pump to report the exit.
func (f *Fleet) killTerminal(t *terminal) {
	if in := t.proc.Stdin(); in != nil {
		_ = in.Close()
	}
	t.proc.Kill()
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
		f.finishTerminal(t)
	}
}

// callHold sends one session/hold and broadcasts the change. unsupported
// is true when the agent has no such method.
func (f *Fleet) callHold(t *terminal, on bool) (ok, unsupported bool) {
	ctx, cancel := context.WithTimeout(context.Background(), terminalHoldTimeout)
	defer cancel()
	var err error
	if f.holdCall != nil {
		err = f.holdCall(ctx, t.agentID, on)
	} else {
		_, err = f.agentCall(ctx, t.agentID, "session/hold", "hold", map[string]any{"on": on})
	}
	if err != nil {
		var un ErrUnsupported
		unsupported = errors.As(err, &un)
		if !unsupported {
			slog.Default().Warn("webbridge: terminal hold failed", "agent", t.agentID, "on", on, "err", err)
		}
		return false, unsupported
	}
	f.emit(fleetDelta{
		Kind: "hold", SessionID: t.agentID, AgentID: t.agentID, Held: &on, By: "terminal",
	})
	return true, false
}

// syncHold drives the agent's hold toward the terminal's wanted state, one
// call at a time. A call that was wanted while another was in flight is
// made after it, and a stale one is skipped because want is re-read each
// round. A failure gives up for this attempt; unsupported is remembered for
// good and other failures back off, so typing never retries per keystroke.
func (f *Fleet) syncHold(t *terminal) {
	if t.agentID == "" {
		return
	}
	t.holdMu.Lock()
	defer t.holdMu.Unlock()
	for {
		t.mu.Lock()
		want, held, unsupported := t.want, t.held, t.holdUnsupported
		t.mu.Unlock()
		if want == held || (want && unsupported) {
			return
		}
		ok, unsup := f.callHold(t, want)
		t.mu.Lock()
		switch {
		case ok:
			t.held = want
		default:
			if unsup {
				t.holdUnsupported = true
			}
			t.holdRetryAt = f.now().Add(terminalHoldBackoff)
			if want {
				t.want = false // not held; the next try waits out the backoff
			}
		}
		t.mu.Unlock()
		if !ok {
			return
		}
	}
}

// releaseTerminalHold hands the agent back if the terminal wants or holds
// it, and waits for that to be acknowledged.
func (f *Fleet) releaseTerminalHold(t *terminal) {
	t.mu.Lock()
	t.want = false
	t.mu.Unlock()
	f.syncHold(t)
	t.mu.Lock()
	stillHeld := t.held && !t.holdUnsupported
	t.mu.Unlock()
	if stillHeld {
		st := f.terminals()
		st.mu.Lock()
		st.unreleased[t.agentID] = true
		st.mu.Unlock()
		go f.retryRelease(t)
	}
}

// agentHeld reports whether a terminal is holding the agent: one holds it
// now, or a closed one's hand-back has not gone through yet.
func (f *Fleet) agentHeld(agentID string) bool {
	st := f.terminals()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.unreleased[agentID] {
		return true
	}
	for _, t := range st.byID[agentID] {
		t.mu.Lock()
		held := t.held
		t.mu.Unlock()
		if held {
			return true
		}
	}
	return false
}

func (f *Fleet) clearUnreleased(agentID string) {
	st := f.terminals()
	st.mu.Lock()
	delete(st.unreleased, agentID)
	st.mu.Unlock()
}

// retryRelease keeps trying to hand the agent back after a failed release,
// with growing waits. It stops when the agent is handed back, is gone, or
// has another terminal open (that terminal inherits the hold and hands it
// back), or when the fleet closes.
func (f *Fleet) retryRelease(t *terminal) {
	delays := f.releaseRetry
	if delays == nil {
		delays = defaultReleaseRetryDelays
	}
	for _, d := range delays {
		select {
		case <-time.After(d):
		case <-f.done:
			return
		}
		if _, err := f.runtimeForAgent(t.agentID); err != nil {
			f.clearUnreleased(t.agentID)
			return
		}
		st := f.terminals()
		st.mu.Lock()
		others := append([]*terminal(nil), st.byID[t.agentID]...)
		st.mu.Unlock()
		if len(others) > 0 {
			// The agent is still held. Hand that hold to the open
			// terminals, so release, close or the idle timer sends the
			// hand-back even if nobody types again.
			for _, o := range others {
				o.mu.Lock()
				o.want, o.held = true, true
				o.mu.Unlock()
			}
			f.clearUnreleased(t.agentID)
			return
		}
		f.syncHold(t)
		t.mu.Lock()
		held := t.held
		t.mu.Unlock()
		if !held {
			f.clearUnreleased(t.agentID)
			return
		}
	}
	slog.Default().Warn("webbridge: could not hand the agent back after a terminal closed", "agent", t.agentID)
}

func (f *Fleet) terminalFor(owner, tid string) (*terminal, error) {
	return f.terminals().get(owner, tid)
}

// TerminalInput writes decoded keystrokes to the terminal. The first byte
// after a release holds the agent.
func (f *Fleet) TerminalInput(owner, tid string, data []byte) error {
	if len(data) > maxTerminalInput {
		return fmt.Errorf("%w: input over %d bytes", errTerminalInput, maxTerminalInput)
	}
	t, err := f.terminalFor(owner, tid)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	// The first byte after a hand back holds the agent, in the background:
	// a slow or hung agent must not delay the keystroke.
	t.mu.Lock()
	startHold := t.agentID != "" && !t.want && !t.holdUnsupported && !f.now().Before(t.holdRetryAt)
	if startHold {
		t.want = true
	}
	t.mu.Unlock()
	if startHold {
		go f.syncHold(t)
	}
	if _, err := t.proc.Stdin().Write(data); err != nil {
		return err
	}
	t.mu.Lock()
	t.lastInput = f.now()
	t.bytesIn += int64(len(data))
	if t.log != nil {
		_, _ = t.log.Write(data)
	}
	t.mu.Unlock()
	return nil
}

// ResizeTerminal sets the PTY's size with stty run against the PTY device
// from a separate process, so the foreground program gets SIGWINCH and
// nothing is typed into it.
func (f *Fleet) ResizeTerminal(owner, tid string, cols, rows int) error {
	if cols < 1 || cols > 500 || rows < 1 || rows > 500 {
		return fmt.Errorf("%w: size out of range", errTerminalInput)
	}
	t, err := f.terminalFor(owner, tid)
	if err != nil {
		return err
	}
	if t.resize == nil {
		return errors.New("bridge: this terminal cannot be resized")
	}
	return t.resize(rows, cols)
}

// ReleaseTerminal hands the agent back without closing the shell.
func (f *Fleet) ReleaseTerminal(owner, tid string) error {
	t, err := f.terminalFor(owner, tid)
	if err != nil {
		return err
	}
	f.releaseTerminalHold(t)
	return nil
}

// CloseTerminal ends one terminal.
func (f *Fleet) CloseTerminal(owner, tid string) error {
	t, err := f.terminalFor(owner, tid)
	if err != nil {
		return err
	}
	f.killTerminal(t)
	return nil
}

// ---- HTTP ----

type terminalSizeReq struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *Server) terminalOpen(w http.ResponseWriter, r *http.Request) {
	var req terminalSizeReq
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	tid, err := s.fleet.OpenTerminal(r.Context(), r.PathValue("id"), req.Cols, req.Rows)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"terminalId": tid})
}

func (s *Server) terminalEvents(w http.ResponseWriter, r *http.Request) {
	s.serveTerminalEvents(w, r, r.PathValue("id"))
}

func (s *Server) serveTerminalEvents(w http.ResponseWriter, r *http.Request, owner string) {
	tid := r.PathValue("tid")
	if _, err := s.fleet.terminalFor(owner, tid); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.TerminalLog().ServeSSEKey(w, r, terminalKeyPrefix+tid)
}

func (s *Server) terminalInput(w http.ResponseWriter, r *http.Request) {
	s.terminalInputFor(w, r, r.PathValue("id"))
}

func (s *Server) terminalInputFor(w http.ResponseWriter, r *http.Request, owner string) {
	var req struct {
		Data string `json:"data"`
	}
	// base64 expands by a third; leave room for the JSON envelope.
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxTerminalInput)*4/3+1024)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data must be base64"})
		return
	}
	if err := s.fleet.TerminalInput(owner, r.PathValue("tid"), data); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) terminalResize(w http.ResponseWriter, r *http.Request) {
	s.terminalResizeFor(w, r, r.PathValue("id"))
}

func (s *Server) terminalResizeFor(w http.ResponseWriter, r *http.Request, owner string) {
	var req terminalSizeReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.fleet.ResizeTerminal(owner, r.PathValue("tid"), req.Cols, req.Rows); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) terminalRelease(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.ReleaseTerminal(r.PathValue("id"), r.PathValue("tid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) terminalClose(w http.ResponseWriter, r *http.Request) {
	s.terminalCloseFor(w, r, r.PathValue("id"))
}

func (s *Server) terminalCloseFor(w http.ResponseWriter, r *http.Request, owner string) {
	if err := s.fleet.CloseTerminal(owner, r.PathValue("tid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// closeAllTerminals ends every open terminal as the bridge shuts down.
func (f *Fleet) closeAllTerminals() {
	st := f.terminals()
	st.mu.Lock()
	var all []*terminal
	for _, ts := range st.byID {
		all = append(all, ts...)
	}
	st.mu.Unlock()
	for _, t := range all {
		f.killTerminal(t)
	}
}

// ---- Workspace test shell ----

const shellContainerPrefix = "marshal-shell-"

// workspaceOwner is the terminal owner of a workspace's test shell.
func workspaceOwner(name string) string { return "workspace:" + name }

// OpenWorkspaceShell starts a throwaway container of a built Studio
// template's image, with its mounts and environment, and attaches a
// terminal to it. There is no agent, so nothing is held. The container is
// removed when the shell ends.
func (f *Fleet) OpenWorkspaceShell(ctx context.Context, name string, cols, rows int) (string, error) {
	ref, err := ParseWSRef(name)
	if err != nil {
		return "", err
	}
	if ref.Source != "studio" {
		return "", fmt.Errorf("%w: a test shell needs a Studio template", ErrWorkspaceNotBuilt)
	}
	res, err := f.ResolveWorkspace(ctx, ref, "")
	if err != nil {
		return "", err
	}
	image, err := f.builtImage(res)
	if err != nil {
		return "", err
	}
	rtPath, rtName, ok := f.runtimeInfo()
	if !ok {
		return "", ErrWorkspaceNeedsRuntime
	}
	cols, rows = clampTerm(cols, 80), clampTerm(rows, 24)
	t := &terminal{id: newTerminalID(), owner: workspaceOwner(res.Name), done: make(chan struct{})}
	t.key = terminalKeyPrefix + t.id

	rnd := newTerminalID()
	shellID := "shell-" + rnd
	container := shellContainerPrefix + rnd
	pseudo := Agent{
		ID: shellID, Profile: DefaultRuntimeProfile(),
		Workspace: &AgentWorkspace{Name: res.Name, Version: res.Version, Source: res.Source},
	}
	extras, err := f.buildWorkspaceExtras(ctx, pseudo, res.Doc, res.ImageName, rtName)
	if err != nil {
		return "", err
	}
	wiring, proxied, err := f.egressPrepare(ctx, pseudo)
	if err != nil {
		return "", err
	}
	unregister := func() {
		if proxied && f.egress != nil {
			f.egress.remove(shellID)
		}
	}

	args := []string{"run", "--rm", "-i", "--name", container}
	if wiring.Network != "" {
		args = append(args, "--network", wiring.Network)
	}
	args = append(args, extras.mounts...)
	for _, v := range wiring.Volumes {
		args = append(args, volumeMount(rtName, f.stateVolume, v.Target, v.Subpath, v.ReadOnly)...)
	}
	env := map[string]string{"TERM": "xterm-256color"}
	for k, v := range extras.env {
		env[k] = v
	}
	for k, v := range wiring.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, image, "script", "-qfc", terminalScript(cols, rows, "sh", t.id), "/dev/null")

	owner := t.owner
	t.resize = func(rows, cols int) error {
		return f.runStream("", rtPath, "exec", container, "sh", "-c", resizeCommand(t.id, rows, cols))
	}
	t.cleanup = func() {
		unregister()
		// Closing stdin ends the shell and --rm removes the container; this
		// is the backstop for a client killed before the shell saw EOF.
		_, _ = f.runRuntime(rtName, "rm", "-f", container)
	}
	st := f.terminals()
	if err := st.reserve(owner, t); err != nil {
		unregister()
		return "", err
	}
	if err := f.startTerminal(t, "", rtPath, args); err != nil {
		st.remove(t)
		unregister()
		return "", err
	}
	f.auditf(AuditEvent{Event: AuditTerminalOpened, Detail: "workspace:" + res.Name + " " + t.id})
	return t.id, nil
}

func (s *Server) workspaceShellOpen(w http.ResponseWriter, r *http.Request) {
	var req terminalSizeReq
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	tid, err := s.fleet.OpenWorkspaceShell(r.Context(), r.PathValue("name"), req.Cols, req.Rows)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"terminalId": tid})
}

func (s *Server) workspaceShellEvents(w http.ResponseWriter, r *http.Request) {
	s.serveTerminalEvents(w, r, workspaceOwner(r.PathValue("name")))
}

func (s *Server) workspaceShellInput(w http.ResponseWriter, r *http.Request) {
	s.terminalInputFor(w, r, workspaceOwner(r.PathValue("name")))
}

func (s *Server) workspaceShellResize(w http.ResponseWriter, r *http.Request) {
	s.terminalResizeFor(w, r, workspaceOwner(r.PathValue("name")))
}

func (s *Server) workspaceShellClose(w http.ResponseWriter, r *http.Request) {
	s.terminalCloseFor(w, r, workspaceOwner(r.PathValue("name")))
}
