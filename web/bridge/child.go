// Package bridge supervises a `marshal acp` child process and speaks
// newline-delimited JSON-RPC 2.0 with it over stdio. Task 1 provides
// the process supervisor; later tasks add the session registry, event
// bus, and HTTP server on top.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// maxFrameBytes bounds one inbound JSON-RPC frame. It matches the ACP
// server's inbound default (bufio.Scanner, ~1 MiB).
const maxFrameBytes = 1 << 20

// stopGrace is how long Stop waits between SIGTERM and SIGKILL.
const stopGrace = 2 * time.Second

// restartBackoff is the delay before respawning an unexpectedly exited
// child, so a crash-looping binary does not spin the CPU. It is a var so
// tests can widen the respawn window and land Stop inside it
// deterministically.
var restartBackoff = 100 * time.Millisecond

// stderrCap bounds the retained tail of the child's stderr stream.
const stderrCap = 64 << 10

// errStopped is returned by Request and Notify on a stopped Child, and
// delivered to every pending request when Stop runs.
var errStopped = errors.New("bridge: child stopped")

// rpcError is a JSON-RPC error object returned by the child.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// requestResult carries one inbound response frame to the Request
// caller waiting on it.
type requestResult struct {
	result json.RawMessage
	err    error
}

// Child supervises one `marshal acp` process. It owns the stdio pipes,
// correlates responses to outbound requests by JSON-RPC id, routes
// inbound notifications (and method-less child frames) to callbacks,
// and respawns the process when it exits unexpectedly.
//
// The zero value is ready for use; MarshalBin defaults to "marshal".
type Child struct {
	// MarshalBin is the binary to spawn. Empty means "marshal".
	MarshalBin string
	// Args are extra arguments after "acp".
	Args []string
	// Env is the child environment. Nil inherits os.Environ().
	Env []string

	// OnRestart is invoked after each unexpected exit, once the
	// replacement process has spawned. Intended for session resume.
	OnRestart func()
	// OnNotification is invoked for each inbound notification (a frame
	// with a method and no id). It runs on the read goroutine and must
	// not block.
	OnNotification func(method string, params json.RawMessage)
	// OnRequest handles a child-initiated request (a frame with both a
	// method and an integer id), e.g. session/request_permission. It
	// runs on its own goroutine and may block; the returned result or
	// error is written back to the child as the JSON-RPC response.
	OnRequest func(id int, method string, params json.RawMessage) (result any, err error)

	mu         sync.Mutex // guards cmd, stdin, stdout, stderrPipe, readDone, stopping, started
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	// readDone is closed by this generation's readLoop when the child's
	// stdout reaches EOF. supervise waits on it before reaping.
	readDone chan struct{}

	stopping bool
	started  bool
	done     chan struct{}

	writeMu sync.Mutex // serializes all writes to the child's stdin

	pendMu  sync.Mutex
	nextID  int
	pending map[int]chan requestResult

	stderrMu sync.Mutex
	stderr   bytes.Buffer // bounded tail of the child's stderr
}

// Start spawns the child process and its read and supervision
// goroutines. It is not idempotent; call it once per Child.
func (c *Child) Start() error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("bridge: child already started")
	}
	c.started = true
	c.done = make(chan struct{})
	c.pendMu.Lock()
	c.pending = make(map[int]chan requestResult)
	c.pendMu.Unlock()
	c.mu.Unlock()
	if err := c.spawn(); err != nil {
		// Leave the Child in its pre-Start state so a cleanup Stop()
		// does not block forever on done.
		c.mu.Lock()
		c.started = false
		close(c.done)
		c.mu.Unlock()
		return err
	}
	go c.supervise()
	return nil
}

// Stop terminates the child and disables supervision: stdin is closed,
// then SIGTERM is sent, escalated to SIGKILL after a grace period. It
// is idempotent and safe to call before Start.
func (c *Child) Stop() {
	c.mu.Lock()
	if c.stopping || !c.started {
		c.mu.Unlock()
		return
	}
	c.stopping = true
	stdin := c.stdin
	c.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	c.signalCurrent(syscall.SIGTERM)
	kill := time.AfterFunc(stopGrace, c.killCurrent)
	defer kill.Stop()
	<-c.done
}

// Request sends one JSON-RPC request and blocks until the matching
// response arrives, ctx is done, or the child stops. The returned
// RawMessage is the response's "result".
func (c *Child) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.pendMu.Lock()
	if c.pending == nil {
		c.pendMu.Unlock()
		return nil, errStopped
	}
	id := c.nextID
	c.nextID++
	ch := make(chan requestResult, 1)
	c.pending[id] = ch
	c.pendMu.Unlock()

	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}

	err := c.writeFrame(req)
	if err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, err
	}

	select {
	case res := <-ch:
		return res.result, res.err
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, ctx.Err()
	}
}

// Notify sends one JSON-RPC notification (no id, no response).
func (c *Child) Notify(method string, params any) error {
	frame := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	return c.writeFrame(frame)
}

// StderrLog returns the bounded tail of everything the child has
// written to stderr so far.
func (c *Child) StderrLog() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return c.stderr.String()
}

