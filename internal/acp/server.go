package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"marshal/internal/acpwire"
)

// nullID is the JSON-RPC "id: null" sentinel used for responses where
// the request id could not be determined (parse errors). JSON-RPC 2.0
// §4.6 requires id to be present and null in that case, but
// *json.RawMessage with omitempty would omit it.
var nullID = json.RawMessage("null")

// joinErrors returns the joined error of all non-nil errors. When only
// one non-nil error is present it is returned unwrapped so callers can
// use == comparison with sentinel errors (context.Canceled, etc.).
func joinErrors(errs ...error) error {
	n := 0
	for _, err := range errs {
		if err != nil {
			errs[n] = err
			n++
		}
	}
	switch n {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return errors.Join(errs[:n]...)
	}
}

// defaultHandlerShutdownTimeout is the default value for
// Server.handlerShutdownTimeout.
const defaultHandlerShutdownTimeout = 5 * time.Second

// responseBarrierTimeout bounds how long a response waits for its session's
// fences (P3 R11).
const responseBarrierTimeout = 2 * time.Second

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// outboundResult carries either a response or a terminal error to a
// waiter blocked in Request.
type outboundResult struct {
	response *Response
	err      error
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithLogger sets the logger on a Server. When nil (or unset), the
// server uses slog.Default().
func WithLogger(l *slog.Logger) ServerOption {
	return func(s *Server) { s.Logger = l }
}

// Server is a dedicated asynchronous JSON-RPC frame router that keeps
// the transport reader live while handlers execute. Inbound requests
// are classified and dispatched to handler goroutines so that the
// scanner goroutine continues reading; outbound responses (editor
// replies to permission requests) are routed back to the blocking
// Request caller. When the transport closes or the parent context
// cancels, pending handlers are waited on with a bounded timeout.
type Server struct {
	Logger *slog.Logger // nil → slog.Default()

	in       io.Reader
	out      *json.Encoder
	handlers map[string]Handler

	// notificationMethods tracks methods registered via HandleNotification.
	// An id-bearing frame for one of these methods is rejected with
	// invalidRequest (-32600) because the method is defined as a
	// notification and clients must not expect a response.
	notificationMethods map[string]bool

	// outMu serialises writes to the JSON encoder. Encode is
	// goroutine-safe for individual calls, but our writes interleave
	// notification, response, and outbound-request frames and the
	// underlying bufio.Writer is not safe for concurrent use; the
	// mutex keeps the on-the-wire stream atomic.
	outMu sync.Mutex

	// stateMu guards the outbound waiters map and the closed flag.
	// Reads and writes happen from the Serve loop and from independent
	// handler goroutines (via Request), so a dedicated mutex avoids
	// blocking encoder throughput.
	stateMu    sync.Mutex
	outbound   map[string]chan outboundResult
	outboundID uint64
	closed     bool

	// inFlight tracks inbound requests that are still being handled, keyed
	// by the compact JSON of their id, so a $/cancel_request can end the one
	// it names (P3 R2). Guarded by stateMu. A notification registers nothing.
	inFlight map[string]*inflight

	// marshal records whether this connection's client declared the
	// marshal-only protocol in initialize (P3 R9). Guarded by stateMu.
	marshal bool

	// responseBarrier flushes the updates a call caused before its response is
	// written, when the client needs the read-your-writes guarantee (P3 R2).
	// Nil means no barrier. Guarded by stateMu.
	responseBarrier func(ctx context.Context, sessionID string)

	// barrierTimeout bounds how long a response waits for that flush (P3 R11).
	// Guarded by stateMu.
	barrierTimeout time.Duration

	// handlerWG tracks in-flight handler goroutines so Serve can wait
	// for them during shutdown.
	handlerWG sync.WaitGroup

	// handlerShutdownTimeout is the maximum time Serve waits for active
	// handlers to complete. Tests can override this per-instance.
	handlerShutdownTimeout time.Duration

	// MaxLineBytes is the maximum token size for the bufio.Scanner used
	// in scanLines. If zero or negative, a default of 1 MiB is used.
	// Set this before calling Serve (F-POL-60).
	MaxLineBytes int

	// EndOnPanic makes a handler's panic end the connection: the panic is
	// logged with its stack, the call is answered as before, and Serve returns
	// an error naming the method. The embedding sets it (P3 R11). Set it before
	// Serve.
	EndOnPanic bool

	// MarshalOnly refuses a client that did not declare the marshal
	// capability, and every call before a marshal initialize (P4 D2). Set it
	// before Serve.
	MarshalOnly bool

	// fatalErr is a buffered channel that handler goroutines use to
	// report fatal write errors to the Serve loop. Created in Serve.
	fatalErr chan error
}

func NewServer(stdin io.Reader, stdout io.Writer, opts ...ServerOption) *Server {
	s := &Server{
		in:                     stdin,
		out:                    json.NewEncoder(stdout),
		handlers:               map[string]Handler{},
		notificationMethods:    map[string]bool{},
		outbound:               map[string]chan outboundResult{},
		handlerShutdownTimeout: defaultHandlerShutdownTimeout,
		barrierTimeout:         responseBarrierTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Server) Handle(method string, fn Handler) {
	s.handlers[method] = fn
}

// HandleNotification registers fn as the handler for a method that the
// ACP plan defines as a notification. The handler runs and its result
// is discarded: no response is ever written for a notification. If a
// client sends the method WITH a request id, the server responds with
// invalidRequest (-32600) because the method is notification-only and
// clients must not expect a response.
func (s *Server) HandleNotification(method string, fn Handler) {
	s.handlers[method] = fn
	s.notificationMethods[method] = true
}

// Notify emits a JSON-RPC notification frame (no id) to the connected
// client. Prompt turns and permission requests can fire notifications
// from independent goroutines, so the encoder write is guarded by a
// mutex to keep the on-the-wire stream atomic.
func (s *Server) Notify(method string, params any) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return s.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// Request sends an outbound JSON-RPC request to the connected editor
// and blocks until a matching response (by id) is read from stdin, the
// context is cancelled, or the server is shut down. The returned value
// is the response's Result payload (or the response's Error if the
// editor rejected the request).
//
// Thread safety:
//  1. Rejects a closed server under stateMu.
//  2. Registers the waiter channel under stateMu.
//  3. Encodes the outbound frame under outMu.
//  4. Removes the waiter if encoding fails.
//  5. Waits on the channel, or removes the waiter on ctx cancellation.
func (s *Server) Request(ctx context.Context, method string, params any, result any) error {
	// 0. Reject empty method (F-POL-61).
	if method == "" {
		return fmt.Errorf("acp: request method is required")
	}

	// 1. Reject closed server.
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return fmt.Errorf("acp: server closed")
	}

	// 2. Allocate and register waiter.
	s.outboundID++
	id := fmt.Sprintf("marshal-out-%d", s.outboundID)
	ch := make(chan outboundResult, 2)
	s.outbound[id] = ch
	s.stateMu.Unlock()

	// 3. Encode outbound request under outMu.
	s.outMu.Lock()
	encErr := s.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	s.outMu.Unlock()

	// 4. Remove waiter if encoding fails.
	if encErr != nil {
		s.stateMu.Lock()
		delete(s.outbound, id)
		s.stateMu.Unlock()
		return fmt.Errorf("acp: encode outbound request: %w", encErr)
	}

	// 5. Wait for response or context cancellation.
	select {
	case res, ok := <-ch:
		if !ok {
			return fmt.Errorf("acp: outbound request %s: connection closed", method)
		}
		if res.err != nil {
			return res.err
		}
		if res.response == nil {
			return fmt.Errorf("acp: outbound request %s: nil response", method)
		}
		if res.response.Error != nil {
			return fmt.Errorf("acp: %s: %s", method, res.response.Error.Message)
		}
		if result == nil || res.response.Result == nil {
			return nil
		}
		raw, err := json.Marshal(res.response.Result)
		if err != nil {
			return fmt.Errorf("acp: re-encode %s result: %w", method, err)
		}
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("acp: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		s.stateMu.Lock()
		delete(s.outbound, id)
		s.stateMu.Unlock()
		return ctx.Err()
	}
}

// Serve runs the transport read loop until the parent context is
// cancelled, the input reader reaches EOF or errors, or a fatal write
// error is reported. The scanner goroutine decodes individual frames
// and sends them on a buffered channel; the main goroutine classifies
// and dispatches each frame without blocking the reader. On shutdown,
// all outbound waiters are failed, active handlers are waited on with
// a bounded timeout, and errors are joined.
func (s *Server) Serve(ctx context.Context) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.fatalErr = make(chan error, 16)

	// Scanner goroutine: reads lines from input, sends to frames channel.
	frames := make(chan []byte, 100)
	scannerDone := make(chan error, 1)
	go s.scanLines(serveCtx, frames, scannerDone)

	for {
		select {
		case <-ctx.Done():
			// Parent context cancelled.
			handlerErr, late := s.shutdown(cancel)
			return joinErrors(ctx.Err(), handlerErr, late)

		case scanErr := <-scannerDone:
			// Scanner finished (EOF or error). Drain any buffered frames
			// before shutting down so no frame is lost due to select
			// picking scannerDone over a ready frames channel.
		drainLoop:
			for {
				select {
				case line := <-frames:
					s.handleFrame(serveCtx, line)
				default:
					break drainLoop
				}
			}
			handlerErr, late := s.shutdown(cancel)
			if ctx.Err() != nil {
				return joinErrors(ctx.Err(), handlerErr, late)
			}
			if scanErr != nil {
				return joinErrors(scanErr, handlerErr, late)
			}
			return joinErrors(handlerErr, late)

		case fatalWrite := <-s.fatalErr:
			// A handler goroutine reported a fatal write error.
			handlerErr, late := s.shutdown(cancel)
			return joinErrors(fatalWrite, handlerErr, late)

		case line := <-frames:
			s.handleFrame(serveCtx, line)
		}
	}
}

// scanLines reads from s.in using a bufio.Scanner and sends each
// trimmed non-empty line to the frames channel. It exits when the
// scanner finishes (EOF or error) or serveCtx is cancelled.
func (s *Server) scanLines(serveCtx context.Context, frames chan<- []byte, done chan<- error) {
	sc := bufio.NewScanner(s.in)
	maxLine := s.MaxLineBytes
	if maxLine <= 0 {
		maxLine = 1 << 20 // 1 MiB default
	}
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		select {
		case frames <- []byte(line):
		case <-serveCtx.Done():
			done <- sc.Err()
			return
		}
	}
	done <- sc.Err()
}

