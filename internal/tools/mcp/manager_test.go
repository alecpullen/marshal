package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

func TestManagerRegistersAndInvokesTools(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"mock": {
			Command: exe,
			Args:    []string{"-test.run=TestManagerRegistersAndInvokesTools"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
		},
	}

	mgr := NewManager(&cfg)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Close()

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool, ok := reg.Lookup("mcp.mock.hello")
	if !ok {
		t.Fatal("tool mcp.mock.hello not registered")
	}

	if tool.Description != "says hello" {
		t.Errorf("description = %q, want 'says hello'", tool.Description)
	}

	res, err := tool.Handler(ctx, registry.ToolCall{Name: "mcp.mock.hello", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("Handler error: %v", err)
	}

	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("content = %q, want containing 'hello world'", res.Content)
	}
}

// TestRegisterTools_SkipsHangingServer verifies that RegisterTools applies a
// per-server timeout and skips unresponsive servers instead of failing the
// entire registration.
func TestRegisterTools_SkipsHangingServer(t *testing.T) {
	switch {
	case os.Getenv("BE_HANGING_SERVER") == "1":
		hangingServerMain()
		return
	case os.Getenv("BE_MOCK_SERVER") == "1":
		mockServerMain()
		return
	}

	// Shorten timeout so the test doesn't wait 10s per hanging server.
	origTimeout := mcpServerTimeout
	mcpServerTimeout = 100 * time.Millisecond
	defer func() { mcpServerTimeout = origTimeout }()

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"hanging": {
			Command: exe,
			Args:    []string{"-test.run=TestRegisterTools_SkipsHangingServer"},
			Env:     map[string]string{"BE_HANGING_SERVER": "1"},
		},
		"good": {
			Command: exe,
			Args:    []string{"-test.run=TestRegisterTools_SkipsHangingServer"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
		},
	}

	mgr := NewManager(&cfg)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Close()

	reg := registry.New()
	start := time.Now()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	elapsed := time.Since(start)

	// Should complete quickly — two servers × 100ms timeout gives ~200ms.
	// Add generous overhead for process startup and shutdown.
	if elapsed > 10*time.Second {
		t.Errorf("RegisterTools took %v, expected < 10s", elapsed)
	}

	// The good server's tool should be registered.
	_, ok := reg.Lookup("mcp.good.hello")
	if !ok {
		t.Error("mcp.good.hello not registered — good server should have succeeded")
	}

	// The hanging server's tool should NOT be registered.
	_, ok = reg.Lookup("mcp.hanging.hello")
	if ok {
		t.Error("mcp.hanging.hello should not be registered — hanging server should have been skipped")
	}
}

// stubCaller is a test stub that implements the caller interface.
type stubCaller struct {
	res CallToolResult
	err error
}

func (s *stubCaller) Call(ctx context.Context, method string, params, result any) error {
	if s.err != nil {
		return s.err
	}
	data, _ := json.Marshal(s.res)
	return json.Unmarshal(data, result)
}

func TestMakeHandlerPropagatesIsError(t *testing.T) {
	m := NewManager(nil)
	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			IsError: true,
			Content: []MCPContent{{Type: "text", Text: "boom"}},
		},
	}, "server", "any")
	res, err := handler(context.Background(), registry.ToolCall{Name: "any", Args: nil})
	if err == nil {
		t.Fatal("expected error when IsError=true, got nil")
	}
	if res.Error == "" {
		t.Errorf("expected ToolResult.Error to be set, got %q", res.Error)
	}
}

// TestManagerLogsCall verifies that makeHandler logs an "mcp call" line
// with server, tool, duration_ms, and error fields.
func TestManagerLogsCall(t *testing.T) {
	var buf bytes.Buffer
	m := NewManager(nil, WithManagerLogger(slog.New(slog.NewTextHandler(&buf, nil))))

	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			Content: []MCPContent{{Type: "text", Text: "ok"}},
		},
	}, "myserver", "mytool")

	_, err := handler(context.Background(), registry.ToolCall{Name: "mytool", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "mcp call") {
		t.Fatalf("expected 'mcp call' in log, got %q", logged)
	}
	if !strings.Contains(logged, "server=myserver") {
		t.Errorf("expected server=myserver in log, got %q", logged)
	}
	if !strings.Contains(logged, "tool=mytool") {
		t.Errorf("expected tool=mytool in log, got %q", logged)
	}
	if !strings.Contains(logged, "duration_ms=") {
		t.Errorf("expected duration_ms= in log, got %q", logged)
	}
	if !strings.Contains(logged, "error=<nil>") {
		t.Errorf("expected error=<nil> in log, got %q", logged)
	}
}

func TestStartRejectsDangerousEnvKey(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"evil": {
			Command: exe,
			Args:    []string{"-test.run=TestStartRejectsDangerousEnvKey"},
			Env: map[string]string{
				"BE_MOCK_SERVER": "1",
				"LD_PRELOAD":     "/tmp/evil.so",
			},
		},
	}
	m := NewManager(&cfg)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected Start to reject LD_PRELOAD env key, got nil")
	}
}

func TestStartRejectsNewlineInEnvValue(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"evil": {
			Command: exe,
			Args:    []string{"-test.run=TestStartRejectsNewlineInEnvValue"},
			Env: map[string]string{
				"BE_MOCK_SERVER": "1",
				"FOO":            "bar\nbaz",
			},
		},
	}
	m := NewManager(&cfg)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected Start to reject newline in env value, got nil")
	}
}

// hangingServerMain is a minimal MCP server that completes the initialize
// handshake but never responds to tools/list (or any subsequent request),
// simulating a hanging server.
func hangingServerMain() {
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
		if req.Method == "initialize" {
			result := InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      Implementation{Name: "hanging-server", Version: "1.0"},
			}
			res := Response{JSONRPC: "2.0", ID: req.ID}
			data, _ := json.Marshal(result)
			res.Result = data
			_ = enc.Encode(res)
			continue
		}
		// Block forever without responding. The parent will kill this process.
		select {}
	}
}
