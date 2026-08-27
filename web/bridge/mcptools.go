package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// firstNonEmpty returns the first non-empty string, or "" if all are empty.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// mcpToolDefinitions returns the six MCP tool definitions with JSON
// Schema inputSchema blocks.
func mcpToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "spawn",
			"description": "Submit a task to the fleet. Returns a pending submission (awaiting confirmation) or a running agent, plus a URL for the operator.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repoId": map[string]any{"type": "string", "description": "Registered repo id"},
					"ref":    map[string]any{"type": "string", "description": "Git ref to check out (optional)"},
					"title":  map[string]any{"type": "string", "description": "What the operator reads when confirming (required)"},
					"prompt": map[string]any{"type": "string", "description": "Task prompt"},
					"plan":   map[string]any{"type": "string", "description": "Markdown plan in pipeline.ParsePlan format"},
					"mode":   map[string]any{"type": "string", "description": "Permission mode"},
				},
				"required": []string{"repoId", "title"},
			},
		},
		{
			"name":        "status",
			"description": "Check an agent's current status.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agentId": map[string]any{"type": "string"},
				},
				"required": []string{"agentId"},
			},
		},
		{
			"name":        "result",
			"description": "Get an agent's diff and exit readiness.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agentId": map[string]any{"type": "string"},
				},
				"required": []string{"agentId"},
			},
		},
		{
			"name":        "send",
			"description": "Send a message to an agent's session.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agentId": map[string]any{"type": "string"},
					"text":    map[string]any{"type": "string"},
				},
				"required": []string{"agentId", "text"},
			},
		},
		{
			"name":        "cancel",
			"description": "Cancel an agent's in-flight turn.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agentId": map[string]any{"type": "string"},
				},
				"required": []string{"agentId"},
			},
		},
		{
			"name":        "list",
			"description": "List the calling client's agents.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// mcpToolCall dispatches one tools/call.
//
// Every tool is scoped to the calling client. An agent belonging to
// another client is reported as not found rather than as forbidden, so
// the existence of other clients' work is not disclosed.
func (s *Server) mcpToolCall(w http.ResponseWriter, r *http.Request, c MCPClient, req rpcRequest) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeRPC(w, req.ID, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}

	switch p.Name {
	case "spawn":
		s.mcpSpawn(w, r, c, req.ID, p.Arguments)
	case "status":
		s.mcpStatus(w, c, req.ID, p.Arguments)
	case "result":
		s.mcpResult(w, c, req.ID, p.Arguments)
	case "send":
		s.mcpSend(w, r, c, req.ID, p.Arguments)
	case "cancel":
		s.mcpCancel(w, r, c, req.ID, p.Arguments)
	case "list":
		s.mcpList(w, c, req.ID)
	default:
		writeRPC(w, req.ID, nil, &rpcError{rpcInvalidParams, "unknown tool " + p.Name})
	}
}

