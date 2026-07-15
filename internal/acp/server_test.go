package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Intentionally context-ignoring to exercise the timeout path.
	// This is NOT a recommended handler pattern; real handlers SHOULD
	// select on ctx.Done() alongside their blocking operation.
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

// --- F-CON-52: Non-blocking outbound channel sends ---

func TestDeliverOutboundDoesNotBlockOnFullChannel(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
	}
	id := json.RawMessage(`"abc"`)
	ch := make(chan outboundResult, 1)
	ch <- outboundResult{response: &Response{}} // pre-fill buffer
	s.outbound["abc"] = ch

	done := make(chan bool, 1)
	go func() {
		_ = s.deliverOutbound(&id, []byte(`{"jsonrpc":"2.0","id":"abc","result":{}}`))
		done <- true
	}()
	select {
	case <-done:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("deliverOutbound blocked on a full waiter channel (F-CON-52)")
	}
}

func TestFailOutboundDoesNotBlockOnFullChannel(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
		closed:   true,
	}
	full := make(chan outboundResult, 1)
	full <- outboundResult{response: &Response{}}
	s.outbound["full"] = full
	open := make(chan outboundResult, 1)
	s.outbound["open"] = open

	done := make(chan struct{})
	go func() {
		s.failOutbound(errors.New("test close"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("failOutbound blocked on a full waiter channel (F-CON-52)")
	}
	// The "open" waiter should have received the error.
	select {
	case res := <-open:
		if res.err == nil {
			t.Fatal("open waiter received no error")
		}
	default:
		t.Fatal("open waiter did not receive the fail error")
	}
}

// --- Wire harness used by the integration_test.go TestACPWire* tests ---

// wireHarness drives a Server through real newline-delimited JSON. Input
// is fed via an io.Pipe; output is read by a scanner goroutine that
// decodes each line into a map[string]any. next() has a 2-second
// timeout; close() closes the input pipe and waits for both the
// scanner and Serve to finish, failing the test on any non-nil error.
//
// responses caches response frames (frames with a non-empty id) keyed
// by id, so the test can request a particular id even if the matching
// frame was read by a previous readResponse call.
type wireHarness struct {
	inputWriter *io.PipeWriter
	frames      <-chan map[string]any
	responses   map[string]map[string]any
	scannerErr  <-chan error
	serveErr    <-chan error
}

func newWireHarness(t *testing.T, configure func(*Server)) *wireHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServer(inR, outW)
	if configure != nil {
		configure(srv)
	}
	frames := make(chan map[string]any, 512)
	scannerErr := make(chan error, 1)
	serveErr := make(chan error, 1)

	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var f map[string]any
			if err := json.Unmarshal([]byte(line), &f); err != nil {
				scannerErr <- fmt.Errorf("wire harness: decode frame: %w (line=%q)", err, line)
				return
			}
			frames <- f
		}
		if err := sc.Err(); err != nil {
			scannerErr <- fmt.Errorf("wire harness: scanner: %w", err)
			return
		}
		scannerErr <- nil
	}()

	go func() {
		serveErr <- srv.Serve(context.Background())
		// Close the output pipe once Serve returns so the scanner
		// goroutine sees EOF and can exit.
		_ = outW.Close()
	}()

	return &wireHarness{
		inputWriter: inW,
		frames:      frames,
		responses:   map[string]map[string]any{},
		scannerErr:  scannerErr,
		serveErr:    serveErr,
	}
}

func (h *wireHarness) send(t *testing.T, frame any) {
	t.Helper()
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	b = append(b, '\n')
	if _, err := h.inputWriter.Write(b); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func (h *wireHarness) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case f, ok := <-h.frames:
		if !ok {
			t.Fatalf("frames channel closed before next frame arrived")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for next wire frame")
		return nil
	}
}

// close closes the input pipe and waits for both the scanner and Serve
// to return. The test fails if either returns a non-nil error within a
// 2-second budget.
func (h *wireHarness) close(t *testing.T) {
	t.Helper()
	if err := h.inputWriter.Close(); err != nil {
		t.Fatalf("close input pipe: %v", err)
	}
	for _, name := range []string{"scanner", "serve"} {
		var ch <-chan error
		if name == "scanner" {
			ch = h.scannerErr
		} else {
			ch = h.serveErr
		}
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("wire harness: %s returned non-nil: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("wire harness: %s did not return within 2 seconds of close", name)
		}
	}
}

// TestReportFatalSurfacesAllErrors reproduces F-CON-55: the pre-fix
// reportFatal uses select-default with a capacity-1 channel, silently
// dropping excess fatal errors. Post-fix, a blocking send and a drain
// loop on shutdown surface every reported error.
func TestReportFatalSurfacesAllErrors(t *testing.T) {
	s := &Server{fatalErr: make(chan error, 1)}

	var mu sync.Mutex
	var got []error
	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for err := range s.fatalErr {
			mu.Lock()
			got = append(got, err)
			mu.Unlock()
		}
	}()

	s.reportFatal(errors.New("first"))
	s.reportFatal(errors.New("second"))
	s.reportFatal(errors.New("third"))

	close(s.fatalErr)
	drainWg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("got %d errors, want 3", len(got))
	}
}

// --- F-POL-61: Empty method rejection ---

func TestRequestRejectsEmptyMethod(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
	}
	err := s.Request(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty method, got nil")
	}
	if !strings.Contains(err.Error(), "method") {
		t.Errorf("error should mention 'method', got: %v", err)
	}
}

// --- F-POL-60: MaxLineBytes scanner cap ---

func TestScanLinesRespectsMaxLineBytes(t *testing.T) {
	// The scanner enforces MaxLineBytes when it needs to grow its
	// internal buffer. The default initial buffer is 64 KiB, so the
	// test line must exceed that to force growth.
	s := &Server{MaxLineBytes: 4096} // tiny cap
	big := strings.Repeat("a", 70000) + "\n"
	in := strings.NewReader(big)
	s.in = in
	frames := make(chan []byte, 1)
	done := make(chan error, 1)
	go s.scanLines(context.Background(), frames, done)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("scanner did not error on oversized line")
		}
	case <-time.After(time.Second):
		t.Fatal("scanLines did not return after oversized line")
	}
}

// TestDispatchRecoversFromHandlerPanic reproduces F-BUG-57. Pre-fix
// code lets a panicking handler crash the process. Post-fix, the
// panic is recovered and returned as a JSON-RPC internalError.
func TestDispatchRecoversFromHandlerPanic(t *testing.T) {
	s := &Server{handlers: map[string]Handler{
		"panic": func(ctx context.Context, params json.RawMessage) (any, error) {
			panic("handler bug")
		},
	}}
	req := Request{Method: "panic"}
	// The dispatch function is private; call it directly.
	res, err := s.dispatch(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from panicking handler, got nil")
	}
	if res != nil {
		t.Errorf("result = %v, want nil", res)
	}
	// Convert to JSON-RPC error and check the code.
	rpcErr, ok := err.(*jsonRPCError)
	if !ok {
		t.Fatalf("err type = %T, want *jsonRPCError", err)
	}
	if rpcErr.Code != internalError {
		t.Errorf("code = %d, want %d (internalError)", rpcErr.Code, internalError)
	}
}
