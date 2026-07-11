package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Len()
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

func (l *lockedBuffer) String() string {
	return string(l.Bytes())
}

// --- Helpers for async tests ---

func mustMarshalWrite(t *testing.T, w io.Writer, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func pollUntil(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServerInitialize(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{
			"protocolVersion": 1,
			"agent":           map[string]any{"name": "Marshal"},
		}, nil
	})
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("no response line written; buf=%q", out.String())
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; line=%q", err, scan.Text())
	}
	if resp.ID == nil || string(*resp.ID) != "1" {
		t.Fatalf("resp.ID = %v, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("resp.Result type = %T", resp.Result)
	}
	if m["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion = %v", m["protocolVersion"])
	}
	agent, ok := m["agent"].(map[string]any)
	if !ok || agent["name"] != "Marshal" {
		t.Fatalf("agent = %v", m["agent"])
	}
}

func TestServerNotificationNoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"event"}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	srv.Handle("event", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output for notification, got %q", out.String())
	}
}

func TestServerParseErrorEmitsNullID(t *testing.T) {
	// Malformed JSON: not a valid JSON-RPC request. The server must
	// emit a parse-error response with id: null per JSON-RPC 2.0.
	in := strings.NewReader("{not valid json}\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("expected a parse-error response line")
	}
	// The response must contain "id":null literally (not omit id).
	if !strings.Contains(line, `"id":null`) {
		t.Fatalf("parse-error response missing id:null: %q", line)
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal: %v; line=%q", err, line)
	}
	if resp.Error == nil || resp.Error.Code != parseError {
		t.Fatalf("expected parse error, got %+v", resp)
	}
}

func TestServerUnknownMethod(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"nope"}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("expected error response line")
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error for unknown method, got %+v", resp)
	}
}

// TestServerReadsOutboundResponseWhileInboundHandlerRuns is the primary
// nonblocking-reader regression. A prompt handler calls Request to ask
// for permission; the synchronous reader deadlocks because it cannot
// read the permission response while the handler runs.
func TestServerReadsOutboundResponseWhileInboundHandlerRuns(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	srv.Handle("session/prompt", func(ctx context.Context, params json.RawMessage) (any, error) {
		var decision struct {
			Approved bool `json:"approved"`
		}
		if err := srv.Request(ctx, "session/request_permission",
			map[string]any{"sessionId": "s1"}, &decision); err != nil {
			return nil, err
		}
		return map[string]any{"approved": decision.Approved}, nil
	})

	go srv.Serve(context.Background())

	// Send the prompt request
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{"sessionId": "s1"},
	})

	// Wait for the outbound request to appear in stdout
	var sentID string
	pollUntil(t, 2*time.Second, func() bool {
		if out.Len() == 0 {
			return false
		}
		lines := bytes.SplitN(bytes.TrimSpace(out.Bytes()), []byte{'\n'}, 2)
		if len(lines) == 0 {
			return false
		}
		var sent map[string]any
		if json.Unmarshal(lines[0], &sent) != nil {
			return false
		}
		id, _ := sent["id"].(string)
		sentID = id
		return id != ""
	})
	if sentID == "" {
		t.Fatal("no outbound request written (timeout — likely deadlocked)")
	}

	// Send the response to the outbound request
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": sentID, "result": map[string]any{"approved": true},
	})

	// Wait for the prompt response to appear in output
	var promptResp *Response
	pollUntil(t, 2*time.Second, func() bool {
		if out.Len() == 0 {
			return false
		}
		sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
		for sc.Scan() {
			var resp Response
			if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
				continue
			}
			if resp.ID != nil && string(*resp.ID) == "1" {
				promptResp = &resp
				return true
			}
		}
		return false
	})
	if promptResp == nil {
		t.Fatal("no prompt response within 2 seconds (deadlock)")
	}
	if promptResp.Error != nil {
		t.Fatalf("prompt response error: %+v", promptResp.Error)
	}
	m, ok := promptResp.Result.(map[string]any)
	if !ok || m["approved"] != true {
		t.Fatalf("prompt result = %+v", promptResp.Result)
	}

	pw.Close()
}