// mcpSpawn submits through the intake seam and returns immediately.
//
// It never blocks waiting for confirmation: an MCP tool call that waits
// on a human wedges the calling agent. The returned URL is what the
// caller shows its human so they can go and approve.
func (s *Server) mcpSpawn(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a struct {
		RepoID string `json:"repoId"`
		Ref    string `json:"ref"`
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
		Plan   string `json:"plan"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	res, err := s.fleet.Submit(r.Context(), SpawnRequest{
		Origin: OriginMCP, ClientID: c.ID, RepoID: a.RepoID, Ref: a.Ref,
		Title: a.Title, Prompt: a.Prompt, Plan: a.Plan, Mode: a.Mode,
	})
	if err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, err.Error()})
		return
	}
	writeRPC(w, id, map[string]any{
		"agentId": res.AgentID,
		"status":  res.Status,
		"url":     s.publicURL("/agents/" + firstNonEmpty(res.AgentID, res.PendingID)),
	}, nil)
}

// mcpStatus reports an agent's current status, scoped to the calling
// client.
func (s *Server) mcpStatus(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	st, ok := s.agentForClient(a.AgentID, c)
	if !ok {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "agent not found"})
		return
	}
	writeRPC(w, id, map[string]any{
		"agentId": st.ID,
		"status":  st.Status,
		"name":    st.Name,
		"mode":    st.Mode,
	}, nil)
}

// mcpResult reports an agent's diff and exit readiness.
func (s *Server) mcpResult(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	st, ok := s.agentForClient(a.AgentID, c)
	if !ok {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "agent not found"})
		return
	}
	writeRPC(w, id, map[string]any{
		"agentId":      st.ID,
		"status":       st.Status,
		"changedFiles": st.ChangedFiles,
		"isolated":     st.Isolated,
		"branch":       st.Branch,
	}, nil)
}

// mcpSend sends a message to an agent's session.
func (s *Server) mcpSend(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a struct {
		AgentID string `json:"agentId"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	if strings.TrimSpace(a.Text) == "" {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "text is required"})
		return
	}
	rt, ok := s.runtimeForClient(a.AgentID, c)
	if !ok {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "agent not found"})
		return
	}
	go func() {
		// Use a detached context: r.Context() is cancelled as soon as
		// the handler returns, which would abort the prompt before it
		// starts. The REST prompt handler does the same.
		ctx := context.Background()
		if err := rt.reg.Prompt(ctx, rt.sessionID, a.Text); err != nil {
			slog.Warn("mcp send: prompt failed", "agentId", a.AgentID, "err", err)
		}
	}()
	writeRPC(w, id, map[string]any{"status": "sent"}, nil)
}

// mcpCancel cancels an agent's in-flight turn.
func (s *Server) mcpCancel(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "parse arguments"})
		return
	}
	rt, ok := s.runtimeForClient(a.AgentID, c)
	if !ok {
		writeRPC(w, id, nil, &rpcError{rpcInvalidParams, "agent not found"})
		return
	}
	if err := rt.reg.Cancel(r.Context(), rt.sessionID); err != nil {
		writeRPC(w, id, nil, &rpcError{rpcInternalError, err.Error()})
		return
	}
	writeRPC(w, id, map[string]any{"status": "cancelled"}, nil)
}

// mcpList lists the calling client's agents.
func (s *Server) mcpList(w http.ResponseWriter, c MCPClient, id json.RawMessage) {
	var agents []map[string]any
	for _, st := range s.fleet.Snapshot() {
		a, ok := s.fleet.ws.Agent(st.ID)
		if !ok || a.Origin != OriginMCP || a.ClientID != c.ID {
			continue
		}
		agents = append(agents, map[string]any{
			"agentId": st.ID,
			"status":  st.Status,
			"name":    st.Name,
			"title":   a.Name,
		})
	}
	if agents == nil {
		agents = []map[string]any{}
	}
	writeRPC(w, id, map[string]any{"agents": agents}, nil)
}

// agentForClient finds an agent's status, returning false if the agent
// does not exist or belongs to another client. Another client's agent
// is reported as "not found" so the existence of other clients' work is
// not disclosed.
func (s *Server) agentForClient(agentID string, c MCPClient) (AgentStatus, bool) {
	a, ok := s.fleet.ws.Agent(agentID)
	if !ok || a.Origin != OriginMCP || a.ClientID != c.ID {
		return AgentStatus{}, false
	}
	for _, st := range s.fleet.Snapshot() {
		if st.ID == agentID {
			return st, true
		}
	}
	return AgentStatus{}, false
}

// runtimeForClient resolves a live runtime for an agent that belongs to
// the calling client.
func (s *Server) runtimeForClient(agentID string, c MCPClient) (*agentRuntime, bool) {
	a, ok := s.fleet.ws.Agent(agentID)
	if !ok || a.Origin != OriginMCP || a.ClientID != c.ID {
		return nil, false
	}
	rt, err := s.fleet.runtimeForAgent(agentID)
	if err != nil {
		return nil, false
	}
	return rt, true
}
