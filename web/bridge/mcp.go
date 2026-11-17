package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

// mcpProtocolVersion is the one Streamable HTTP revision this endpoint speaks.
// A client that asks for another version during initialization is told this
// one, and a request that names a different version afterwards is refused.
const mcpProtocolVersion = "2025-06-18"

// mcpProtocolVersionHeader is the header the transport carries the negotiated
// version in, on responses and on the client's subsequent requests.
const mcpProtocolVersionHeader = "MCP-Protocol-Version"

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
	principal, ok := s.authenticateMCPPrincipal(r)
	return principal.Client, ok
}

// authenticateMCPPrincipal resolves the bearer token to the identity the request
// acts as: a registered external client, or a built-in session.
//
// A client becomes built-in one way only — it holds a live enrollment whose
// generation is the daemon's current one (marshal_enrollment.go). That binding
// is established by the owner-only socket's enrollment operation and by nothing
// the request carries, so no client can name itself one. An external client
// keeps exactly the grant semantics it had, and a credential that was enrolled
// before a restart holds no binding: it has no grants and is refused here rather
// than left as a token with authority nobody revalidated.
func (s *Server) authenticateMCPPrincipal(r *http.Request) (mcpPrincipal, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return mcpPrincipal{}, false
	}
	presented := strings.TrimPrefix(h, prefix)
	if presented == "" {
		return mcpPrincipal{}, false
	}
	if s == nil || s.fleet == nil {
		return mcpPrincipal{}, false
	}
	client, ok := MatchClient(s.fleet.Clients(), presented)
	if !ok {
		return mcpPrincipal{}, false
	}
	if !strings.HasPrefix(client.ID, marshalClientPrefix) {
		return mcpPrincipal{Client: client}, true
	}
	binding, ok := s.marshalBindingFor(client.ID)
	if !ok {
		// An enrollment's client record whose binding is gone (a restart, an
		// expiry, a revoked generation) is not an external client with a
		// fresh chance: it is a credential that no longer names anything, so
		// it is revoked here and the request fails closed.
		s.revokeMarshalClient(client.ID)
		return mcpPrincipal{}, false
	}
	return mcpPrincipal{Client: client, Binding: binding, Builtin: true}, true
}

