package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Bounded teardown deadlines. Teardown must not be able to hold the suite open,
// so Close reaps and drains with its own short limits instead of waiting on a
// child or a descriptor that may never come back.
const (
	closeReapTimeout = 5 * time.Second
	closeReadTimeout = 5 * time.Second
)

// Config configures a PTY session.
type Config struct {
	BinaryPath string
	Width      int
	Height     int
	WorkDir    string
	Env        []string
	HumanDelay time.Duration
}

// rawOutputCap bounds the raw PTY bytes retained per session. The tail is
// diagnostic only: snapshots come from the terminal model.
const rawOutputCap = 1 << 20

// Session wraps a running process inside a PTY.
type Session struct {
	cmd *exec.Cmd
	pty *os.File
	mu  sync.Mutex
	// writeMu serializes every PTY write so a paste's framing bytes and its
	// payload cannot interleave with ordinary keys. mu remains the state lock
	// for term and raw; the two are never merged.
	writeMu sync.Mutex
	// term decodes PTY output into the visible viewport and owns the session
	// dimensions; raw retains the last rawOutputCap bytes for diagnostics.
	// Both are guarded by mu.
	term *terminalModel
	raw  []byte

	// waitDone is closed by the single waiter goroutine once cmd.Wait has
	// returned; waitErr holds that result. waitErr has its own mutex because mu
	// is held per PTY chunk by the read loop and Wait must never queue behind it.
	waitDone  chan struct{}
	waitErrMu sync.Mutex
	waitErr   error
	// readDone is closed when the read loop returns, so teardown can confirm the
	// descriptor's reader has stopped.
	readDone chan struct{}
	// closeOnce makes teardown run exactly once; closeErr is the result every
	// Close call returns.
	closeOnce sync.Once
	closeErr  error
}

// New starts a process in a PTY and begins collecting output.
func New(cfg Config) (*Session, error) {
	if cfg.BinaryPath == "" {
		return nil, fmt.Errorf("BinaryPath is required")
	}
	if cfg.Width == 0 {
		cfg.Width = 80
	}
	if cfg.Height == 0 {
		cfg.Height = 24
	}

	cmd := exec.Command(cfg.BinaryPath)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cfg.Width), Rows: uint16(cfg.Height)})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	s := &Session{
		cmd:      cmd,
		pty:      ptmx,
		term:     newTerminalModel(cfg.Width, cfg.Height),
		waitDone: make(chan struct{}),
		readDone: make(chan struct{}),
	}
	// The waiter owns cmd.Wait for the session's whole life: Wait and Close both
	// observe its recorded result and never reap a second time.
	go s.waitLoop()
	go s.readLoop()
	return s, nil
}

// waitLoop is the single owner of cmd.Wait. It records the exit result once and
// closes waitDone so every Wait and Close observer shares it.
func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	s.waitErrMu.Lock()
	s.waitErr = err
	s.waitErrMu.Unlock()
	close(s.waitDone)
}

// waitResult returns the recorded cmd.Wait result. Callers read it under
// waitErrMu, or after waitDone is closed, which publishes the write.
func (s *Session) waitResult() error {
	s.waitErrMu.Lock()
	defer s.waitErrMu.Unlock()
	return s.waitErr
}

