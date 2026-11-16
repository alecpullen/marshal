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
	"sync"
	"syscall"
	"time"
)

// maxFrameBytes bounds one inbound JSON-RPC frame from the child. A long
// session's session/stack result, or the first stack_patch, passes 2 MiB
// (2,000 calls measured about 2.67 MB), so the cap sits well above that and
// matches the pipe P3 gives the ACP transport (p3-acp-transport.md, R2). It
// is a var so tests can shrink it, as restartBackoff is.
var maxFrameBytes = 64 << 20

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

// errRequestCancelled is the handler's error when the child cancelled the
// request; answerRequest answers it as JSON-RPC -32800.
var errRequestCancelled = errors.New("bridge: the child cancelled the request")

// handlerError lets an OnRequest handler choose the JSON-RPC error code the
// child is answered with, for the protocol's own codes (such as -32601 for a
// method the handler does not implement). Every other error is answered
// -32000, and errRequestCancelled -32800, as before.
type handlerError struct {
	Code    int
	Message string
}

func (e *handlerError) Error() string { return e.Message }

// cancelRequestMethod is JSON-RPC's $/cancel_request notification. It is a
// literal because everything under web/ is standard-library only and so may
// not import marshal/internal/acpwire.
const cancelRequestMethod = "$/cancel_request"

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
	// Dir is a local process's working directory. Empty means the bridge's
	// own.
	Dir string
	// Transport supplies each generation's streams and lifecycle. Nil
	// means a local process built from MarshalBin, Args, Dir, and Env.
	Transport agentTransport
	// Containerized reports whether this child runs in a container whose
	// filesystem view differs from the bridge's, so paths crossing the
	// boundary must be translated (see agentRuntime.agentPath). The
	// production newRuntime closure sets it wherever it sets a container
	// Transport.
	//
	// It is a field rather than a type assertion on Transport because a
	// type assertion no test transport can satisfy leaves every
	// translating call site untestable — which is how the primary
	// translation site shipped with only a tautological test.
	Containerized bool

	// OnRestart is invoked after each unexpected exit, once the
	// replacement process has spawned. Intended for session resume.
	OnRestart func()
	// OnNotification is invoked for each inbound notification (a frame
	// with a method and no id). It runs on the read goroutine and must
	// not block.
	OnNotification func(method string, params json.RawMessage)
	// OnRequest handles a child-initiated request (a frame with both a method
	// and an id). ctx ends when the child cancels the request with
	// $/cancel_request, or when this generation's pipe closes. The handler's
	// result or error is answered with the request's own id.
	OnRequest func(ctx context.Context, method string, params json.RawMessage) (result any, err error)

	mu sync.Mutex // guards transport, stdin, stdout, stderrPipe, readDone, genCtx, genCancel, stopping, started
	// gen counts spawned generations, from 1. A caller keys a session to the
	// process that created it by reading it, and it is bumped in spawn, before
	// OnRestart runs, so a replacement is never mistaken for the old process.
	gen        uint64
	transport  agentTransport
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	// readDone is closed by this generation's readLoop when the child's
	// stdout reaches EOF. supervise waits on it before reaping.
	readDone chan struct{}

	// genCtx is the current generation's parent context: every child
	// request's context derives from it, and failPending cancels it, so a
	// request in flight ends when its generation's pipe closes. genCancel
	// is that context's cancel func.
	genCtx    context.Context
	genCancel context.CancelFunc

	stopping bool
	started  bool
	done     chan struct{}

	writeMu sync.Mutex // serializes all writes to the child's stdin

	pendMu  sync.Mutex
	nextID  int
	pending map[int]chan requestResult

	// inbound is each child request in flight, by its id's compact JSON, so a
	// $/cancel_request can end the one it names. Guarded by inboundMu.
	inboundMu sync.Mutex
	inbound   map[string]context.CancelFunc

	stderrMu sync.Mutex
	stderr   bytes.Buffer // bounded tail of the child's stderr
}

// keepaliveInterval is how often the bridge pings a quiet child so the
// agent's idle deadline (internal/acp idleDeadlineConn, 10 min) never
// fires on a healthy connection. A ping is an unknown-method
// notification the child silently discards; its only job is to be
// bytes on the wire. It is a var so tests can shrink the interval.
var keepaliveInterval = 5 * time.Minute

