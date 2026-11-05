package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"marshal/internal/acpwire"
)

// Handlers are what a Conn does with the frames the agent starts.
type Handlers struct {
	// Notify receives every notification except $/progress, in arrival
	// order, on the connection's read goroutine. It must not block on the
	// connection. Nil drops them.
	Notify func(method string, params json.RawMessage)
	// Request answers an agent-to-client request, on its own goroutine. Its
	// context ends when the connection does. Nil answers every request with
	// method not found.
	Request func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Conn is one JSON-RPC connection over a Transport.
type Conn struct {
	t Transport
	h Handlers

	writeMu sync.Mutex

	idMu   sync.Mutex
	lastID int

	pendMu  sync.Mutex
	pending map[string]*call

	failOnce sync.Once
	done     chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	errMu sync.Mutex
	err   error
}

// call is one waiting request.
type call struct {
	ch       chan callResult
	progress func(value json.RawMessage)
}

// callResult is what the read goroutine hands a waiting call.
type callResult struct {
	result json.RawMessage
	err    error
}

// NewConn starts reading t and returns the connection.
func NewConn(t Transport, h Handlers) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		t:       t,
		h:       h,
		pending: make(map[string]*call),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
	}
	go c.readLoop()
	return c
}

// Call sends a request and waits for its response, decoding the result
// into result unless result is nil. A nil params sends no params.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	return c.CallWithProgress(ctx, method, params, result, nil)
}

// CallWithProgress is Call, handing each $/progress value for the request
// to progress, on the read goroutine, before Call returns.
func (c *Conn) CallWithProgress(ctx context.Context, method string, params, result any, progress func(value json.RawMessage)) error {
	if err := c.Err(); err != nil {
		return err
	}

	c.idMu.Lock()
	c.lastID++
	id := c.lastID
	c.idMu.Unlock()

	key := idKey(id)
	waiting := &call{ch: make(chan callResult, 1), progress: progress}

	c.pendMu.Lock()
	c.pending[key] = waiting
	c.pendMu.Unlock()

	frame := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		frame["params"] = params
	}
	body, err := json.Marshal(frame)
	if err != nil {
		c.removePending(key)
		return err
	}
	if err := c.send(body); err != nil {
		// send failed the connection; report its end error when it has one.
		c.removePending(key)
		if cerr := c.Err(); cerr != nil {
			return cerr
		}
		return err
	}

	select {
	case res := <-waiting.ch:
		if res.err != nil {
			return res.err
		}
		if result != nil && len(res.result) > 0 {
			if err := json.Unmarshal(res.result, result); err != nil {
				return err
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(key)
		c.sendCancel(id)
		return ctx.Err()
	case <-c.done:
		c.removePending(key)
		return c.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		frame["params"] = params
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.send(body)
}

// Close ends the connection and closes its transport.
func (c *Conn) Close() error {
	c.fail(nil)
	return nil
}

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} {
	return c.done
}

// Err is nil while the connection lives. Afterwards it is ErrClosed after
// Close, or an error that wraps ErrClosed and the cause.
func (c *Conn) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Error is a JSON-RPC error that carries no acpwire code.
type Error struct {
	Code    int
	Message string
}

// Error returns the message.
func (e *Error) Error() string {
	return e.Message
}

// send writes one frame under its mutex, ending the connection when the
// transport refuses it.
func (c *Conn) send(frame []byte) error {
	c.writeMu.Lock()
	err := c.t.Send(frame)
	c.writeMu.Unlock()
	if err != nil {
		c.fail(err)
		return err
	}
	return nil
}

// sendCancel tells the agent the call named by id is no longer wanted,
// ignoring a send error.
func (c *Conn) sendCancel(id int) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  acpwire.MethodCancelRequest,
		"params":  acpwire.CancelRequestParams{ID: json.RawMessage(fmt.Sprintf("%d", id))},
	})
	if err != nil {
		return
	}
	_ = c.send(body)
}

// removePending drops a waiting call if it is still registered.
func (c *Conn) removePending(key string) {
	c.pendMu.Lock()
	delete(c.pending, key)
	c.pendMu.Unlock()
}

// fail ends the connection once: it records err, closes the transport, fails
// every waiting call, cancels the agent requests' context, and closes Done.
func (c *Conn) fail(cause error) {
	c.failOnce.Do(func() {
		err := error(ErrClosed)
		if cause != nil {
			err = fmt.Errorf("%w: %w", ErrClosed, cause)
		}

		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()

		_ = c.t.Close()
		c.cancel()

		c.pendMu.Lock()
		waiting := make([]*call, 0, len(c.pending))
		for _, pending := range c.pending {
			waiting = append(waiting, pending)
		}
		c.pending = make(map[string]*call)
		c.pendMu.Unlock()

		for _, pending := range waiting {
			pending.ch <- callResult{err: err}
		}

		close(c.done)
	})
}

