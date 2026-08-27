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
	return NewServer(f, "")
}

func testServerWithToken(t *testing.T, token string) *Server {
	t.Helper()
	f := testFleet(t)
	return NewServer(f, token)
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
	return NewServer(f, ""), plain
}

func TestMCPRejectsUnauthenticatedRequests(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))

	s.ServeHTTP(rec, req)

	// bearerAuth only guards /api/, so this endpoint MUST reject on its
	// own. A 200 here means agent-spawning is open to the network.
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

func TestMCPRejectsTheSharedAPIToken(t *testing.T) {
	s := testServerWithToken(t, "shared-api-token")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer shared-api-token")

	s.ServeHTTP(rec, req)
	// The shared bearer is not a client identity; accepting it would
	// defeat per-client autonomy and caps.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d for the shared API token, want 401", rec.Code)
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