// startKeepalive pings the child periodically until it is stopped. The
// agent's listen path closes connections idle for acpIdleTimeout
// without traffic in either direction (follow-ups doc item #2); a
// child waiting on a human approval for longer than that must not be
// churned through a reattach cycle, so the bridge keeps the wire warm.
func (c *Child) startKeepalive() {
	go func() {
		t := time.NewTicker(keepaliveInterval)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-t.C:
				if err := c.Notify("ping", nil); err != nil {
					return
				}
			}
		}
	}()
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
	c.inboundMu.Lock()
	c.inbound = make(map[string]context.CancelFunc)
	c.inboundMu.Unlock()
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
	c.startKeepalive()
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

// stopConfirmed disables supervision and waits for the generation to exit.
// A failed stop signal is returned so callers retaining work never free a slot
// while the runtime may still be alive.
func (c *Child) stopConfirmed() error {
	c.mu.Lock()
	if !c.started || c.stopping {
		c.mu.Unlock()
		return errors.New("bridge: runtime state is not confirmable for parking")
	}
	c.stopping = true
	stdin := c.stdin
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	err := c.signalCurrentResult(syscall.SIGTERM)
	if err != nil {
		return err
	}
	kill := time.AfterFunc(stopGrace, c.killCurrent)
	defer kill.Stop()
	<-c.done
	return nil
}

// Detach releases the agent without ending it: the container keeps
// running under its name so a later Open reattaches instead of starting
// a duplicate. Used when the bridge is going away and the agent is not.
//
// Unlike Stop it does not wait on c.done. supervise blocks in
// tr.Wait(), which for a container is `docker wait` — and the container
// is deliberately still running, so that never returns. The supervise
// goroutine stays parked; Detach runs only on the shutdown path, and
// the process exits immediately after.
func (c *Child) Detach() {
	c.mu.Lock()
	if c.stopping || !c.started {
		c.mu.Unlock()
		return
	}
	// Set before detaching: supervise checks this after Wait returns
	// and respawns when it is false.
	c.stopping = true
	stdin := c.stdin
	tr := c.transport
	c.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if tr != nil {
		_ = tr.Detach()
	}
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
	c.mu.Lock()
	tr := c.transport
	c.mu.Unlock()
	if tr == nil {
		tr = c.Transport
		if tr == nil {
			args := append([]string{"acp"}, c.Args...)
			tr = newProcessTransport(c.MarshalBin, c.Dir, args, c.Env)
		}
	}

	stdin, stdout, stderr, err := tr.Open()
	if err != nil {
		return err
	}

	readDone := make(chan struct{})
	// Each generation gets its own parent context, so a request in flight
	// ends when this generation's pipe closes (failPending cancels it).
	genCtx, genCancel := context.WithCancel(context.Background())

	c.mu.Lock()
	c.gen++
	c.transport = tr
	c.stdin = stdin
	c.stdout = stdout
	c.stderrPipe = stderr
	c.readDone = readDone
	c.genCtx = genCtx
	c.genCancel = genCancel
	c.mu.Unlock()

	go func() {
		defer close(readDone)
		c.readLoop(stdout)
	}()
	go c.drainStderr(stderr)
	return nil
}

// Generation reports which spawned process is live, counting from 1, and 0
// before the first spawn. It is bumped inside spawn, before OnRestart is
// called, so a caller comparing it never sees a live replacement under the old
// generation's number.
func (c *Child) Generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// signalCurrent sends sig to the generation that is live right now.
// Stop races with supervise respawning, so the process is re-read under
// the mutex instead of captured once: signalling a stale generation
// leaves the replacement running and nothing ever ends supervision.
func (c *Child) signalCurrent(sig os.Signal) {
	_ = c.signalCurrentResult(sig)
}

func (c *Child) signalCurrentResult(sig os.Signal) error {
	c.mu.Lock()
	tr := c.transport
	c.mu.Unlock()
	if tr != nil {
		// Racing with an already-exited generation is benign.
		return tr.Signal(sig)
	}
	return errors.New("bridge: missing runtime transport")
}

// killCurrent SIGKILLs the generation that is live right now. Kill is a
// no-op once Wait has reaped the process.
func (c *Child) killCurrent() {
	c.mu.Lock()
	tr := c.transport
	c.mu.Unlock()
	if tr != nil {
		_ = tr.Kill()
	}
}

