package bridge

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStream is an in-memory streamProc: whatever is written to stdin is
// echoed to stdout, and out lets a test inject more output.
type fakeStream struct {
	mu     sync.Mutex
	in     bytes.Buffer
	inW    *fakeStdin
	outR   *io.PipeReader
	outW   *io.PipeWriter
	killed bool
	done   chan struct{}
	echo   bool
}

type fakeStdin struct{ s *fakeStream }

func (w *fakeStdin) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	w.s.in.Write(p)
	echo := w.s.echo
	w.s.mu.Unlock()
	if echo {
		if _, err := w.s.outW.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *fakeStdin) Close() error { return nil }

func newFakeStream(echo bool) *fakeStream {
	pr, pw := io.Pipe()
	s := &fakeStream{outR: pr, outW: pw, done: make(chan struct{}), echo: echo}
	s.inW = &fakeStdin{s}
	return s
}

func (s *fakeStream) Stdin() io.WriteCloser { return s.inW }
func (s *fakeStream) Stdout() io.Reader     { return s.outR }
func (s *fakeStream) Wait() error           { <-s.done; return nil }
func (s *fakeStream) Kill() {
	s.mu.Lock()
	if s.killed {
		s.mu.Unlock()
		return
	}
	s.killed = true
	s.mu.Unlock()
	s.outW.Close()
	close(s.done)
}

func (s *fakeStream) written() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.in.String()
}

func (s *fakeStream) wasKilled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

// emit injects output as if the process had printed it.
func (s *fakeStream) emit(data string) { _, _ = s.outW.Write([]byte(data)) }

// exit ends the process the way a shell exiting would.
func (s *fakeStream) exit() { s.Kill() }

// streamCall is one recorded streamer invocation.
type streamCall struct {
	dir  string
	name string
	args []string
}

// fakeStreamer records starts and hands out fake processes.
type fakeStreamer struct {
	mu    sync.Mutex
	calls []streamCall
	// shorts are run-to-completion commands (a resize's `sh -c`), which exit
	// at once and are kept apart from the terminals' own processes.
	shorts []streamCall
	procs  []*fakeStream
	echo   bool
	err    error
}

func (fs *fakeStreamer) start(dir, name string, args ...string) (streamProc, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.err != nil {
		return nil, fs.err
	}
	if isShortCommand(name, args) {
		p := newFakeStream(false)
		p.exit()
		fs.shorts = append(fs.shorts, streamCall{dir, name, append([]string(nil), args...)})
		return p, nil
	}
	p := newFakeStream(fs.echo)
	fs.calls = append(fs.calls, streamCall{dir, name, append([]string(nil), args...)})
	fs.procs = append(fs.procs, p)
	return p, nil
}

func (fs *fakeStreamer) last() (*fakeStream, streamCall) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.procs[len(fs.procs)-1], fs.calls[len(fs.calls)-1]
}

func TestExecStreamFakeEchoesInput(t *testing.T) {
	fs := &fakeStreamer{echo: true}
	f := &Fleet{streamer: fs.start}
	p, err := f.startStream("", "docker", "exec")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = p.Stdin().Write([]byte("ls\n")) }()
	buf := make([]byte, 16)
	n, err := p.Stdout().Read(buf)
	if err != nil || string(buf[:n]) != "ls\n" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
	p.Kill()
	if !fs.procs[0].wasKilled() {
		t.Fatal("not killed")
	}
}

func TestExecStreamProductionRunsCat(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not found")
	}
	f := &Fleet{}
	p, err := f.startStream(t.TempDir(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stdin().Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := p.Stdout().Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if s != "hello\n" {
			t.Fatalf("cat echoed %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no output from cat")
	}
	_ = p.Stdin().Close()
	waited := make(chan error, 1)
	go func() { waited <- p.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("cat did not exit after stdin closed")
	}
}

func TestExecStreamStartErrorIsReturned(t *testing.T) {
	f := &Fleet{}
	if _, err := f.startStream("", "/nonexistent/binary-for-marshal"); err == nil || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("err = %v", err)
	}
}

// isShortCommand recognises the `sh -c` commands the bridge runs beside a
// terminal, as opposed to the terminal's own `script`.
func isShortCommand(name string, args []string) bool {
	if name == "sh" {
		return true
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "sh" && args[i+1] == "-c" {
			return true
		}
	}
	return false
}