// inflight is one inbound request the server is still handling. A
// $/cancel_request for its id closes cancelled, ending any handler context
// derived with cancellable.
type inflight struct {
	id        json.RawMessage
	srv       *Server
	cancelled chan struct{}
	once      sync.Once
}

// cancel ends the request's cancellable handler contexts. It is safe to call
// more than once.
func (f *inflight) cancel() {
	f.once.Do(func() { close(f.cancelled) })
}

// inflightKey is a request id's compact JSON, so the registry matches an id
// however its whitespace is encoded.
func inflightKey(id json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, id); err != nil {
		return string(id)
	}
	return buf.String()
}

// requestKey carries an *inflight in a handler's context.
type requestKey struct{}

// withInflight returns ctx carrying entry. It is a value on the request's own
// context, so nothing registers with the connection's context and a long
// connection leaks nothing per request.
func withInflight(ctx context.Context, entry *inflight) context.Context {
	return context.WithValue(ctx, requestKey{}, entry)
}

// inflightFrom returns the request entry ctx carries, or nil when it has none.
func inflightFrom(ctx context.Context) *inflight {
	entry, _ := ctx.Value(requestKey{}).(*inflight)
	return entry
}

// registerInflight records a request before its handler starts, so a cancel
// that arrives next finds it.
func (s *Server) registerInflight(id json.RawMessage) *inflight {
	entry := &inflight{id: id, srv: s, cancelled: make(chan struct{})}
	s.stateMu.Lock()
	if s.inFlight == nil {
		s.inFlight = make(map[string]*inflight)
	}
	s.inFlight[inflightKey(id)] = entry
	s.stateMu.Unlock()
	return entry
}

