package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
)

// JSON-RPC 2.0 error codes used by the MCP endpoint.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// authenticateMCP resolves the presented bearer token to a registered
// client.
//
// This endpoint MUST authenticate itself. bearerAuth guards only paths
// under /api/ and passes everything else through so the SPA can be
// served, so a /mcp handler that trusted the middleware would be open to
// anyone who can reach the port.
//
// The shared API token is deliberately not accepted: it carries no
// client identity, and without identity there is no per-client autonomy,
// no caps, and no repo allowlist.
func (s *Server) authenticateMCP(r *http.Request) (MCPClient, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return MCPClient{}, false
	}
	presented := strings.TrimPrefix(h, prefix)
	if presented == "" {
		return MCPClient{}, false
	}
	return MatchClient(s.fleet.Clients(), presented)
}

// mcpHandler serves MCP over JSON-RPC 2.0. Only tools are served: no
// resources, no prompts, no push channel.
func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateMCP(r)
	if !ok {
		unauthorized(w)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{rpcParseError, "parse error"})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, req.ID, nil, &rpcError{rpcInvalidRequest, "invalid request"})
		return
	}

	switch req.Method {
	case "initialize":
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "marshal-fleet", "version": "1"},
		}, nil)
	case "notifications/initialized":
		// A notification carries no id and expects no response body.
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": mcpToolDefinitions()}, nil)
	case "tools/call":
		s.mcpToolCall(w, r, client, req)
	default:
		writeRPC(w, req.ID, nil, &rpcError{rpcMethodNotFound, "unknown method " + req.Method})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *rpcError) {
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: e})
}
