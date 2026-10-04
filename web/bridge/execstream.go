package bridge

import (
	"io"
	"os"
	"os/exec"
)

// streamProc is a long-lived runtime process with a writable stdin and a
// readable merged stdout and stderr. It is the streaming sibling of
// commandRunner, which only returns buffered output.
type streamProc interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Wait blocks until the process exits.
	Wait() error
	// Kill ends the process.
	Kill()
}

// streamStarter starts a streamProc. dir is the working directory and is
// empty for runtime commands.
type streamStarter func(dir string, name string, args ...string) (streamProc, error)

// execStream is the production streamProc over os/exec.
type execStream struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   io.Reader
}

// startExecStream is the production streamStarter. It runs with the same
// minimal environment as every other runtime command and merges stderr
// into stdout.
func startExecStream(dir string, name string, args ...string) (streamProc, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = clientEnv()
	if dir != "" {
		cmd.Dir = dir
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	// The child holds its own copy of the write end; closing ours lets the
	// reader see EOF when the child exits.
	pw.Close()
	return &execStream{cmd: cmd, stdin: in, out: pr}, nil
}

func (p *execStream) Stdin() io.WriteCloser { return p.stdin }
func (p *execStream) Stdout() io.Reader     { return p.out }

func (p *execStream) Wait() error {
	err := p.cmd.Wait()
	if c, ok := p.out.(io.Closer); ok {
		c.Close()
	}
	return err
}

func (p *execStream) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// startStream starts a streaming process through the fleet's seam. Nil
// means the real starter, mirroring f.runner.
func (f *Fleet) startStream(dir, name string, args ...string) (streamProc, error) {
	if f.streamer != nil {
		return f.streamer(dir, name, args...)
	}
	return startExecStream(dir, name, args...)
}