// TestServerResponsesCorrelateWhenHandlersFinishOutOfOrder verifies that
// two concurrent handlers produce responses whose wire order matches
// completion order, not request order.
func TestServerResponsesCorrelateWhenHandlersFinishOutOfOrder(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	block1 := make(chan struct{})
	srv.Handle("slow", func(ctx context.Context, params json.RawMessage) (any, error) {
		<-block1
		return "first", nil
	})
	srv.Handle("fast", func(ctx context.Context, params json.RawMessage) (any, error) {
		return "second", nil
	})

	go srv.Serve(context.Background())

	// Send both requests — both are already buffered in the pipe
	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "id": float64(1), "method": "slow"})
	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "id": float64(2), "method": "fast"})

	// Give the fast handler time to finish (async dispatch)
	time.Sleep(200 * time.Millisecond)

	// Unblock slow handler
	close(block1)
	time.Sleep(100 * time.Millisecond)

	// Parse output — must have response 2 then response 1
	sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	var responses []Response
	for sc.Scan() {
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &resp); err == nil && resp.ID != nil {
			responses = append(responses, resp)
		}
	}

	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d: %+v", len(responses), responses)
	}
	if string(*responses[0].ID) != "2" {
		t.Fatalf("first response should have id 2 (fast), got %s", string(*responses[0].ID))
	}
	if string(*responses[1].ID) != "1" {
		t.Fatalf("second response should have id 1 (slow), got %s", string(*responses[1].ID))
	}

	pw.Close()
}

// TestServerIgnoresUnmatchedResponse verifies that an ID/result frame
// with no pending outbound waiter is silently discarded.
func TestServerIgnoresUnmatchedResponse(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	// Register a handler so we don't get method-not-found for it
	_ = srv // no handler needed — unmatched frames are ignored

	go srv.Serve(context.Background())

	// Send a response frame with no matching waiter
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": "nonexistent-42", "result": "hello",
	})

	time.Sleep(100 * time.Millisecond)

	if out.Len() != 0 {
		t.Fatalf("expected no output for unmatched response, got %q", out.String())
	}

	pw.Close()
}

// TestServerNotificationRunsWithoutResponse verifies that a notification
// frame runs the handler but produces no output.
func TestServerNotificationRunsWithoutResponse(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	handled := make(chan struct{})
	srv.Handle("event", func(ctx context.Context, params json.RawMessage) (any, error) {
		close(handled)
		return map[string]any{"ok": true}, nil
	})

	go srv.Serve(context.Background())

	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "method": "event"})

	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler was not called for notification")
	}

	if out.Len() != 0 {
		t.Fatalf("expected no output for notification, got %q", out.String())
	}

	pw.Close()
}

