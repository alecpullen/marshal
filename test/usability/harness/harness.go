package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
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
		cmd:  cmd,
		pty:  ptmx,
		term: newTerminalModel(cfg.Width, cfg.Height),
	}
	go s.readLoop()
	return s, nil
}

func (s *Session) readLoop() {
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

// Close terminates the session and waits for the process.
func (s *Session) Close() error {
	_ = s.pty.Close()
	if s.cmd.Process != nil {
		// The background readLoop goroutine holds the PTY fd open; on macOS
		// this prevents pty.Close() from delivering EOF/SIGHUP to the child.
		// Kill the process explicitly before waiting so Close() does not
		// deadlock during PTY teardown.
		_ = s.cmd.Process.Kill()
	}
	return s.cmd.Wait()
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
