package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	f := testFleet(t)
	return NewServer(f)
}

func testServerWithClient(t *testing.T, c MCPClient) (*Server, string) {
	t.Helper()
	f := testFleet(t)
	if c.ID == "" {
		c.ID = "c1"
	}
	c.OwnerID = DefaultOwnerID
	plain, hash, err := NewClientToken()
	if err != nil {
		t.Fatal(err)
	}
	c.TokenHash = hash
	if err := f.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	return NewServer(f), plain
}

func TestMCPRejectsUnauthenticatedRequests(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))

	s.ServeHTTP(rec, req)

	// /mcp sits outside /api/, so nothing in front of the mux guards it: this
	// endpoint MUST reject on its own. A 200 here means agent-spawning is open
	// to the network.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d for an unauthenticated /mcp request, want 401", rec.Code)
	}
}

// TestMCPRouteReachesTheHandler guards the routing trap: before the
// ServeHTTP change, /mcp fell through to the SPA and answered 200 with
// index.html, which would make every auth assertion below vacuous.
func TestMCPRouteReachesTheHandler(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))

	s.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("Content-Type %q — /mcp was served by the SPA, not the handler", ct)
	}
	if strings.Contains(rec.Body.String(), "<!doctype") ||
		strings.Contains(rec.Body.String(), "<html") {
		t.Fatal("/mcp returned HTML; the request never reached mcpHandler")
	}
}

func TestMCPRejectsAnUnknownToken(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer not-a-real-token")

	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d for an unknown token, want 401", rec.Code)
	}
}

func TestMCPRejectsATokenThatNamesNoClient(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer shared-api-token")

	s.ServeHTTP(rec, req)
	// Only a registered client's token is a client identity; accepting any
	// other would defeat per-client autonomy and caps.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d for a token that names no client, want 401", rec.Code)
	}
}

func TestMCPListsToolsForAValidClient(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", Name: "cc", OwnerID: DefaultOwnerID})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)

	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var out struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	want := map[string]bool{"spawn": true, "status": true, "result": true,
		"send": true, "cancel": true, "list": true}
	for _, tool := range out.Result.Tools {
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("tools/list is missing %v", want)
	}
}

func TestMCPUnknownMethodIsAJSONRPCError(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"nope"}`))
	req.Header.Set("Authorization", "Bearer "+token)

	s.ServeHTTP(rec, req)
	var out struct {
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error == nil || out.Error.Code != -32601 {
		t.Fatalf("want JSON-RPC method-not-found (-32601), got %+v", out.Error)
	}
}

// TestMCPStreamableHTTPVersionAndOrigin covers the transport rules the
// 2025-06-18 revision prescribes on this endpoint: the negotiated version, the
// version header on later requests, the notification body, the absence of a
// server-initiated stream, and Origin validation that is not CORS.
func TestMCPStreamableHTTPVersionAndOrigin(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})

	post := func(body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	// Initialization answers with the version this endpoint speaks, even when
	// the client asked for another revision, and advertises it in the header.
	rec := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"test"}}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize = %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get(mcpProtocolVersionHeader); got != mcpProtocolVersion {
		t.Fatalf("initialize %s = %q, want %q", mcpProtocolVersionHeader, got, mcpProtocolVersion)
	}
	var init struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &init); err != nil {
		t.Fatal(err)
	}
	if init.Result.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("initialize negotiated %q, want the supported %q", init.Result.ProtocolVersion, mcpProtocolVersion)
	}
	if _, ok := init.Result.Capabilities["tools"]; !ok {
		t.Fatalf("initialize advertised %v, want a tools capability", init.Result.Capabilities)
	}

	// Initialization is where the version is chosen, so a client that carries
	// another revision's header on that one request is negotiated with rather
	// than refused: otherwise a version-pinned client could never get in.
	rec = post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		map[string]string{mcpProtocolVersionHeader: "2025-03-26"})
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize carrying another revision's header = %d %s, want 200", rec.Code, rec.Body)
	}

	// The negotiated version on a later request is accepted, with the headers
	// the transport requires.
	rec = post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{
		mcpProtocolVersionHeader: mcpProtocolVersion,
		"Content-Type":           "application/json",
		"Accept":                 "application/json, text/event-stream",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list with the negotiated version = %d %s", rec.Code, rec.Body)
	}
	// A version this bridge does not speak is refused.
	rec = post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, map[string]string{mcpProtocolVersionHeader: "2024-11-05"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tools/list with an unsupported version = %d, want 400", rec.Code)
	}
	// A legacy client that sends no version header keeps working.
	rec = post(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list without a version header = %d, want 200", rec.Code)
	}

	// Content-Type and Accept are checked as the transport prescribes.
	if rec = post(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, map[string]string{"Content-Type": "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON Content-Type = %d, want 415", rec.Code)
	}
	if rec = post(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, map[string]string{"Accept": "text/html"}); rec.Code != http.StatusNotAcceptable {
		t.Fatalf("unacceptable Accept = %d, want 406", rec.Code)
	}

	// A notification is accepted with no response body.
	rec = post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("notification response body = %q, want none", rec.Body)
	}
	// A notifications/* method is a notification whatever id it carried: it
	// is not answered with a result, and it is not a method-not-found.
	rec = post(`{"jsonrpc":"2.0","id":9,"method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a notification carrying an id = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("a notification carrying an id answered %q, want no body", rec.Body)
	}

	// There is no server-initiated stream, so GET is answered 405 for POST
	// only, and it discloses nothing.
	getReq := httptest.NewRequest("GET", "/mcp", nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getRec := httptest.NewRecorder()
	s.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp = %d, want 405", getRec.Code)
	}
	if allow := getRec.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Fatalf("GET /mcp Allow = %q, want POST", allow)
	}

	// Origin is validated on its own terms: another origin is refused even
	// with a valid token, and this server's own origin is allowed. A request
	// with no Origin is a non-browser client, which the token covers.
	if rec = post(`{"jsonrpc":"2.0","id":6,"method":"tools/list"}`, map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin = %d, want 403", rec.Code)
	}
	own := httptest.NewRequest("POST", "/mcp", nil)
	if rec = post(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, map[string]string{"Origin": "http://" + own.Host}); rec.Code != http.StatusOK {
		t.Fatalf("this server's own Origin = %d, want 200", rec.Code)
	}

	// None of those responses bypasses authentication.
	anon := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`))
	anonRec := httptest.NewRecorder()
	s.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d, want 401", anonRec.Code)
	}
}
