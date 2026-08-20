package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestClientStartContextCancelDoesNotKillChild verifies that cancelling
// the Start context after the process has started does not kill the child
// process (TOOLS-MOD-F5). The child process lifecycle is managed by
// Close(), not by the Start context. With exec.CommandContext, cancelling
// the context would kill the child; with exec.Command it survives.
func TestClientStartContextCancelDoesNotKillChild(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient("mock", exe, []string{"-test.run=TestClientStartContextCancelDoesNotKillChild"}, []string{"BE_MOCK_SERVER=1"})
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Cancel the context — this must NOT kill the child process.
	cancel()

	// Give the process a moment to see if it survives.
	time.Sleep(50 * time.Millisecond)

	// The child should still be alive after context cancellation.
	if client.cmd == nil || client.cmd.Process == nil {
		t.Fatal("child process not started")
	}
	if err := client.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("child process died after context cancel: %v", err)
	}

	// Close should succeed — the process is still alive and managed by Close.
	if err := client.Close(); err != nil {
		t.Fatalf("Close failed after context cancel: %v", err)
	}
}

func TestClientCall(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient("mock", exe, []string{"-test.run=TestClientCall"}, []string{"BE_MOCK_SERVER=1"})
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var res ListToolsResult
	if err := client.Call(ctx, "tools/list", nil, &res); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if len(res.Tools) != 3 || res.Tools[0].Name != "hello" || res.Tools[1].Name != "broken" || res.Tools[2].Name != "goodbye" {
		t.Errorf("unexpected tools: %+v", res)
	}
}

func TestClient_BuildEnv_StripsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")

	c := &Client{Env: []string{"FOO=bar"}}
	env := c.buildChildEnv()
	for _, kv := range env {
		if len(kv) >= 16 && kv[:16] == "ANTHROPIC_API_KEY" {
			t.Errorf("MCP child env leaked ANTHROPIC_API_KEY: %s", kv)
		}
		if len(kv) >= 11 && kv[:11] == "LD_PRELOAD=" {
			t.Errorf("MCP child env leaked LD_PRELOAD: %s", kv)
		}
	}

	// Verify user-supplied safe var is present
	foundFOO := false
	for _, kv := range env {
		if kv == "FOO=bar" {
			foundFOO = true
			break
		}
	}
	if !foundFOO {
		t.Error("MCP child env missing user-supplied FOO=bar")
	}

	// Verify PATH from allowlist is present
	foundPATH := false
	for _, kv := range env {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			foundPATH = true
			break
		}
	}
	if !foundPATH {
		t.Error("MCP child env missing PATH from allowlist")
	}
}

// TestCallReceivesQuotedStringID exercises F-BUG-47: an MCP server that
// echoes the request id as a JSON string must still resolve the pending
// response. Pre-fix code dropped the response because the pending map
// was keyed by int64 but the response id unmarshaled as a string.
func TestCallReceivesQuotedStringID(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	c := NewClient("test", "ignored", nil, nil)
	c.stdin = stdinW
	c.stdout = stdoutR
	c.cmd = nil

	// Start readLoop in the background.
	c.wg.Add(1)
	go c.readLoop()

	// Server goroutine: reads the request from stdin, echoes back a
	// JSON-RPC response whose "id" is a JSON string (quoted).
	serverErr := make(chan error, 1)
	go func() {
		defer stdinR.Close()
		defer stdoutW.Close()

		var req Request
		if err := json.NewDecoder(stdinR).Decode(&req); err != nil {
			serverErr <- fmt.Errorf("server decode: %w", err)
			return
		}

		// Build a response with the id as a JSON string.
		rawID := fmt.Sprintf("%v", req.ID)
		jsonResp := fmt.Sprintf(`{"jsonrpc":"2.0","id":"%s","result":%q}`+"\n",
			rawID, "ok")
		if _, err := io.WriteString(stdoutW, jsonResp); err != nil {
			serverErr <- fmt.Errorf("server write: %w", err)
			return
		}
		serverErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result json.RawMessage
	if err := c.Call(ctx, "ping", nil, &result); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(result) != `"ok"` {
		t.Errorf("unexpected result: %s", string(result))
	}

	if srvErr := <-serverErr; srvErr != nil {
		t.Errorf("server error: %v", srvErr)
	}
}

// TestReadLoopFailPendingDoesNotBlockUnderMu verifies F-CON-53: when the
// read loop fails pending requests at EOF, it must not hold c.mu while
// sending on the per-id channel. Holding the mutex during a send
// deadlocks the client if any pending goroutine also tries to take
// c.mu while receiving.
func TestReadLoopFailPendingDoesNotBlockUnderMu(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	c := NewClient("test", "ignored", nil, nil)
	c.stdin = stdinW
	c.stdout = stdoutR

	// Two pending entries that will both need to receive the EOF error.
	// One of them is full (no reader); the test asserts the loop does not
	// deadlock while failing them.
	c.mu.Lock()
	full := make(chan Response, 1)
	full <- Response{ID: json.Number("1")} // pre-fill the buffer
	open := make(chan Response, 1)
	c.pending[json.Number("1")] = full
	c.pending[json.Number("2")] = open
	c.mu.Unlock()

	// Close stdout so readLoop returns and runs the fail-pending path.
	stdoutW.Close()
	stdinR.Close()

	done := make(chan struct{})
	c.wg.Add(1)
	go func() {
		c.readLoop()
		close(done)
	}()

	select {
	case <-done:
		// Expected: readLoop exited without deadlocking.
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop deadlocked while failing pending (F-CON-53)")
	}
}

// TestClientReadLoopLogsCleanEOF verifies that readLoop logs at INFO level
// on clean EOF (no scanner error).
func TestClientReadLoopLogsCleanEOF(t *testing.T) {
	var buf bytes.Buffer
	c := NewClient("test", "ignored", nil, nil,
		WithClientLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	)
	c.stdin = &nopWriteCloser{}
	c.stdout = &malformedReader{}

	c.wg.Add(1)
	c.readLoop()

	logged := buf.String()
	if !strings.Contains(logged, "level=INFO") {
		t.Fatalf("expected INFO level on clean EOF, got %q", logged)
	}
	if !strings.Contains(logged, "mcp readLoop ended") {
		t.Fatalf("expected 'mcp readLoop ended' in log, got %q", logged)
	}
	if strings.Contains(logged, "err=") {
		t.Fatalf("expected no err attribute on clean EOF, got %q", logged)
	}
}

// TestClientReadLoopLogsScannerError verifies that readLoop logs at WARN
// level when the scanner encounters a real error.
func TestClientReadLoopLogsScannerError(t *testing.T) {
	var buf bytes.Buffer
	c := NewClient("test", "ignored", nil, nil,
		WithClientLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))),
	)
	c.stdin = &nopWriteCloser{}
	c.stdout = &errorReader{}

	c.wg.Add(1)
	c.readLoop()

	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("expected WARN level on scanner error, got %q", logged)
	}
	if !strings.Contains(logged, "mcp readLoop ended") {
		t.Fatalf("expected 'mcp readLoop ended' in log, got %q", logged)
	}
	if !strings.Contains(logged, "err=") {
		t.Fatalf("expected err attribute on scanner error, got %q", logged)
	}
}

