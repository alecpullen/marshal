package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// decodeRequest reads and decodes the JSON-RPC request from an HTTP request.
func decodeRequest(t *testing.T, r *http.Request) Request {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

// writeJSONResult writes a JSON-RPC success response.
func writeJSONResult(w http.ResponseWriter, id json.Number, result any) {
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: id, Result: raw})
}

// TestHTTPClientRoundTrip exercises the full initialize handshake followed by
// tools/list and tools/call against a Streamable-HTTP handler.
func TestHTTPClientRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var sessionOnList string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch req.Method {
		case "initialize":
			w.Header().Set(mcpSessionIDHeader, "sess-123")
			writeJSONResult(w, req.ID, InitializeResult{ProtocolVersion: "2024-11-05"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			sessionOnList = r.Header.Get(mcpSessionIDHeader)
			mu.Unlock()
			writeJSONResult(w, req.ID, ListToolsResult{Tools: []MCPTool{{
				Name:        "hello",
				Description: "says hi",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}}})
		case "tools/call":
			writeJSONResult(w, req.ID, CallToolResult{Content: []MCPContent{{Type: "text", Text: "hi there"}}})
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "hello" {
		t.Fatalf("tools/list returned %+v, want one tool named hello", list.Tools)
	}

	var call CallToolResult
	if err := c.Call(context.Background(), "tools/call", CallToolParams{Name: "hello"}, &call); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if len(call.Content) != 1 || call.Content[0].Text != "hi there" {
		t.Fatalf("tools/call returned %+v, want text 'hi there'", call.Content)
	}

	mu.Lock()
	got := sessionOnList
	mu.Unlock()
	if got != "sess-123" {
		t.Errorf("tools/list carried session id %q, want %q", got, "sess-123")
	}
}

// TestHTTPClientSSEFraming verifies that a text/event-stream response is
// parsed and the matching JSON-RPC message is returned.
func TestHTTPClientSSEFraming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"tools":[{"name":"sse-tool"}]}`),
		})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list over SSE: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "sse-tool" {
		t.Fatalf("SSE tools/list returned %+v, want one tool named sse-tool", list.Tools)
	}
}

// TestHTTPClientSSEMultiline verifies multi-line data: continuation per the
// SSE spec.
func TestHTTPClientSSEMultiline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"tools":[{"name":"multi"}]}`),
		})
		// Split the JSON across two data: lines; they join with a newline.
		// Split after the opening brace so the joined payload stays valid
		// JSON (a newline between tokens is insignificant whitespace).
		fmt.Fprintf(w, "data: %s\ndata: %s\n\n", raw[:1], raw[1:])
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list over multi-line SSE: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "multi" {
		t.Fatalf("multi-line SSE returned %+v, want one tool named multi", list.Tools)
	}
}

// TestHTTPClientSessionIDPropagation verifies the session id returned by the
// initialize response is echoed on subsequent requests.
func TestHTTPClientSessionIDPropagation(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		mu.Lock()
		seen = append(seen, r.Header.Get(mcpSessionIDHeader))
		mu.Unlock()
		switch req.Method {
		case "initialize":
			w.Header().Set(mcpSessionIDHeader, "abc-789")
			writeJSONResult(w, req.ID, InitializeResult{})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			writeJSONResult(w, req.ID, ListToolsResult{})
		}
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 3 {
		t.Fatalf("expected at least 3 requests, saw %d", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("initialize request carried session id %q, want empty", seen[0])
	}
	if seen[1] != "abc-789" {
		t.Errorf("initialized notification carried session id %q, want abc-789", seen[1])
	}
	if seen[2] != "abc-789" {
		t.Errorf("tools/list carried session id %q, want abc-789", seen[2])
	}
}

// TestHTTPClientJSONRPCError verifies a JSON-RPC error is surfaced with the
// stdio client's wording.
func TestHTTPClientJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32601, Message: "nope"},
		})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MCP error (-32601): nope") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "MCP error (-32601): nope")
	}
}

// TestHTTPClientNon2xx verifies a non-2xx status is reported with the status
// code and a body snippet.
func TestHTTPClientNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	err := c.Call(context.Background(), "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to include the body snippet", err.Error())
	}
}

// TestHTTPClientCallAfterClose verifies Call returns ErrClientClosed once the
// client has been closed.
func TestHTTPClientCallAfterClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResult(w, json.Number("1"), ListToolsResult{})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, nil)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close must be idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	err := c.Call(context.Background(), "tools/list", nil, nil)
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("Call after Close = %v, want ErrClientClosed", err)
	}
}

// TestHTTPClientContextCancellation verifies Call honours ctx cancellation and
// returns promptly against a blocking server.
func TestHTTPClientContextCancellation(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)

	c := NewHTTPClient("remote", srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Call(ctx, "tools/list", nil, nil)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want a context error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Call took %v, want it to return promptly", elapsed)
	}
}

// TestHTTPClientCustomHeaders verifies configured headers reach the server.
func TestHTTPClientCustomHeaders(t *testing.T) {
	var mu sync.Mutex
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		req := decodeRequest(t, r)
		writeJSONResult(w, req.ID, ListToolsResult{})
	}))
	defer srv.Close()

	c := NewHTTPClient("remote", srv.URL, map[string]string{"Authorization": "Bearer x"})
	var list ListToolsResult
	if err := c.Call(context.Background(), "tools/list", nil, &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "Bearer x" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer x")
	}
}