// unregisterInflight drops a request once its handler has returned.
func (s *Server) unregisterInflight(id *json.RawMessage) {
	if id == nil {
		return
	}
	s.stateMu.Lock()
	delete(s.inFlight, inflightKey(*id))
	s.stateMu.Unlock()
}

// handleCancelRequest ends the request the params name. An unknown id, a
// malformed frame and a repeat cancel are ignored: the method is a
// notification, so nothing is written back.
func (s *Server) handleCancelRequest(params json.RawMessage) {
	var p acpwire.CancelRequestParams
	if err := json.Unmarshal(params, &p); err != nil || len(p.ID) == 0 {
		return
	}
	s.stateMu.Lock()
	entry, ok := s.inFlight[inflightKey(p.ID)]
	s.stateMu.Unlock()
	if !ok {
		return
	}
	entry.cancel()
}

// cancellable returns a context for a handler that can be cancelled: it ends
// when the request is cancelled with $/cancel_request, as well as when ctx
// ends. Handlers that keep today's context are unaffected, because some flows
// (an MCP sign-in) outlive their request on purpose. A context carrying no
// request is simply derived. The caller must call the cancel function.
func cancellable(ctx context.Context) (context.Context, context.CancelFunc) {
	derived, cancel := context.WithCancel(ctx)
	entry := inflightFrom(ctx)
	if entry == nil {
		return derived, cancel
	}
	// A cancel that arrived before the handler opted in ends the derived
	// context now, not when the watcher below first runs.
	select {
	case <-entry.cancelled:
		cancel()
		return derived, cancel
	default:
	}
	go func() {
		select {
		case <-entry.cancelled:
			cancel()
		case <-derived.Done():
		}
	}()
	return derived, cancel
}