// spawn starts one generation of the child process and its read and
// stderr-drain goroutines.
func (c *Child) spawn() error {
	bin := c.MarshalBin
	if bin == "" {
		bin = "marshal"
	}
	args := append([]string{"acp"}, c.Args...)
	cmd := exec.Command(bin, args...)
	if c.Env != nil {
		cmd.Env = append([]string(nil), c.Env...)
	} else {
		cmd.Env = os.Environ()
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	readDone := make(chan struct{})

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.stderrPipe = stderr
	c.readDone = readDone
	c.mu.Unlock()

	go func() {
		defer close(readDone)
		c.readLoop(stdout)
	}()
	go c.drainStderr(stderr)
	return nil
}

// signalCurrent sends sig to the generation that is live right now.
// Stop races with supervise respawning, so the process is re-read under
// the mutex instead of captured once: signalling a stale generation
// leaves the replacement running and nothing ever ends supervision.
func (c *Child) signalCurrent(sig os.Signal) {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		// Racing with an already-exited process is benign: Signal
		// returns an error we ignore.
		_ = cmd.Process.Signal(sig)
	}
}

// killCurrent SIGKILLs the generation that is live right now. Kill is a
// no-op once Wait has reaped the process.
func (c *Child) killCurrent() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// supervise waits for each child generation to exit and respawns it,
// unless Stop marked the Child as stopping. On respawn it fails all
// pending requests and invokes OnRestart.
func (c *Child) supervise() {
	defer close(c.done)
	for {
		c.mu.Lock()
		cmd := c.cmd
		readDone := c.readDone
		c.mu.Unlock()

		// Drain stdout before reaping. exec's Wait closes the pipes as
		// soon as the process exits, and os/exec documents that calling
		// it before all reads have completed is incorrect — doing so
		// discards a reply the child wrote just before exiting, which
		// surfaces to the caller as a spurious "child exited" error.
		if readDone != nil {
			<-readDone
		}
		_ = cmd.Wait()

		c.mu.Lock()
		stopping := c.stopping
		c.mu.Unlock()
		if stopping {
			c.failPending(errStopped)
			return
		}

		c.failPending(errors.New("bridge: child exited"))
		time.Sleep(restartBackoff)
		if err := c.spawn(); err != nil {
			// Respawn failure leaves the Child without a live process;
			// requests keep failing fast and no further restart is
			// attempted.
			c.failPending(fmt.Errorf("bridge: respawn: %w", err))
			return
		}
		// Stop may have begun while this generation was respawning, in
		// which case it signalled the previous one. Terminate the
		// replacement here; the next iteration reaps it and the stopping
		// check above ends supervision, releasing Stop's wait on done.
		c.mu.Lock()
		stopping = c.stopping
		c.mu.Unlock()
		if stopping {
			c.killCurrent()
			continue
		}
		if c.OnRestart != nil {
			c.OnRestart()
		}
	}
}

// failPending delivers err to every pending request and clears the
// correlation map.
func (c *Child) failPending(err error) {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	for id, ch := range c.pending {
		ch <- requestResult{err: err}
		delete(c.pending, id)
	}
}

// writeFrame marshals v as one JSON object and writes it plus a
// trailing newline to the child's stdin. Concurrent callers are
// serialized so frames never interleave mid-line.
func (c *Child) writeFrame(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	c.mu.Lock()
	stdin := c.stdin
	stopping := c.stopping
	c.mu.Unlock()
	if stopping || stdin == nil {
		return errStopped
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = stdin.Write(body)
	return err
}

// readLoop demuxes inbound frames: responses (id, no method) go to the
// pending request, frames with a method go to OnNotification. It
// returns when the child's stdout closes; the supervision goroutine
// handles the exit.
func (c *Child) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)
	for sc.Scan() {
		line := sc.Bytes()
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		methodRaw, hasMethod := probe["method"]
		idRaw, hasID := probe["id"]

		if hasMethod {
			var method string
			if err := json.Unmarshal(methodRaw, &method); err != nil {
				continue
			}
			if hasID && c.OnRequest != nil {
				// Child-initiated request (e.g. session/request_permission).
				// Handle on a separate goroutine: the handler may block
				// and readLoop must keep draining.
				var id int
				if err := json.Unmarshal(idRaw, &id); err != nil {
					continue
				}
				go c.answerRequest(id, method, probe["params"])
				continue
			}
			if c.OnNotification == nil {
				continue
			}
			c.OnNotification(method, probe["params"])
			continue
		}
		if !hasID {
			continue // neither response nor notification: ignore
		}
		var id int
		if err := json.Unmarshal(idRaw, &id); err != nil {
			continue // non-integer id we never assigned
		}

		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		res := requestResult{}
		if err := json.Unmarshal(line, &resp); err != nil {
			res.err = err
		} else if resp.Error != nil {
			res.err = resp.Error
		} else {
			res.result = resp.Result
		}

		c.pendMu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.pendMu.Unlock()
		if ok {
			ch <- res
		}
	}
}

// answerRequest invokes OnRequest for one child-initiated request and
// writes the JSON-RPC response back. Runs on its own goroutine.
func (c *Child) answerRequest(id int, method string, params json.RawMessage) {
	result, err := c.OnRequest(id, method, params)
	if err != nil {
		_ = c.writeFrame(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32000, "message": err.Error()},
		})
		return
	}
	_ = c.writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

// drainStderr copies the child's stderr into the bounded log buffer
// until the pipe closes.
func (c *Child) drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c.appendStderr(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// appendStderr appends p to the stderr tail, dropping the oldest bytes
// once the buffer exceeds stderrCap.
func (c *Child) appendStderr(p []byte) {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	if len(p) >= stderrCap {
		c.stderr.Reset()
		_, _ = c.stderr.Write(p[len(p)-stderrCap:])
		return
	}
	_, _ = c.stderr.Write(p)
	if c.stderr.Len() > stderrCap {
		tail := c.stderr.Bytes()[c.stderr.Len()-stderrCap:]
		keep := make([]byte, len(tail))
		copy(keep, tail)
		c.stderr.Reset()
		_, _ = c.stderr.Write(keep)
	}
}
