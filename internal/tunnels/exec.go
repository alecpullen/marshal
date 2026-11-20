package tunnels

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stopTimeout is how long Stop waits for the process to go after SIGTERM
// before it kills it.
const stopTimeout = 2 * time.Second

// Commands runs ssh. Exec() runs real processes, and tests script them.
type Commands interface {
	// Output runs argv to completion, bounded by ctx, and answers its standard
	// output. A failure is a *CommandError.
	Output(ctx context.Context, argv []string) ([]byte, error)
	// Start starts argv and answers the running process.
	Start(argv []string) (Process, error)
}

// Process is one running ssh -N.
type Process interface {
	Done() <-chan struct{} // closed once the process has exited
	Err() error            // why it exited, a *CommandError; read after Done
	Stop()                 // SIGTERM, then SIGKILL after 2s; returns once it has exited
}

// CommandError is how an ssh command ended: its exit code (-1 when it did not
// start or was signalled) and its last non-empty line of stderr.
type CommandError struct {
	Code int
	Line string
}

// Error is Line, or "exit status <Code>" when Line is empty.
func (e *CommandError) Error() string {
	if e.Line == "" {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Line
}

// Exec is the Commands that runs processes.
func Exec() Commands { return execCommands{} }

// execCommands is Exec(): the Commands that runs real processes.
type execCommands struct{}

// Output runs argv to completion, bounded by ctx, and answers its standard
// output. A failure is a *CommandError.
func (execCommands) Output(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, &CommandError{Code: -1}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout bytes.Buffer
	stderr := &lineTail{}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &CommandError{Code: exitCode(cmd), Line: stderr.last()}
	}
	return stdout.Bytes(), nil
}

// Start starts argv: no standard input or output, and its stderr kept as its
// last non-empty line.
func (execCommands) Start(argv []string) (Process, error) {
	if len(argv) == 0 {
		return nil, &CommandError{Code: -1}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// Stdin and Stdout stay nil, which exec maps to the null device, as
	// web/bridge/helper.go (Helper.start) leaves them.
	tail := &lineTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, &CommandError{Code: -1, Line: strings.TrimSpace(err.Error())}
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	// Two goroutines: exec's own copies stderr into tail, and this one waits
	// for the process. Wait waits for the copier, so tail is complete here.
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.err = &CommandError{Code: exitCode(cmd), Line: tail.last()}
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// execProcess is one started process.
type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu     sync.Mutex // guards err
	err    error
	stopMu sync.Mutex // serialises Stop
}

// Done is closed once the process has exited.
func (p *execProcess) Done() <-chan struct{} { return p.done }

// Err is why the process exited: its *CommandError once Done is closed.
func (p *execProcess) Err() error {
	select {
	case <-p.done:
	default:
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Stop sends SIGTERM, waits up to stopTimeout for the process to go, then
// kills it and waits again. It is safe to call twice, and after the process
// has exited.
func (p *execProcess) Stop() {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	select {
	case <-p.done:
		return
	default:
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case <-timer.C:
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
}

// exitCode is a run's exit code, or -1 when the process did not start or was
// signalled.
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		return -1
	}
	return code
}

// lineTail keeps the last non-empty line written to it.
type lineTail struct {
	mu   sync.Mutex
	held []byte // bytes since the last newline
	line string
}

// Write records p's lines, keeping the last non-empty one.
func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.held = append(t.held, p...)
	for {
		i := bytes.IndexByte(t.held, '\n')
		if i < 0 {
			break
		}
		t.set(string(t.held[:i]))
		t.held = append(t.held[:0], t.held[i+1:]...)
	}
	return len(p), nil
}

// last is the last non-empty line, trimmed, with any unterminated tail line
// counted as a line of its own.
func (t *lineTail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	line := t.line
	if s := strings.TrimSpace(string(t.held)); s != "" {
		line = s
	}
	return line
}

// set records one line, ignoring a blank one. The caller holds t.mu.
func (t *lineTail) set(raw string) {
	if s := strings.TrimSpace(raw); s != "" {
		t.line = s
	}
}