// reportProgress sends one $/progress value for the request ctx carries. A
// context with no request (a notification's) sends nothing. The handler sends
// its progress before it returns and the response is written after, so every
// value precedes the response on the wire (P3 R2).
func reportProgress(ctx context.Context, value any) error {
	entry := inflightFrom(ctx)
	if entry == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return entry.srv.Notify(acpwire.MethodProgress, acpwire.ProgressParams{
		ID:    entry.id,
		Value: raw,
	})
}

// marshalClient reports whether this connection's client declared the
// marshal-only protocol in initialize (P3 R9).
func (s *Server) marshalClient() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.marshal
}

// setMarshalClient records whether this connection's client declared the
// marshal-only protocol in initialize (P3 R9).
func (s *Server) setMarshalClient(v bool) {
	s.stateMu.Lock()
	s.marshal = v
	s.stateMu.Unlock()
}

// SetResponseBarrier installs fn, which the server runs after a request's
// handler returns and before its response is written, when the client
// declared the marshal capability and the request's params name a session
// (P3 R2). fn returns once every update the call caused is written.
func (s *Server) SetResponseBarrier(fn func(ctx context.Context, sessionID string)) {
	s.stateMu.Lock()
	s.responseBarrier = fn
	s.stateMu.Unlock()
}

// runResponseBarrier waits for the session's forwarders to write the updates
// the call caused, so a marshal client's read after the call sees its effect
// (P3 R2). Every other request is answered exactly as it was before (R9): the
// barrier runs only for a marshal client and only for a call whose params name
// a session. A forwarder that never answers costs the response at most
// barrierTimeout, then a warning (R11).
func (s *Server) runResponseBarrier(ctx context.Context, req Request) {
	s.stateMu.Lock()
	barrier := s.responseBarrier
	timeout := s.barrierTimeout
	s.stateMu.Unlock()
	if barrier == nil || !s.marshalClient() {
		return
	}
	sessionID, ok := sessionParam(req.Params)
	if !ok {
		return
	}
	// The barrier's context ends with the handler's request, and the wait is
	// bounded by the timer below. The barrier is cancelled only after the
	// warning, so a forwarder that never answers is released with the bound
	// spent rather than racing the log.
	barrierCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		// The deferred close runs last, so a panicking barrier still releases
		// the select below. dispatchRequest's own recover cannot reach this
		// goroutine: a barrier panic must not take the server down.
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				s.log().Error("acp: response barrier panicked",
					"method", req.Method, "session_id", sessionID, "panic", r)
			}
		}()
		barrier(barrierCtx, sessionID)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// A barrier that finished as the timer fired is not a timeout, and
		// select chooses at random among ready cases.
		select {
		case <-done:
			return
		default:
		}
		s.log().Warn("acp: response barrier timed out",
			"method", req.Method, "session_id", sessionID)
		stop()
	}
}

