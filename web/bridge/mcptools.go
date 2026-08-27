package bridge

import (
	"encoding/json"
	"net/http"
)

// mcpToolDefinitions returns the six MCP tool definitions with JSON
// Schema inputSchema blocks. This is a stub — Task 4 fills in the full
// implementations.
func mcpToolDefinitions() []map[string]any {
	return []map[string]any{
		{"name": "spawn", "description": "Submit a task to the fleet", "inputSchema": map[string]any{"type": "object"}},
		{"name": "status", "description": "Check an agent's status", "inputSchema": map[string]any{"type": "object"}},
		{"name": "result", "description": "Get an agent's result", "inputSchema": map[string]any{"type": "object"}},
		{"name": "send", "description": "Send a message to an agent", "inputSchema": map[string]any{"type": "object"}},
		{"name": "cancel", "description": "Cancel an agent", "inputSchema": map[string]any{"type": "object"}},
		{"name": "list", "description": "List the caller's agents", "inputSchema": map[string]any{"type": "object"}},
	}
}

// mcpToolCall dispatches one tools/call. Stub — Task 4 implements it.
func (s *Server) mcpToolCall(w http.ResponseWriter, r *http.Request, c MCPClient, req rpcRequest) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeRPC(w, req.ID, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	writeRPC(w, req.ID, nil, &rpcError{rpcInvalidParams, "unknown tool " + p.Name})
}
