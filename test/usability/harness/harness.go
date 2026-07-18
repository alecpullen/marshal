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

// Session wraps a running process inside a PTY.
type Session struct {
	cmd    *exec.Cmd
	pty    *os.File
	mu     sync.Mutex
	buf    []byte
	width  int
	height int
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
		cmd:    cmd,
		pty:    ptmx,
		width:  cfg.Width,
		height: cfg.Height,
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
			s.buf = append(s.buf, tmp[:n]...)
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Send writes text to the PTY with an optional human-like delay.
func (s *Session) Send(text string) error {
	delay := s.humanDelayPerChar()
	for _, r := range text {
		if _, err := s.pty.Write([]byte(string(r))); err != nil {
			return err
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return nil
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
	_, err := s.pty.Write(seq)
	return err
}

// Snapshot returns the current terminal contents.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	content := make([]byte, len(s.buf))
	copy(content, s.buf)
	s.mu.Unlock()

	lines := strings.Split(string(content), "\n")
	return Snapshot{Width: s.width, Height: s.height, Content: content, Lines: lines}
}

// Output returns all raw output bytes collected so far.
func (s *Session) Output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
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
}