// Wait waits for the child process to exit without killing it or closing the
// PTY, and reports its exit status: nil for a clean exit. If ctx ends first it
// returns ctx.Err() and leaves the child running; Close owns cleanup. A child
// that has already exited reports its recorded status, so repeated Wait calls
// are safe and never reap twice.
func (s *Session) Wait(ctx context.Context) error {
	// Prefer the recorded exit when it is already available: a context that
	// expired after the child exited must not turn a normal exit into a
	// deadline error.
	select {
	case <-s.waitDone:
		return s.waitResult()
	default:
	}
	select {
	case <-s.waitDone:
		return s.waitResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) readLoop() {
	defer close(s.readDone)
	var tmp [4096]byte
	for {
		n, err := s.pty.Read(tmp[:])
		if n > 0 {
			s.mu.Lock()
			// Each chunk feeds the terminal model exactly once; the raw bytes
			// are retained only as a bounded diagnostic tail, never as the
			// source of a screen.
			_, _ = s.term.Write(tmp[:n])
			s.raw = append(s.raw, tmp[:n]...)
			if over := len(s.raw) - rawOutputCap; over > 0 {
				s.raw = append(s.raw[:0], s.raw[over:]...)
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// writeAll writes p to the PTY, looping until the buffer is drained and
// propagating any write error. Callers must hold s.writeMu.
func (s *Session) writeAll(p []byte) error {
	for len(p) > 0 {
		n, err := s.pty.Write(p)
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Send writes text to the PTY with an optional human-like delay.
func (s *Session) Send(text string) error {
	delay := s.humanDelayPerChar()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, r := range text {
		if err := s.writeAll([]byte(string(r))); err != nil {
			return err
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return nil
}

// Paste delivers text as bracketed paste: the start marker, the payload
// unchanged, and the end marker, in order. It never submits Enter. Writes are
// serialized against Send and SendKey so ordinary input cannot interleave
// inside the framing bytes.
func (s *Session) Paste(text string) error {
	const (
		start = "\x1b[200~"
		end   = "\x1b[201~"
	)
	buf := make([]byte, 0, len(start)+len(text)+len(end))
	buf = append(buf, start...)
	buf = append(buf, text...)
	buf = append(buf, end...)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeAll(buf)
}

// SendKey writes a named key to the PTY.
func (s *Session) SendKey(key string) error {
	var seq []byte
	switch strings.ToLower(key) {
	case "enter", "return":
		seq = []byte("\r")
	case "esc", "escape":
		seq = []byte("\x1b")
	case "tab":
		seq = []byte("\t")
	case "space":
		seq = []byte(" ")
	case "backspace":
		seq = []byte("\x7f")
	case "ctrl+c":
		seq = []byte("\x03")
	case "ctrl+d":
		seq = []byte("\x04")
	case "ctrl+o":
		seq = []byte("\x0f")
	case "ctrl+k":
		seq = []byte("\x0b")
	case "ctrl+g":
		seq = []byte("\x07")
	case "ctrl+r":
		seq = []byte("\x12")
	case "ctrl+x":
		seq = []byte("\x18")
	case "up":
		seq = []byte("\x1b[A")
	case "down":
		seq = []byte("\x1b[B")
	case "right":
		seq = []byte("\x1b[C")
	case "left":
		seq = []byte("\x1b[D")
	case "pgup":
		seq = []byte("\x1b[5~")
	case "pgdown":
		seq = []byte("\x1b[6~")
	case "home":
		seq = []byte("\x1b[H")
	case "end":
		seq = []byte("\x1b[F")
	default:
		return fmt.Errorf("unknown key: %q", key)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeAll(seq)
}

// Resize changes the PTY window and the viewport model together. Dimensions
// outside 1..65535 are rejected before any conversion, leaving both the PTY
// and the model untouched. The ioctl runs under mu so the read loop cannot
// feed the model between the two updates; on ioctl failure the model keeps
// its previous dimensions.
func (s *Session) Resize(width, height int) error {
	if width < 1 || width > 65535 || height < 1 || height > 65535 {
		return fmt.Errorf("resize to %dx%d: width and height must be in 1..65535", width, height)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
		return fmt.Errorf("resize pty to %dx%d: %w", width, height, err)
	}
	return s.term.Resize(width, height)
}

// Snapshot returns the current visible viewport.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.term.Snapshot()
}

// Output returns a copy of the raw PTY tail retained for diagnostics.
func (s *Session) Output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.raw))
	copy(out, s.raw)
	return out
}

// WaitFor polls until predicate holds for two consecutive snapshots.
func (s *Session) WaitFor(ctx context.Context, pred func(Snapshot) bool) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	stable := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if pred(s.Snapshot()) {
				stable++
				if stable >= 2 {
					return nil
				}
			} else {
				stable = 0
			}
		}
	}
}

// Close releases the session exactly once. A child that is still running is
// force-killed; teardown is bounded so a wedged child or a stuck descriptor
// cannot hold the caller open. Repeated and concurrent calls are safe and
// return the same result. The retained output and snapshot stay readable
// afterwards, so a caller can still inspect the final screen.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.teardown()
	})
	return s.closeErr
}

// teardown forces the child down if it is still running and releases the PTY,
// then reaps through the session's single waiter.
func (s *Session) teardown() error {
	// (1) A child that has not exited is force-killed. Killing an already-exited
	// child is the expected state after a graceful Wait, not a new failure.
	killErr := forceKill(s.cmd.Process)

	// (2) Release the descriptor before waiting on the reader: a pty.Read blocked
	// on a still-open PTY would otherwise pin teardown forever.
	_ = s.pty.Close()

	// (3) Reap through the central waiter, which has been waiting since startup,
	// so cmd.Wait runs exactly once. The deadline keeps a wedged child from
	// hanging the suite.
	select {
	case <-s.waitDone:
	case <-time.After(closeReapTimeout):
		if killErr != nil {
			return fmt.Errorf("close: kill: %v; child did not exit within %s", killErr, closeReapTimeout)
		}
		return fmt.Errorf("close: child did not exit within %s of the kill", closeReapTimeout)
	}

	// (4) Let the read loop drain the descriptor it just lost.
	select {
	case <-s.readDone:
	case <-time.After(closeReadTimeout):
		return fmt.Errorf("close: pty reader did not stop within %s of closing the pty", closeReadTimeout)
	}

	// term and raw are deliberately left in place: the final snapshot and output
	// remain readable after cleanup.
	return s.waitResult()
}

// forceKill terminates p and reports a Kill failure other than "already done".
// An already-finished process reports os.ErrProcessDone, which is the expected
// state after a graceful exit rather than a failure. Any other Kill error is
// returned for the caller's diagnostic; teardown still releases the PTY and the
// reader, and the recorded wait result reports how the child actually ended.
func forceKill(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (s *Session) humanDelayPerChar() time.Duration {
	// no delay by default
	return 0
}

// Snapshot is a captured screen state.
type Snapshot struct {
	Width   int
	Height  int
	Content []byte
	Lines   []string
	// Err reports a sticky terminal decoding or unsupported screen-operation
	// error from the terminal model. A nil Err means the screen decoded
	// cleanly.
	Err error
}
