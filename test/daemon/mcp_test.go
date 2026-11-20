package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// mcpClientCredential creates one MCP client on the daemon's socket and answers
// the credential POST /api/clients shows once. The route is the bridge's own,
// reached over the owner-only socket, so it needs no other credential.
func mcpClientCredential(t *testing.T, h *daemonHarness) string {
	t.Helper()

	// The address is a name, not a lookup: every request goes to the socket.
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", h.socket)
		},
	}}

	body, err := json.Marshal(map[string]any{"name": "relay-test"})
	if err != nil {
		t.Fatalf("encode client request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/api/clients", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build client request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read client answer: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create MCP client answered %s: %s", resp.Status, raw)
	}

	var created struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode client answer %q: %v", raw, err)
	}
	if created.Token == "" {
		t.Fatalf("the daemon created client %q without a credential", created.ID)
	}
	return created.Token
}

// TestPostMCPOverTheRealSocket: the credential such a client is shown is the one
// /mcp accepts over the daemon's socket, so `marshal mcp` reaches a real
// endpoint rather than a stub.
func TestPostMCPOverTheRealSocket(t *testing.T) {
	h := newDaemonHarness(t)
	credential := mcpClientCredential(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	status, answer, err := h.client.PostMCP(ctx, credential,
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("PostMCP(initialize): %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("PostMCP(initialize) = %d: %s, want 200", status, answer)
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(answer, &initialized); err != nil {
		t.Fatalf("decode initialize answer %q: %v", answer, err)
	}
	if initialized.Result.ProtocolVersion == "" {
		t.Errorf("initialize answered %q, want a negotiated protocol version", answer)
	}

	status, answer, err = h.client.PostMCP(ctx, credential,
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("PostMCP(tools/list): %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("PostMCP(tools/list) = %d: %s, want 200", status, answer)
	}
	var listed struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(answer, &listed); err != nil {
		t.Fatalf("decode tools/list answer %q: %v", answer, err)
	}
	if listed.ID != 2 {
		t.Errorf("tools/list answered id %d, want 2", listed.ID)
	}
	if len(listed.Result.Tools) == 0 {
		t.Errorf("tools/list answered no tools: %s", answer)
	}
}
