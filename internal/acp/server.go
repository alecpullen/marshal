package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	parseError     = -32700
	methodNotFound = -32601
	internalError  = -32603
)

// nullID is the JSON-RPC "id: null" sentinel used for responses where
// the request id could not be determined (parse errors). JSON-RPC 2.0
// §4.6 requires id to be present and null in that case, but
// *json.RawMessage with omitempty would omit it.
var nullID = json.RawMessage("null")

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

type Server struct {
	in       *bufio.Scanner
	out      *json.Encoder
	handlers map[string]Handler

	// outMu serialises writes to the JSON encoder. Encode is
	// goroutine-safe for individual calls, but our writes interleave
	// notification, response, and outbound-request frames and the
	// underlying bufio.Writer is not safe for concurrent use; the
	// mutex keeps the on-the-wire stream atomic.
	outMu sync.Mutex

	// outbound: pending requests we sent to the connected editor and
	// are waiting for a matching response on. Keyed by the raw JSON
	// id of the request. Guards by outMu; reads happen in Serve while
	// writes happen in Request.
	outbound   map[string]chan *Response
	outboundID uint64
}

func NewServer(stdin io.Reader, stdout io.Writer) *Server {
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Server{
		in:       sc,
		out:      json.NewEncoder(stdout),
		handlers: map[string]Handler{},
		outbound: map[string]chan *Response{},
	}
}

func (s *Server) Handle(method string, fn Handler) {
	s.handlers[method] = fn
}

// Notify emits a JSON-RPC notification frame (no id) to the connected
// client. Prompt turns and permission requests can fire notifications from
// independent goroutines, so the encoder write is guarded by a mutex to
// keep the on-the-wire stream atomic.
func (s *Server) Notify(method string, params any) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return s.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// Request sends an outbound JSON-RPC request to the connected editor and
// blocks until a matching response (by id) is read from stdin, the
// context is cancelled, or the underlying scanner terminates. The
// returned value is the response's Result payload (or the response's
// Error if the editor rejected the request). Outbound ids are generated
// locally and registered in the outbound map; Serve's reader uses the
// map to route matching responses back to the waiting channel.
func (s *Server) Request(ctx context.Context, method string, params any, result any) error {
	s.outMu.Lock()
	s.outboundID++
	id := fmt.Sprintf("marshal-out-%d", s.outboundID)
	ch := make(chan *Response, 1)
	s.outbound[id] = ch
	encErr := s.out.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	s.outMu.Unlock()
	if encErr != nil {
		s.outMu.Lock()
		delete(s.outbound, id)
		s.outMu.Unlock()
		return fmt.Errorf("acp: encode outbound request: %w", encErr)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("acp: outbound request %s: connection closed", method)
		}
		if resp == nil {
			return fmt.Errorf("acp: outbound request %s: nil response", method)
		}
		if resp.Error != nil {
			return fmt.Errorf("acp: %s: %s", method, resp.Error.Message)
		}
		if result == nil || resp.Result == nil {
			return nil
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			return fmt.Errorf("acp: re-encode %s result: %w", method, err)
		}
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("acp: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		s.outMu.Lock()
		delete(s.outbound, id)
		s.outMu.Unlock()
		return ctx.Err()
	}
}

func (s *Server) Serve(ctx context.Context) error {
	for s.in.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(s.in.Text())
		if line == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if writeErr := s.writeError(nil, parseError, "parse error: "+err.Error()); writeErr != nil {
				return writeErr
			}
			continue
		}
		// A frame with an id may be either an inbound request (handled
		// below) or the editor's response to one of our outbound
		// requests. Distinguish by checking the outbound registry.
		if req.ID != nil {
			if s.tryDeliverOutbound(req.ID, line) {
				continue
			}
		}
		if req.ID == nil {
			_, _ = s.dispatch(ctx, req)
			continue
		}
		if err := s.writeResponse(ctx, req); err != nil {
			return err
		}
	}
	if err := s.in.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

// tryDeliverOutbound reports whether the given id matches a pending
// outbound request; if so, it parses the line as a Response and delivers
// it to the waiter. Returns false if no match — Serve should then
// treat the frame as an inbound request. The id is the raw JSON token
// (e.g. `"marshal-out-1"` with surrounding quotes for a string); we
// trim surrounding quotes so the key matches what we stored when
// generating the request.
func (s *Server) tryDeliverOutbound(id *json.RawMessage, line string) bool {
	if id == nil {
		return false
	}
	key := unquoteJSONString(string(*id))
	s.outMu.Lock()
	ch, ok := s.outbound[key]
	if ok {
		delete(s.outbound, key)
	}
	s.outMu.Unlock()
	if !ok {
		return false
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		resp = Response{Error: &Error{Code: parseError, Message: "malformed response: " + err.Error()}}
	}
	select {
	case ch <- &resp:
	default:
	}
	return true
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

func (s *Server) writeResponse(ctx context.Context, req Request) error {
	result, err := s.dispatch(ctx, req)
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if err != nil {
		resp.Error = &Error{Code: codeFor(err), Message: err.Error()}
	} else {
		resp.Result = result
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if err := s.out.Encode(resp); err != nil {
		return fmt.Errorf("acp: encode response: %w", err)
	}
	return nil
}

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

func (s *Server) dispatch(ctx context.Context, req Request) (any, error) {
	handler, ok := s.handlers[req.Method]
	if !ok {
		return nil, &jsonRPCError{Code: methodNotFound, Message: "method not found: " + req.Method}
	}
	result, err := handler(ctx, req.Params)
	if err != nil {
		return nil, &jsonRPCError{Code: codeFor(err), Message: err.Error()}
	}
	return result, nil
}

func codeFor(err error) int {
	if jre, ok := err.(*jsonRPCError); ok {
		return jre.Code
	}
	return internalError
}

type jsonRPCError struct {
	Code    int
	Message string
}

func (e *jsonRPCError) Error() string { return e.Message }