// sessionParam returns the session a request's params name, when they carry a
// top-level non-empty "sessionId" string. A request with no params, params
// that are not an object, or a differently typed id names no session.
func sessionParam(params json.RawMessage) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	var named struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &named); err != nil {
		return "", false
	}
	if named.SessionID == "" {
		return "", false
	}
	return named.SessionID, true
}

// handleFrame classifies a single decoded JSON-RPC frame and
// dispatches it to the appropriate handler goroutine or outbound
// response delivery.
func (s *Server) handleFrame(ctx context.Context, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		// Malformed JSON — emit parse error with id:null.
		if writeErr := s.writeError(nil, parseError, "parse error: "+err.Error()); writeErr != nil {
			s.reportFatal(writeErr)
		}
		return
	}

	// Classify the frame using hasMethod (set by UnmarshalJSON) to avoid
	// re-unmarshalling the line just to check for a "method" key (F-POL-59).
	if !req.hasMethod {
		// No method key — this is a response (or a garbage frame).
		if req.ID != nil {
			s.deliverOutbound(req.ID, line)
		}
		return
	}
	if req.Method == "" {
		// Explicit "method":"" — invalid request.
		if req.ID != nil {
			if writeErr := s.writeError(req.ID, invalidRequest, "method is required"); writeErr != nil {
				s.reportFatal(writeErr)
			}
		}
		return
	}

	// Frame has a method — inbound request or notification.
	//
	// $/cancel_request is the protocol's own notification, so the server
	// handles it here, on the read goroutine and in arrival order: a cancel
	// that follows its request is never missed (P3 R2).
	if req.Method == acpwire.MethodCancelRequest {
		if req.ID != nil {
			// Notification-only, like session/cancel.
			if writeErr := s.writeError(req.ID, invalidRequest, "method is notification-only: "+req.Method); writeErr != nil {
				s.reportFatal(writeErr)
			}
			return
		}
		s.handleCancelRequest(req.Params)
		return
	}
	if s.notificationMethods[req.Method] {
		// Method is registered as notification-only. Reject id-bearing
		// frames with invalidRequest; accept no-id frames and run the
		// handler without writing a response.
		if req.ID != nil {
			if writeErr := s.writeError(req.ID, invalidRequest, "method is notification-only: "+req.Method); writeErr != nil {
				s.reportFatal(writeErr)
			}
			return
		}
		s.handlerWG.Add(1)
		go s.dispatchNotification(ctx, req)
		return
	}
	s.handlerWG.Add(1)
	if req.ID == nil {
		// Notification — no response expected, so nothing to cancel.
		go s.dispatchNotification(ctx, req)
	} else {
		// Request — handler writes a response. Register it before the
		// handler starts, so a cancel that arrives next finds it.
		entry := s.registerInflight(*req.ID)
		go s.dispatchRequest(withInflight(ctx, entry), req)
	}
}

// deliverOutbound routes a response frame to a pending outbound
// waiter identified by the frame's id. Returns true if a waiter
// was found and received the response.
func (s *Server) deliverOutbound(id *json.RawMessage, line []byte) bool {
	if id == nil {
		return false
	}
	key := unquoteJSONString(string(*id))

	s.stateMu.Lock()
	ch, ok := s.outbound[key]
	if ok {
		delete(s.outbound, key)
	}
	s.stateMu.Unlock()

	if !ok {
		return false
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		resp = Response{Error: &Error{Code: parseError, Message: "malformed response: " + err.Error()}}
	}
	select {
	case ch <- outboundResult{response: &resp}:
	default:
		// Buffer full — both slots consumed or waiter already gone.
		s.log().Warn("acp: outbound response dropped (channel full)",
			"id", key)
	}
	return true
}