// readLoop reads frames until the transport ends, handing each to its
// destination.
func (c *Conn) readLoop() {
	for {
		frame, err := c.t.Recv()
		if err != nil {
			c.fail(err)
			return
		}
		c.dispatch(frame)
	}
}

// dispatch classifies one frame: a method with an ID is an agent request, a
// method without one is a notification, and an ID without a method is a
// response. A frame that does not parse is dropped.
func (c *Conn) dispatch(frame []byte) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(frame, &probe); err != nil {
		return
	}
	methodRaw, hasMethod := probe["method"]
	idRaw, hasID := probe["id"]

	if hasMethod {
		var method string
		if err := json.Unmarshal(methodRaw, &method); err != nil {
			return
		}
		if hasID {
			go c.answer(idRaw, method, probe["params"])
			return
		}
		if method == acpwire.MethodProgress {
			c.deliverProgress(probe["params"])
			return
		}
		if c.h.Notify != nil {
			c.h.Notify(method, probe["params"])
		}
		return
	}
	if !hasID {
		return
	}
	c.deliverResponse(idRaw, probe["result"], probe["error"])
}

// deliverResponse hands a response to its waiting call, dropping it when no
// call waits for that ID.
func (c *Conn) deliverResponse(idRaw, resultRaw, errRaw json.RawMessage) {
	key := compactID(idRaw)

	c.pendMu.Lock()
	waiting, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.pendMu.Unlock()
	if !ok {
		return
	}

	res := callResult{result: resultRaw}
	if len(errRaw) > 0 && string(errRaw) != "null" {
		res.err = decodeResponseError(errRaw)
	}
	waiting.ch <- res
}

// deliverProgress hands a $/progress value to the call whose ID it names,
// dropping it when that call no longer waits.
func (c *Conn) deliverProgress(params json.RawMessage) {
	var p acpwire.ProgressParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	key := compactID(p.ID)

	c.pendMu.Lock()
	waiting, ok := c.pending[key]
	var progress func(value json.RawMessage)
	if ok {
		progress = waiting.progress
	}
	c.pendMu.Unlock()

	if progress != nil {
		progress(p.Value)
	}
}

// answer runs one agent request's handler and writes its answer back with
// the request's raw ID.
func (c *Conn) answer(idRaw json.RawMessage, method string, params json.RawMessage) {
	frame := map[string]any{
		"jsonrpc": "2.0",
		"id":      idRaw,
	}

	var result any
	var err error
	if c.h.Request != nil {
		result, err = c.h.Request(c.ctx, method, params)
	} else {
		err = &codeError{code: -32601, message: "method not found: " + method}
	}

	if err != nil {
		frame["error"] = answerError(err)
	} else if result == nil {
		frame["result"] = nil
	} else {
		frame["result"] = result
	}

	body, merr := json.Marshal(frame)
	if merr != nil {
		return
	}
	_ = c.send(body)
}

// answerError shapes a handler's error as the agent reads errors: an
// *acpwire.Error travels as -32000 with its own encoding as data, a coded
// error keeps its code, and anything else is an internal error.
func answerError(err error) map[string]any {
	var coded *codeError
	var wired *acpwire.Error
	switch {
	case errors.As(err, &wired):
		return map[string]any{
			"code":    -32000,
			"message": wired.Message,
			"data":    wired,
		}
	case errors.As(err, &coded):
		return map[string]any{
			"code":    coded.code,
			"message": coded.message,
		}
	default:
		return map[string]any{
			"code":    -32603,
			"message": err.Error(),
		}
	}
}

// decodeResponseError maps a response error object to its Go error: a
// -32000 whose data is an *acpwire.Error with a code is that error, anything
// else is an *Error.
func decodeResponseError(raw json.RawMessage) error {
	var obj struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	if obj.Code == -32000 && len(obj.Data) > 0 {
		var wired acpwire.Error
		if uerr := json.Unmarshal(obj.Data, &wired); uerr == nil && wired.Code != "" {
			return &wired
		}
	}
	return &Error{Code: obj.Code, Message: obj.Message}
}

// codeError is an error with a JSON-RPC code but no acpwire code.
type codeError struct {
	code    int
	message string
}

// Error returns the message.
func (e *codeError) Error() string {
	return e.message
}

// idKey is a request ID's compact JSON, so a numeric client ID and a string
// agent ID can never collide.
func idKey(id int) string {
	return fmt.Sprintf("%d", id)
}

// compactID is a raw JSON ID's compact form.
func compactID(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