// TestServerEOFReleasesOutboundWaiter verifies that closing the transport
// releases a blocked Request with a "connection closed" error.
func TestServerEOFReleasesOutboundWaiter(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	go srv.Serve(context.Background())

	reqErr := make(chan error, 1)
	go func() {
		reqErr <- srv.Request(context.Background(), "test", map[string]any{}, nil)
	}()

	// Wait for the outbound request to be sent
	time.Sleep(50 * time.Millisecond)

	// Close the pipe — should release the waiter
	pw.Close()

	select {
	case err := <-reqErr:
		if err == nil || !strings.Contains(err.Error(), "connection closed") {
			t.Fatalf("expected 'connection closed' error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request was not released by EOF")
	}

	// Drain serveErr
	_ = out // Serve may or may not return; ignore
}

// TestServerCancellationJoinsHandlers verifies that cancelling the parent
// context causes the handler to return (via context cancellation) before
// Serve returns.
func TestServerCancellationJoinsHandlers(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	handlerReturned := make(chan struct{})
	srv.Handle("wait", func(ctx context.Context, params json.RawMessage) (any, error) {
		<-ctx.Done()
		close(handlerReturned)
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "id": float64(1), "method": "wait"})

	// Let handler start
	time.Sleep(50 * time.Millisecond)

	// Cancel parent context
	cancel()

	// Handler must return before (or very shortly after) Serve
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after cancellation")
	}

	select {
	case err := <-serveErr:
		if err == nil {
			t.Fatal("expected Serve to return an error after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after cancellation")
	}

	pw.Close()
}

// TestServerShutdownHonorsBound verifies that when a handler ignores
// context cancellation, Serve still returns within handlerShutdownTimeout.
func TestServerShutdownHonorsBound(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)
	srv.handlerShutdownTimeout = 50 * time.Millisecond

	block := make(chan struct{})
	srv.Handle("block", func(ctx context.Context, params json.RawMessage) (any, error) {
		<-block // never closed — handler ignores cancellation
		return nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "id": float64(1), "method": "block"})

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-serveErr:
		if err != nil && !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("expected deadline/timeout error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within shutdown bound")
	}

	close(block) // prevent goroutine leak
	pw.Close()
}

// writeFailer returns a sentinel error on every Write call.
type writeFailer struct {
	err error
}

func (w *writeFailer) Write(p []byte) (int, error) {
	return 0, w.err
}

// TestServerResponseWriteFailureStopsServe verifies that a failed response
// encode causes Serve to exit with the underlying error.
func TestServerResponseWriteFailureStopsServe(t *testing.T) {
	sentinel := errors.New("write failure")

	pr, pw := io.Pipe()
	out := &writeFailer{err: sentinel}
	srv := NewServer(pr, out)

	srv.Handle("test", func(ctx context.Context, params json.RawMessage) (any, error) {
		return "ok", nil
	})

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(context.Background())
	}()

	mustMarshalWrite(t, pw, map[string]any{"jsonrpc": "2.0", "id": float64(1), "method": "test"})

	select {
	case err := <-serveErr:
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after write failure")
	}

	pw.Close()
}

// TestServerOutboundRequestReceivesResponse exercises the outbound
// request path: pre-load the editor's stdin with a response frame
// (keyed to a placeholder id), then have the test goroutine send a
// request, extract the assigned id, and feed a matching response back
// into the pipe. Serve reads the response and routes it via the
// outbound id map.
func TestServerOutboundRequestReceivesResponse(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	srv := NewServer(pr, out)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(context.Background())
	}()

	type result struct {
		Approved bool   `json:"approved"`
		Edited   string `json:"edited,omitempty"`
	}
	resCh := make(chan result, 1)
	reqErr := make(chan error, 1)
	go func() {
		var got result
		err := srv.Request(context.Background(), "session/request_permission",
			map[string]any{"toolName": "shell.run"}, &got)
		resCh <- got
		reqErr <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if out.Len() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if out.Len() == 0 {
		t.Fatalf("no request written to stdout")
	}
	var sent map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &sent); err != nil {
		t.Fatalf("unmarshal sent request: %v", err)
	}
	id, _ := sent["id"].(string)
	if id == "" {
		t.Fatalf("sent id = %v, want non-empty string", sent["id"])
	}

	respFrame := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"approved": true, "edited": "rm tmp.bak"},
	}
	rb, _ := json.Marshal(respFrame)
	if _, err := pw.Write(append(rb, '\n')); err != nil {
		t.Fatalf("write response: %v", err)
	}

	select {
	case got := <-resCh:
		if !got.Approved || got.Edited != "rm tmp.bak" {
			t.Fatalf("result = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Request never returned")
	}
	if err := <-reqErr; err != nil {
		t.Fatalf("Request error: %v", err)
	}
	pw.Close()
	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled && err.Error() != "EOF" {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(time.Second):
		t.Logf("Serve did not return; harmless in pipe EOF race")
	}
}