// failOutbound replaces the outbound map with an empty one and
// delivers the given error to every buffered waiter channel.
func (s *Server) failOutbound(err error) {
	s.stateMu.Lock()
	old := s.outbound
	s.outbound = make(map[string]chan outboundResult)
	s.closed = true
	s.stateMu.Unlock()

	for _, ch := range old {
		select {
		case ch <- outboundResult{err: err}:
		default:
		}
		close(ch)
	}
}

// reportFatal sends a fatal error to the Serve loop via the buffered
// fatalErr channel. The channel has capacity 16 so simultaneous
// reports do not block. Serve drains the channel on shutdown to
// ensure every reported fatal is joined into the final return value.
func (s *Server) reportFatal(err error) {
	s.fatalErr <- err
}

// wireError returns the safe, opaque message emitted to the client.
// Server-side: the full error is logged via slog before this is
// called. Wire-side: a fixed string per JSON-RPC error code. F-SEC-37.
func wireError(err error) string {
	var rpcErr *jsonRPCError
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case parseError:
			return "parse error"
		case invalidRequest:
			// invalid-request messages are server-generated refusals, not
			// client input, so they are safe to expose; a viewer's refusal
			// carries its sentence to the client this way (P4 D2). F-SEC-37.
			return "invalid request: " + rpcErr.Message
		case methodNotFound:
			return "method not found"
		case invalidParams:
			return "invalid params"
		case internalError:
			return "internal error"
		case requestCancelled:
			return "request cancelled"
		case serverError:
			// serverError messages are server-generated (not client input),
			// so they are safe to expose to the client. F-SEC-37.
			return "server error: " + rpcErr.Message
		default:
			return "server error"
		}
	}
	if errors.Is(err, context.Canceled) {
		return "request cancelled"
	}
	return "internal error"
}

// dispatchRequest runs the handler for an inbound request with an id
// and writes exactly one success/error response. If the response
// encode fails, the error is reported as fatal.
func (s *Server) dispatchRequest(ctx context.Context, req Request) {
	defer s.handlerWG.Done()

	result, err := s.dispatch(ctx, req)
	// Drop the request from the in-flight registry before the response is
	// written, so a cancel that arrives after it is ignored (P3 R2).
	s.unregisterInflight(req.ID)
	// A marshal client's call returns only after every update it caused is
	// written, so a read after the call sees its effect (P3 R2). The barrier
	// runs for a failed call too: it may have published before it failed.
	s.runResponseBarrier(ctx, req)
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if err != nil {
		var data json.RawMessage
		var rpcErr *jsonRPCError
		if errors.As(err, &rpcErr) {
			data = rpcErr.Data
		}
		resp.Error = &Error{Code: codeFor(err), Message: wireError(err), Data: data}
		// Log the full error server-side for operators. F-SEC-37.
		s.log().Warn("acp dispatch error", "method", req.Method, "err", err)
	} else {
		resp.Result = result
	}

	s.outMu.Lock()
	encErr := s.out.Encode(resp)
	s.outMu.Unlock()

	if encErr != nil {
		s.reportFatal(fmt.Errorf("acp: encode response: %w", encErr))
	}
}

// dispatchNotification runs the handler for a notification (no id)
// and discards its result and error.
func (s *Server) dispatchNotification(ctx context.Context, req Request) {
	defer s.handlerWG.Done()
	_, _ = s.dispatch(ctx, req)
}

