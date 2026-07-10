package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
