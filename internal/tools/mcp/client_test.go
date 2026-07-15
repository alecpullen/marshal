package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

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

	if len(res.Tools) != 1 || res.Tools[0].Name != "hello" {
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
