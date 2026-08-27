package bridge

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// callMCPRaw sends a tools/call request and returns the raw RPC response.
func callMCPRaw(t *testing.T, s *Server, token, body string) rpcResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return out
}

// callMCP sends a tools/call spawn request and returns the parsed result.
func callMCP(t *testing.T, s *Server, token, body string) struct {
	Status string `json:"status"`
	URL    string `json:"url"`
} {
	t.Helper()
	out := callMCPRaw(t, s, token, body)
	if out.Error != nil {
		t.Fatalf("tools/call error: %s", out.Error.Message)
	}
	var res struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	b, _ := json.Marshal(out.Result)
	_ = json.Unmarshal(b, &res)
	return res
}

// mustSubmit submits a spawn request and returns the agent id, failing
// the test on error.
func mustSubmit(t *testing.T, f *Fleet, req SpawnRequest) string {
	t.Helper()
	res, err := f.Submit(t.Context(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.AgentID == "" {
		t.Fatalf("Submit returned no agent id: %+v", res)
	}
	return res.AgentID
}

// testAddClient adds a client to the fleet and returns its token.
func testAddClient(t *testing.T, s *Server, c MCPClient) (string, string) {
	t.Helper()
	if c.ID == "" {
		c.ID = "c1"
	}
	c.OwnerID = DefaultOwnerID
	plain, hash, err := NewClientToken()
	if err != nil {
		t.Fatal(err)
	}
	c.TokenHash = hash
	if err := s.fleet.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	return c.ID, plain
}

func TestSpawnToolSubmitsThroughIntake(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, s.fleet, "r1")

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"spawn","arguments":{"repoId":"r1","title":"add a flag","prompt":"do it"}}}`
	res := callMCP(t, s, token, body)

	if res.Status != "pending" {
		t.Fatalf("status = %q, want pending (the client is not autonomous)", res.Status)
	}
	if res.URL == "" {
		t.Fatal("spawn returned no web UI URL; the caller cannot tell its human where to approve")
	}
}

func TestSpawnToolRejectsARawURL(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"spawn","arguments":{"url":"https://evil.example/r.git","title":"x","prompt":"y"}}}`

	out := callMCPRaw(t, s, token, body)
	if out.Error == nil {
		t.Fatal("spawn accepted a raw URL from an MCP client; the registry allowlist was bypassed")
	}
}

func TestListIsScopedToTheCallingClient(t *testing.T) {
	s, tokenA := testServerWithClient(t, MCPClient{ID: "cA", OwnerID: DefaultOwnerID, Autonomous: true})
	_, _ = testAddClient(t, s, MCPClient{ID: "cB", OwnerID: DefaultOwnerID, Autonomous: true})
	registerGitRepo(t, s.fleet, "r1")

	// An agent belonging to client B.
	bID := mustSubmit(t, s.fleet, SpawnRequest{
		Origin: OriginMCP, ClientID: "cB", RepoID: "r1", Title: "b work", Prompt: "y",
	})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{}}}`
	out := callMCPRaw(t, s, tokenA, body)
	if out.Error != nil {
		t.Fatalf("list error: %s", out.Error.Message)
	}
	var res struct {
		Agents []struct {
			AgentID string `json:"agentId"`
		} `json:"agents"`
	}
	b, _ := json.Marshal(out.Result)
	_ = json.Unmarshal(b, &res)
	for _, a := range res.Agents {
		if a.AgentID == bID {
			t.Fatal("client A can see client B's agents")
		}
	}
}

func TestCancelRefusesAnotherClientsAgent(t *testing.T) {
	s, tokenA := testServerWithClient(t, MCPClient{ID: "cA", OwnerID: DefaultOwnerID, Autonomous: true})
	_, _ = testAddClient(t, s, MCPClient{ID: "cB", OwnerID: DefaultOwnerID, Autonomous: true})
	registerGitRepo(t, s.fleet, "r1")
	id := mustSubmit(t, s.fleet, SpawnRequest{
		Origin: OriginMCP, ClientID: "cB", RepoID: "r1", Title: "b", Prompt: "y",
	})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"cancel","arguments":{"agentId":"` + id + `"}}}`
	out := callMCPRaw(t, s, tokenA, body)
	if out.Error == nil {
		t.Fatal("client A cancelled client B's agent")
	}
}

func TestToolCallWithAnUnknownToolIsAnError(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`
	out := callMCPRaw(t, s, token, body)
	if out.Error == nil || out.Error.Code != rpcInvalidParams {
		t.Fatalf("want invalid-params for an unknown tool, got %+v", out.Error)
	}
}