// waitHandlers blocks until all in-flight handler goroutines complete
// or the handlerShutdownTimeout elapses, returning the timeout error
// if the deadline was exceeded.
func (s *Server) waitHandlers() error {
	waitCtx, cancel := context.WithTimeout(context.Background(), s.handlerShutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.handlerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

// shutdown performs the common Serve teardown: cancel the serve context,
// fail pending outbound calls, close input, wait for handlers, and drain
// any fatal errors reported during or after the wait. The caller joins the
// returned errors with its arm-specific cause.
func (s *Server) shutdown(cancel context.CancelFunc) (handlerErr error, late error) {
	cancel()
	s.failOutbound(errors.New("acp: connection closed"))
	s.closeInput()
	handlerErr = s.waitHandlers()
	var lateErrs []error
	for {
		select {
		case err := <-s.fatalErr:
			lateErrs = append(lateErrs, err)
		default:
			return handlerErr, errors.Join(lateErrs...)
		}
	}
}

// closeInput closes the underlying reader if it implements io.Closer.
func (s *Server) closeInput() {
	if closer, ok := s.in.(io.Closer); ok {
		closer.Close()
	}
}

// writeError encodes and writes a JSON-RPC error response. It acquires
// outMu internally so callers do not need to serialise access.
func (s *Server) writeError(id *json.RawMessage, code int, message string) error {
	if id == nil {
		id = &nullID
	}
	resp := Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if err := s.out.Encode(resp); err != nil {
		return fmt.Errorf("acp: encode error: %w", err)
	}
	return nil
}

// unquoteJSONString returns the raw JSON token with one layer of
// surrounding quotes removed, if present. Outbound ids are string-
// encoded in the map, but the parsed id arrives as the raw JSON
// representation (which includes the quotes).
func unquoteJSONString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// log returns the server's logger, defaulting to slog.Default() when
// the Logger field is nil.
func (s *Server) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *Server) dispatch(ctx context.Context, req Request) (result any, err error) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			s.log().Error("acp: handler panicked",
				"method", req.Method,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			result = nil
			err = &jsonRPCError{
				Code:    internalError,
				Message: "internal error: handler panicked",
			}
			// An embedded connection has nobody to report a broken call to:
			// ending it with the panic's method lets the TUI end with the error
			// (P3 R11). Every other client keeps recovering call by call.
			if s.EndOnPanic {
				s.reportFatal(fmt.Errorf("acp: %s panicked: %v", req.Method, r))
			}
		}
		s.log().Info("acp dispatch",
			"method", req.Method,
			"id", req.ID,
			"duration_ms", time.Since(start).Milliseconds(),
			"error", err,
		)
	}()
	// A viewer serves marshal clients only (P4 D2): until a marshal initialize
	// has been answered, every other call is refused the same way.
	if s.MarshalOnly && req.Method != "initialize" && !s.marshalClient() {
		return nil, viewerCapabilityRefusal()
	}
	handler, ok := s.handlers[req.Method]
	if !ok {
		return nil, &jsonRPCError{Code: methodNotFound, Message: "method not found: " + req.Method}
	}
	res, herr := handler(ctx, req.Params)
	if herr != nil {
		// A marshal client's error keeps its sentence (decision 1). The marshal
		// client is the embedding's own TUI, which shows these sentences in
		// process, so a plain error is sent as the uncoded *acpwire.Error
		// settingsFailure builds; a coded error, a JSON-RPC error and a
		// cancellation pass through unchanged. Every other client keeps
		// F-SEC-37's fixed text.
		if s.marshalClient() {
			herr = settingsFailure(herr)
		}
		// An *acpwire.Error travels as itself: code serverError, its own
		// message, and the whole error as data, so the client rebuilds it and
		// IsCode, NoticeOf and err.Error() read as they do in process (P3 R2).
		var wired *acpwire.Error
		if errors.As(herr, &wired) {
			data, encErr := json.Marshal(wired)
			if encErr != nil {
				return nil, &jsonRPCError{
					Code:    internalError,
					Message: "internal error: encode error data: " + encErr.Error(),
				}
			}
			return nil, &jsonRPCError{Code: serverError, Message: wired.Message, Data: data}
		}
		return nil, &jsonRPCError{Code: codeFor(herr), Message: herr.Error()}
	}
	return res, nil
}
