package bridge

import (
	"io"
	"os"
	"os/exec"
	"sync"
)

// agentTransport supplies one generation of a running agent: its framed
// stdio plus a handle to that generation's lifecycle. Nothing above this
// seam may see an *os.Process — a containerized agent has none.
//
// Implementations need not be safe for concurrent Open; Child serializes
// calls. Wait, Signal, and Kill may be called concurrently with Open and
// must tolerate being called before the first Open or after exit.
type agentTransport interface {
	// Open starts one generation and returns its streams. The returned
	// stderr may be an always-empty reader for transports with no separate
	// diagnostic stream.
	Open() (stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser, err error)
	// Wait blocks until the generation started by the most recent Open exits.
	Wait() error
	// Signal delivers sig to the current generation. A signal to an already
	// exited generation is not an error.
	Signal(sig os.Signal) error
	// Kill force-terminates the current generation.
	Kill() error
}

// processTransport runs the agent as a local child process. It is the
// behaviour Child had before the seam existed.
type processTransport struct {
	bin  string
	args []string
	env  []string

	mu  sync.Mutex
	cmd *exec.Cmd
}

func newProcessTransport(bin string, args, env []string) *processTransport {
	if bin == "" {
		bin = "marshal"
	}
	return &processTransport{bin: bin, args: args, env: env}
}

func (p *processTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	cmd := exec.Command(p.bin, p.args...)
	if p.env != nil {
		cmd.Env = append([]string(nil), p.env...)
	} else {
		cmd.Env = os.Environ()
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	return stdin, stdout, stderr, nil
}

func (p *processTransport) current() *exec.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd
}

func (p *processTransport) Wait() error {
	cmd := p.current()
	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}

// Signal re-reads the live generation under the mutex rather than
// capturing it once: Stop races with supervise respawning, and
// signalling a stale generation leaves the replacement running.
func (p *processTransport) Signal(sig os.Signal) error {
	cmd := p.current()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}

func (p *processTransport) Kill() error {
	cmd := p.current()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