// TestClientReadLoopHandlesOversizedLines reproduces the 64KB bufio limit:
// a single MCP response larger than 64KB must be delivered, not poison the
// client with bufio.ErrTooLong.
func TestClientReadLoopHandlesOversizedLines(t *testing.T) {
	big := strings.Repeat("x", 128*1024)
	line := []byte(`{"jsonrpc":"2.0","id":1,"result":{"text":"` + big + `"}}` + "\n")

	c := NewClient("test", "ignored", nil, nil)
	c.stdin = &nopWriteCloser{}
	ch := make(chan Response, 1)
	c.pending[json.Number("1")] = ch
	c.stdout = &oversizedLineReader{data: line}

	c.wg.Add(1)
	c.readLoop()

	select {
	case res := <-ch:
		if res.Error != nil {
			t.Fatalf("oversized line poisoned the client: %s", res.Error.Message)
		}
		if !strings.Contains(string(res.Result), big[:64]) {
			t.Fatalf("oversized payload not delivered; result has %d bytes", len(res.Result))
		}
	default:
		t.Fatal("no response delivered for oversized line")
	}
}

// oversizedLineReader yields its whole payload (one giant JSON-RPC line),
// then EOF.
type oversizedLineReader struct {
	data []byte
	pos  int
}

func (r *oversizedLineReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *oversizedLineReader) Close() error { return nil }

// nopWriteCloser is an io.WriteCloser that discards all writes.
type nopWriteCloser struct{}

func (n *nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (n *nopWriteCloser) Close() error                { return nil }

// malformedReader is an io.ReadCloser that yields a single malformed JSON
// line and then returns io.EOF on subsequent reads.
type malformedReader struct {
	pos int
}

func (m *malformedReader) Read(p []byte) (int, error) {
	if m.pos > 0 {
		return 0, io.EOF
	}
	m.pos++
	data := []byte("{invalid}\n")
	n := copy(p, data)
	return n, nil
}

func (m *malformedReader) Close() error { return nil }

// errorReader is an io.ReadCloser that returns a non-nil error on the first
// read, simulating a scanner error.
type errorReader struct{}

func (e *errorReader) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (e *errorReader) Close() error { return nil }

func mockServerMain() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return
			}
			panic(err)
		}
		var result interface{}
		switch req.Method {
		case "initialize":
			result = InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "mock-server", Version: "1.0"},
			}
		case "tools/list":
			result = ListToolsResult{
				Tools: []MCPTool{
					{Name: "hello", Description: "says hello", InputSchema: []byte(`{"type":"object"}`)},
					{Name: "broken", Description: "unusable schema", InputSchema: []byte(`{"type":123}`)},
					{Name: "goodbye", Description: "says goodbye", InputSchema: []byte(`{"type":"object"}`)},
				},
			}
		case "tools/call":
			result = CallToolResult{
				Content: []MCPContent{
					{Type: "text", Text: "hello world"},
				},
			}
		}
		res := Response{
			JSONRPC: "2.0",
			ID:      req.ID,
		}
		if result != nil {
			data, _ := json.Marshal(result)
			res.Result = data
		}
		_ = enc.Encode(res)
	}
}
