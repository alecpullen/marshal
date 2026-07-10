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

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

type Server struct {
	in       *bufio.Scanner
	out      *json.Encoder
	handlers map[string]Handler

	// outMu serialises writes to the JSON encoder. Encode is goroutine-safe
	// for individual calls, but our writes interleave notification frames
	// with response frames and the underlying bufio.Writer is not safe for
	// concurrent use; the mutex keeps the on-the-wire stream atomic.
	outMu sync.Mutex
}

func NewServer(stdin io.Reader, stdout io.Writer) *Server {
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Server{
		in:       sc,
		out:      json.NewEncoder(stdout),
		handlers: map[string]Handler{},
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