// supervise waits for each child generation to exit and respawns it,
// unless Stop marked the Child as stopping. On respawn it fails all
// pending requests and invokes OnRestart.
func (c *Child) supervise() {
	defer close(c.done)
	for {
		c.mu.Lock()
		tr := c.transport
		readDone := c.readDone
		c.mu.Unlock()

		// Drain stdout before reaping. exec's Wait closes the pipes as
		// soon as the process exits, and calling it before all reads
		// have completed discards a reply written just before exit.
		if readDone != nil {
			<-readDone
		}
		_ = tr.Wait()

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
// correlation map. It also cancels the generation's parent context, so a
// child request still in flight ends instead of waiting on a pipe that is
// already gone. Every dead generation runs this, whether it stopped, exited,
// or failed to respawn.
func (c *Child) failPending(err error) {
	c.mu.Lock()
	genCancel := c.genCancel
	c.mu.Unlock()
	if genCancel != nil {
		genCancel()
	}

	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	for id, ch := range c.pending {
		ch <- requestResult{err: err}
		delete(c.pending, id)
	}
}

// generationContext returns the current generation's parent context, which a
// child request's context derives from. It is read under c.mu so readLoop
// never races a respawn replacing the generation.
func (c *Child) generationContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.genCtx == nil {
		return context.Background()
	}
	return c.genCtx
}

// handleCancelRequest ends the child request the $/cancel_request names. A
// cancel for an id that names nothing in flight is ignored. Runs on the read
// goroutine, in arrival order, so a cancel that follows its request always
// finds it registered.
func (c *Child) handleCancelRequest(params json.RawMessage) {
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.ID) == 0 {
		return
	}
	key := compactID(p.ID)
	c.inboundMu.Lock()
	cancel, ok := c.inbound[key]
	c.inboundMu.Unlock()
	if ok {
		cancel()
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
// returns when the child's stdout closes or a frame cannot be read; the
// supervision goroutine handles the exit. A read failure that is not a
// shutdown also fails every pending request and kills the generation, so
// an oversized frame surfaces to its caller instead of wedging the pipe.
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
			if method == cancelRequestMethod && !hasID {
				// $/cancel_request is the protocol's own notification,
				// handled here on the read goroutine and in arrival order,
				// so a cancel that follows its request is never missed. It
				// never reaches OnNotification.
				c.handleCancelRequest(probe["params"])
				continue
			}
			if hasID && c.OnRequest != nil {
				// Child-initiated request (e.g. session/request_permission).
				// Register its cancel func before the handler goroutine
				// starts, so a $/cancel_request read next finds it. The
				// raw id is answered back unchanged.
				key := compactID(idRaw)
				ctx, cancel := context.WithCancel(c.generationContext())
				c.inboundMu.Lock()
				c.inbound[key] = cancel
				c.inboundMu.Unlock()
				go c.answerRequest(ctx, key, idRaw, method, probe["params"])
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

	// A scan error that is not a shutdown means the pipe is unusable: the
	// frame was too long, or the read failed. Fail the pending requests so
	// the caller waiting on this frame sees it, then kill the generation so
	// supervise reaps it and respawns, as it does for any exit. On a
	// shutdown Stop and Detach close the pipe themselves and hand their
	// pending requests errStopped.
	if err := sc.Err(); err != nil {
		c.mu.Lock()
		stopping := c.stopping
		c.mu.Unlock()
		if !stopping {
			c.failPending(fmt.Errorf("bridge: read child output: %w", err))
			c.killCurrent()
		}
	}
}

// answerRequest invokes OnRequest for one child-initiated request and writes
// the JSON-RPC response back under the request's own raw id, byte for byte.
// Runs on its own goroutine. It removes its own inbound entry when the
// handler returns, so a later cancel for this id names nothing.
func (c *Child) answerRequest(ctx context.Context, key string, id json.RawMessage, method string, params json.RawMessage) {
	defer func() {
		c.inboundMu.Lock()
		delete(c.inbound, key)
		c.inboundMu.Unlock()
	}()
	result, err := c.OnRequest(ctx, method, params)
	if err != nil {
		code, message := -32000, err.Error()
		var he *handlerError
		switch {
		case errors.Is(err, errRequestCancelled):
			// The child ended this request itself (P5 R2).
			code, message = -32800, "request cancelled"
		case errors.As(err, &he):
			code, message = he.Code, he.Message
		}
		_ = c.writeFrame(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": code, "message": message},
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
