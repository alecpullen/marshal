package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
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

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// outboundResult carries either a response or a terminal error to a
// waiter blocked in Request.
type outboundResult struct {
	response *Response
	err      error
}

// Server is a dedicated asynchronous JSON-RPC frame router that keeps
// the transport reader live while handlers execute. Inbound requests
// are classified and dispatched to handler goroutines so that the
// scanner goroutine continues reading; outbound responses (editor
// replies to permission requests) are routed back to the blocking
// Request caller. When the transport closes or the parent context
// cancels, pending handlers are waited on with a bounded timeout.
type Server struct {
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

	// fatalErr is a buffered channel that handler goroutines use to
	// report fatal write errors to the Serve loop. Created in Serve.
	fatalErr chan error
}

func NewServer(stdin io.Reader, stdout io.Writer) *Server {
	return &Server{
		in:                     stdin,
		out:                    json.NewEncoder(stdout),
		handlers:               map[string]Handler{},
		notificationMethods:    map[string]bool{},
		outbound:               map[string]chan outboundResult{},
		handlerShutdownTimeout: defaultHandlerShutdownTimeout,
	}
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
	ch := make(chan outboundResult, 1)
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
			cancel()
			s.failOutbound(errors.New("acp: connection closed"))
			s.closeInput()
			handlerErr := s.waitHandlers()
			// Drain any fatal errors reported during or after shutdown.
			var late []error
		drainLoop1:
			for {
				select {
				case err := <-s.fatalErr:
					late = append(late, err)
				default:
					break drainLoop1
				}
			}
			return joinErrors(ctx.Err(), handlerErr, errors.Join(late...))

		case scanErr := <-scannerDone:
			// Scanner finished (EOF or error).
			cancel()
			s.failOutbound(errors.New("acp: connection closed"))
			s.closeInput()
			handlerErr := s.waitHandlers()

			// Drain any fatal errors reported during shutdown.
			var late []error
		drainLoop2:
			for {
				select {
				case err := <-s.fatalErr:
					late = append(late, err)
				default:
					break drainLoop2
				}
			}

			if ctx.Err() != nil {
				return joinErrors(ctx.Err(), handlerErr, errors.Join(late...))
			}
			if scanErr != nil {
				return joinErrors(scanErr, handlerErr, errors.Join(late...))
			}
			return joinErrors(handlerErr, errors.Join(late...))

		case fatalWrite := <-s.fatalErr:
			// A handler goroutine reported a fatal write error.
			cancel()
			s.failOutbound(errors.New("acp: connection closed"))
			s.closeInput()
			handlerErr := s.waitHandlers()
			// Drain any fatal errors reported after waitHandlers returned.
			var late []error
		drainLoop3:
			for {
				select {
				case err := <-s.fatalErr:
					late = append(late, err)
				default:
					break drainLoop3
				}
			}
			return joinErrors(fatalWrite, handlerErr, errors.Join(late...))

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
		// Notification — no response expected.
		go s.dispatchNotification(ctx, req)
	} else {
		// Request — handler writes a response.
		go s.dispatchRequest(ctx, req)
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
		// waiter already consumed the buffer; nothing to do
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
	}
}

// reportFatal sends a fatal error to the Serve loop via the buffered
// fatalErr channel. The channel has capacity 16 so simultaneous
// reports do not block. Serve drains the channel on shutdown to
// ensure every reported fatal is joined into the final return value.
func (s *Server) reportFatal(err error) {
	s.fatalErr <- err
}

// dispatchRequest runs the handler for an inbound request with an id
// and writes exactly one success/error response. If the response
// encode fails, the error is reported as fatal.
func (s *Server) dispatchRequest(ctx context.Context, req Request) {
	defer s.handlerWG.Done()

	result, err := s.dispatch(ctx, req)
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if err != nil {
		resp.Error = &Error{Code: codeFor(err), Message: err.Error()}
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

func (s *Server) dispatch(ctx context.Context, req Request) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Default().Error("acp: handler panicked",
				"method", req.Method,
				"panic", r,
			)
			result = nil
			err = &jsonRPCError{
				Code:    internalError,
				Message: "internal error: handler panicked",
			}
		}
	}()
	handler, ok := s.handlers[req.Method]
	if !ok {
		return nil, &jsonRPCError{Code: methodNotFound, Message: "method not found: " + req.Method}
	}
	res, herr := handler(ctx, req.Params)
	if herr != nil {
		return nil, &jsonRPCError{Code: codeFor(herr), Message: herr.Error()}
	}
	return res, nil
}
