package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
	if err := s.out.Encode(resp); err != nil {
		return fmt.Errorf("acp: encode response: %w", err)
	}
	return nil
}

func (s *Server) writeError(id *json.RawMessage, code int, message string) error {
	resp := Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}}
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