// mcpHandler serves MCP over the Streamable HTTP transport: JSON-RPC 2.0
// requests over POST, notifications accepted without a response body, and no
// server-initiated stream, so a GET (or a session DELETE) is answered 405 by
// the mux's method matching. Only tools are served: no resources, no prompts,
// no push channel.
//
// Every operation authenticates here, before any handler sees the request:
// there is no unauthenticated path to a tool, and no tool trusts the shared
// bearer.
func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(mcpProtocolVersionHeader, mcpProtocolVersion)
	if !s.mcpOriginAllowed(r) {
		// The Origin header is validated here, not left to CORS: a browser on
		// another origin is refused outright, so a DNS-rebinding page cannot
		// drive this endpoint through the user's browser.
		writeRPCStatus(w, http.StatusForbidden, nil, nil, &rpcError{rpcInvalidRequest, "origin not allowed"})
		return
	}
	principal, ok := s.authenticateMCPPrincipal(r)
	if !ok {
		unauthorized(w)
		return
	}
	// The principal the transport authenticated travels with the request, so a
	// tool path authorizes against that same identity — the client record, or a
	// built-in session's verified binding — and never against anything a tool
	// argument names.
	r = withMCPPrincipal(r, principal)
	if !mcpContentTypeAccepted(r.Header.Get("Content-Type")) {
		writeRPCStatus(w, http.StatusUnsupportedMediaType, nil, nil, &rpcError{rpcInvalidRequest, "Content-Type must be application/json"})
		return
	}
	if !mcpAcceptAccepted(r.Header.Get("Accept")) {
		writeRPCStatus(w, http.StatusNotAcceptable, nil, nil, &rpcError{rpcInvalidRequest, "Accept must allow application/json"})
		return
	}

	body := http.MaxBytesReader(w, r.Body, factoryRequestLimit)
	var req rpcRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The envelope limit is the factory request limit; the plan and
			// spec inside it keep their own, smaller, combined limit.
			writeRPCStatus(w, http.StatusRequestEntityTooLarge, nil, nil,
				&rpcError{rpcInvalidRequest, "request exceeds the 2 MiB MCP envelope limit"})
			return
		}
		writeRPC(w, nil, nil, &rpcError{rpcParseError, "parse error"})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, req.ID, nil, &rpcError{rpcInvalidRequest, "invalid request"})
		return
	}
	// After initialization the client must send the negotiated version; a
	// version this bridge does not speak is refused rather than answered with
	// a result it would misread. Initialization itself is where the version
	// is chosen, so a client that carries another revision's header on that
	// one request is negotiated with, not refused.
	if req.Method != "initialize" && !mcpVersionAccepted(r.Header.Get(mcpProtocolVersionHeader)) {
		writeRPCStatus(w, http.StatusBadRequest, nil, nil, &rpcError{rpcInvalidRequest, "unsupported " + mcpProtocolVersionHeader})
		return
	}
	id := req.ID
	if len(id) == 0 || string(id) == "null" || strings.HasPrefix(req.Method, "notifications/") {
		// A notification carries no id and expects no response body, and a
		// notifications/* method is one whatever id it carried. The
		// transport prescribes 202 with an empty body for it.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPC(w, id, map[string]any{
			// The negotiated version is always the one this endpoint speaks.
			// A client that requested another revision reads the supported
			// one here and uses it for the rest of the session.
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "marshal-fleet", "version": "1"},
		}, nil)
	case "tools/list":
		writeRPC(w, id, map[string]any{"tools": mcpToolDefinitions()}, nil)
	case "tools/call":
		s.mcpToolCall(w, r, principal.Client, req)
	default:
		writeRPC(w, id, nil, &rpcError{rpcMethodNotFound, "unknown method " + req.Method})
	}
}

// mcpOriginAllowed validates the Origin header for the loopback bridge: a
// browser on this server's own origin may call the endpoint, and any other
// origin is refused. A missing Origin is a non-browser client, which the
// bearer token already covers.
func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	allowed := []string{"http://" + r.Host, "https://" + r.Host}
	if s.publicURLBase != "" {
		if u, err := url.Parse(s.publicURLBase); err == nil && u.Scheme != "" && u.Host != "" {
			allowed = append(allowed, u.Scheme+"://"+u.Host)
		}
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimRight(candidate, "/"), origin) {
			return true
		}
	}
	return false
}

// mcpVersionAccepted reports whether a request's protocol-version header names
// the version this endpoint speaks. An absent header is a client that
// initialized before the header existed.
func mcpVersionAccepted(version string) bool {
	v := strings.TrimSpace(version)
	return v == "" || v == mcpProtocolVersion
}

// mcpContentTypeAccepted reports whether a request body carries JSON. An
// absent Content-Type is accepted: the transport requires it, but a client
// that sent a JSON body has already said what it is.
func mcpContentTypeAccepted(contentType string) bool {
	media := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	if media == "" {
		return true
	}
	media = strings.ToLower(media)
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

// mcpAcceptAccepted reports whether the caller can take the JSON this
// endpoint produces. An absent Accept header is treated as "anything".
func mcpAcceptAccepted(accept string) bool {
	if strings.TrimSpace(accept) == "" {
		return true
	}
	for _, part := range strings.Split(accept, ",") {
		switch strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0])) {
		case "application/json", "application/*", "*/*", "text/event-stream":
			return true
		}
	}
	return false
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *rpcError) {
	writeRPCStatus(w, http.StatusOK, id, result, e)
}

// writeRPCStatus writes one JSON-RPC response with an explicit HTTP status,
// which the transport uses for the failures it answers before a method runs.
func writeRPCStatus(w http.ResponseWriter, status int, id json.RawMessage, result any, e *rpcError) {
	if e != nil {
		writeJSON(w, status, rpcResponse{JSONRPC: "2.0", ID: id, Error: e})
		return
	}
	writeJSON(w, status, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}
