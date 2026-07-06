package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
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
				ServerInfo: Implementation{Name: "mock-server", Version: "1.0"},
			}
		case "tools/list":
			result = ListToolsResult{
				Tools: []MCPTool{
					{Name: "hello", Description: "says hello", InputSchema: []byte(`{"type":"object"}`)},
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
