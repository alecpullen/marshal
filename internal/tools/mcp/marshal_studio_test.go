package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/daemon"
	"marshal/internal/tools/registry"
)

// This file is the built-in Marshal Studio client's proof. Each test drives a
// real unix-socket endpoint through the production enrollment client
// (daemon.Client.EnrollMarshal) and the production MCP transport, with a
// deterministic credential the fixture mints. Nothing here reads the
// developer's config, reaches an ambient daemon or touches the network: the
// socket lives in a short temporary directory and the credential is a string
// the fixture prints.

// studioTestUserDirectionHeader is the header the bridge reads a
// user-direction receipt from. The runtime injects the bridge's own constant;
// the transport itself only knows the name it is given.
const studioTestUserDirectionHeader = "X-Marshal-User-Direction"

const (
	studioTestProjectID = "proj-1"
	studioTestSessionID = "session-1"
)

// studioTestTool is one tool the fixture advertises, with the schema the client
// must copy verbatim.
type studioTestTool struct {
	name        string
	description string
	schema      string
}

// studioTestTools is the fixture's surface. The client must register exactly
// these names with exactly these schemas: nothing is hand-written on this side.
var studioTestTools = []studioTestTool{
	{
		name:        "list_items",
		description: "List the project's factory items.",
		schema:      `{"type":"object","properties":{"projectId":{"type":"string"}},"required":["projectId"]}`,
	},
	{
		name:        "approve_and_queue",
		description: "Approve one item and queue it for implementation.",
		schema:      `{"type":"object","properties":{"projectId":{"type":"string"},"itemId":{"type":"string"},"requestId":{"type":"string"},"expectedVersion":{"type":"integer"}},"required":["projectId","itemId","requestId"]}`,
	},
	{
		name:        "refuse_loudly",
		description: "Refuses with a failure that is not about user direction.",
		schema:      `{"type":"object","properties":{"projectId":{"type":"string"}}}`,
	},
}

// studioTestAccess is the authority the fixture grants a call.
type studioTestAccess int

const (
	studioAccessReadOnly studioTestAccess = iota
	studioAccessUserDirected
)

// studioTestCall is one recorded JSON-RPC call.
type studioTestCall struct {
	Method    string
	Tool      string
	Token     string
	RPCID     string
	RequestID string
	ProjectID string
	Direction string
}

// studioTestFixture is one daemon-shaped endpoint: the enrollment route a
// daemon serves on its socket and the MCP route a bridge serves beside it.
type studioTestFixture struct {
	t      *testing.T
	socket string
	server *http.Server

	mu          sync.Mutex
	enrollments int
	tokens      map[string]bool
	generation  string
	calls       []studioTestCall
	access      studioTestAccess
	mutations   int
	nextReceipt int
	pending     map[string]bool
}

// newStudioTestFixture starts a fixture and returns it, with its listener and
// socket removed at cleanup.
func newStudioTestFixture(t *testing.T) *studioTestFixture {
	t.Helper()
	f := &studioTestFixture{
		t:          t,
		generation: "4242:1700000000000",
		pending:    map[string]bool{},
		tokens:     map[string]bool{},
	}
	// A short directory on purpose: a unix socket path is length-bounded, and
	// t.TempDir() carries the (long) test name.
	dir, err := os.MkdirTemp("", "ms")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f.socket = filepath.Join(dir, "d.sock")

	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", f.socket, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/marshal/enroll", f.enroll)
	mux.HandleFunc("/mcp", f.handleMCP)
	f.server = &http.Server{Handler: mux}
	go func() { _ = f.server.Serve(listener) }()
	t.Cleanup(func() { _ = f.server.Close() })
	return f
}

// enroll is the daemon's enrollment route: it mints one session-scoped
// credential and reports the binding it resolved.
func (f *studioTestFixture) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectRoot string `json:"projectRoot"`
		SessionID   string `json:"sessionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.enrollments++
	token := fmt.Sprintf("studio-credential-%d", f.enrollments)
	f.tokens[token] = true
	sessionID, generation, projectID := req.SessionID, f.generation, studioTestProjectID
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state":      daemon.MarshalStateConnected,
		"instanceId": f.socket,
		"generation": generation,
		"projectId":  projectID,
		"sessionId":  sessionID,
		"token":      token,
		"expiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

// revoke makes every credential the fixture issued stop being accepted, the way
// a daemon restart or an owner change does.
func (f *studioTestFixture) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = map[string]bool{}
}

// setAccess changes the authority the fixture grants.
func (f *studioTestFixture) setAccess(access studioTestAccess) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access = access
}

// recorded returns a copy of the calls the fixture answered.
func (f *studioTestFixture) recorded() []studioTestCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]studioTestCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// mutationCount is how many mutating operations actually ran. A refused or
// unconfirmed operation must never increase it.
func (f *studioTestFixture) mutationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mutations
}

// enrollmentCount is how many credentials the fixture minted.
func (f *studioTestFixture) enrollmentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enrollments
}

// handleMCP is the Streamable-HTTP MCP endpoint, as a bridge serves it.
func (f *studioTestFixture) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch req.Method {
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "initialize":
		f.writeResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "studio-fixture", "version": "1"},
		})
		return
	}
	if !f.tokenAccepted(token) {
		// A rejected attempt is still an attempt: the fixture records it before
		// answering, so a test can see that a retry was the same request.
		if req.Method == "tools/call" {
			f.recordCall(req.ID, req.Params, token, r.Header.Get(studioTestUserDirectionHeader))
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch req.Method {
	case "tools/list":
		f.writeResult(w, req.ID, map[string]any{"tools": f.toolDescriptors()})
	case "tools/call":
		f.callTool(w, req.ID, req.Params, token, r.Header.Get(studioTestUserDirectionHeader))
	default:
		f.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// recordCall appends one tools/call to the ledger.
func (f *studioTestFixture) recordCall(id json.RawMessage, rawParams json.RawMessage, token, direction string) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(rawParams, &params)
	var args struct {
		ProjectID string `json:"projectId"`
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(params.Arguments, &args)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, studioTestCall{
		Method:    "tools/call",
		Tool:      params.Name,
		Token:     token,
		RPCID:     string(id),
		RequestID: args.RequestID,
		ProjectID: args.ProjectID,
		Direction: direction,
	})
}

// tokenAccepted reports whether the fixture still honours a credential. A
// revoked credential is the 401 a revalidation exists for.
func (f *studioTestFixture) tokenAccepted(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return token != "" && f.tokens[token]
}

// toolDescriptors is the surface, with each schema embedded verbatim.
func (f *studioTestFixture) toolDescriptors() []map[string]any {
	out := make([]map[string]any, 0, len(studioTestTools))
	for _, tool := range studioTestTools {
		out = append(out, map[string]any{
			"name":        tool.name,
			"description": tool.description,
			"inputSchema": json.RawMessage(tool.schema),
		})
	}
	return out
}

// callTool runs one tool the way the factory surface does: reads-only work is
// allowed by default, a mutation needs the person's direction, and every
// failure is a tool failure payload in the result rather than a protocol error.
func (f *studioTestFixture) callTool(w http.ResponseWriter, id json.RawMessage, rawParams json.RawMessage, token, direction string) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil {
		f.writeError(w, id, -32602, "invalid params")
		return
	}
	var args struct {
		ProjectID string `json:"projectId"`
		ItemID    string `json:"itemId"`
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(params.Arguments, &args)

	f.mu.Lock()
	f.calls = append(f.calls, studioTestCall{
		Method:    "tools/call",
		Tool:      params.Name,
		Token:     token,
		RPCID:     string(id),
		RequestID: args.RequestID,
		ProjectID: args.ProjectID,
		Direction: direction,
	})
	f.mu.Unlock()

	switch params.Name {
	case "list_items":
		f.writeToolResult(w, id, `{"items":[{"id":"i1","title":"Add the thing"}]}`, false)
	case "refuse_loudly":
		f.writeToolResult(w, id, `{"code":"refused","message":"the factory declined"}`, true)
	case "approve_and_queue":
		f.mu.Lock()
		access := f.access
		admitted := direction != "" && f.pending[direction]
		if admitted {
			delete(f.pending, direction)
			f.mutations++
		}
		receipt := ""
		if access == studioAccessUserDirected && !admitted {
			f.nextReceipt++
			receipt = fmt.Sprintf("receipt-%d", f.nextReceipt)
			f.pending[receipt] = true
		}
		f.mu.Unlock()
		switch {
		case access == studioAccessReadOnly:
			f.writeToolResult(w, id, `{"code":"unauthorized","message":"this session holds read-only access"}`, true)
		case admitted:
			f.writeToolResult(w, id, `{"queued":"i1"}`, false)
		default:
			payload, _ := json.Marshal(map[string]any{
				"code":      "user_direction_required",
				"receipt":   receipt,
				"expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
			})
			f.writeToolResult(w, id, string(payload), true)
		}
	default:
		f.writeError(w, id, -32601, "unknown tool: "+params.Name)
	}
}

func (f *studioTestFixture) writeToolResult(w http.ResponseWriter, id json.RawMessage, text string, isError bool) {
	f.writeResult(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	})
}

func (f *studioTestFixture) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (f *studioTestFixture) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// studioTestCredential is one session's credential cell, wired the way the
// runtime wires it: a per-request token source, one revalidation, and the
// caller's confirmer for an operation that needs the person's direction.
type studioTestCredential struct {
	socket    string
	sessionID string

	mu            sync.Mutex
	enrollment    daemon.MarshalEnrollment
	revalidations int
	challenges    []UserDirectionChallenge
	declined      bool
}

func newStudioTestCredential(t *testing.T, fixture *studioTestFixture, sessionID string) *studioTestCredential {
	t.Helper()
	c := &studioTestCredential{socket: fixture.socket, sessionID: sessionID}
	if err := c.enroll(context.Background()); err != nil {
		t.Fatalf("enroll %s: %v", sessionID, err)
	}
	return c
}

// enroll runs one enrollment through the production client, exactly as
// enrollMarshalStudio does.
func (c *studioTestCredential) enroll(ctx context.Context) error {
	enrollment, err := daemon.NewClient(c.socket).EnrollMarshal(ctx, "/project", c.sessionID)
	c.mu.Lock()
	c.enrollment = enrollment
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if enrollment.Status.State != daemon.MarshalStateConnected || enrollment.Token == "" {
		return fmt.Errorf("enrollment state %q", enrollment.Status.State)
	}
	return nil
}

// tokenSource is the per-request credential source the transport consults.
func (c *studioTestCredential) tokenSource(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enrollment.Token == "" {
		return "", errors.New("studio test: no enrollment")
	}
	return c.enrollment.Token, nil
}

// revalidate re-runs enrollment once, as a 401 or a transport failure does.
func (c *studioTestCredential) revalidate(ctx context.Context) error {
	c.mu.Lock()
	c.revalidations++
	c.mu.Unlock()
	return c.enroll(ctx)
}

// confirm is the caller's confirmer: it records the challenge it was shown and
// returns the receipt to present.
func (c *studioTestCredential) confirm(_ context.Context, challenge UserDirectionChallenge) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.challenges = append(c.challenges, challenge)
	if c.declined {
		return "", errors.New("studio test: the person declined")
	}
	return challenge.Receipt, nil
}

func (c *studioTestCredential) confirmations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.challenges)
}

func (c *studioTestCredential) revalidationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revalidations
}

func (c *studioTestCredential) options() MarshalStudioOptions {
	return MarshalStudioOptions{
		HTTPClient:           studioTestHTTPClient(c.socket),
		TokenSource:          c.tokenSource,
		Revalidate:           c.revalidate,
		UserDirectionHeader:  studioTestUserDirectionHeader,
		ConfirmUserDirection: c.confirm,
		Timeout:              5 * time.Second,
	}
}

// studioTestHTTPClient dials the fixture's socket. The endpoint's host is a
// placeholder, so nothing is ever resolved.
func studioTestHTTPClient(socket string) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}}
}

// studioTestLogger keeps the fixture's transport quiet.
func studioTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// studioTestRegistry attaches one session's built-in client and returns the
// registry its tools were discovered into.
func studioTestRegistry(t *testing.T, credential *studioTestCredential) (*Manager, *registry.Registry) {
	t.Helper()
	cfg := config.Config{}
	manager := NewManager(&cfg, WithManagerLogger(studioTestLogger()))
	if err := manager.AttachMarshalStudio(context.Background(), credential.options()); err != nil {
		t.Fatalf("attach the built-in client: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	reg := registry.New()
	if err := manager.RegisterTools(reg); err != nil {
		t.Fatalf("register tools: %v", err)
	}
	return manager, reg
}

// studioTestToolNames lists the built-in tools a registry holds.
func studioTestToolNames(reg *registry.Registry) []string {
	var names []string
	for _, tool := range reg.List() {
		if strings.HasPrefix(tool.Name, "mcp."+MarshalStudioServerName+".") {
			names = append(names, tool.Name)
		}
	}
	return names
}

// dispatch drives one tool through the registry, the way the agent loop does.
func dispatch(t *testing.T, reg *registry.Registry, name string, args any) (registry.ToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode %s arguments: %v", name, err)
	}
	return reg.Dispatch(context.Background(), registry.ToolCall{ID: "call-1", Name: name, Args: raw})
}

// TestMarshalStudioToolsReadDefault proves a session that enrolled reads the
// factory surface with its own identity: the tools are the endpoint's own
// names and schemas, a read succeeds and returns the endpoint's bounded result,
// and an operation outside the default read-only access is refused without
// running.
func TestMarshalStudioToolsReadDefault(t *testing.T) {
	fixture := newStudioTestFixture(t)
	credential := newStudioTestCredential(t, fixture, studioTestSessionID)
	_, reg := studioTestRegistry(t, credential)

	names := studioTestToolNames(reg)
	want := []string{
		"mcp." + MarshalStudioServerName + ".approve_and_queue",
		"mcp." + MarshalStudioServerName + ".list_items",
		"mcp." + MarshalStudioServerName + ".refuse_loudly",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registered tools = %v, want %v", names, want)
	}

	// The schema is the endpoint's, byte for byte: nothing is hand-written on
	// the client side.
	for _, advertised := range studioTestTools {
		tool, ok := reg.Lookup("mcp." + MarshalStudioServerName + "." + advertised.name)
		if !ok {
			t.Fatalf("%s did not register", advertised.name)
		}
		var got, wantSchema any
		if err := json.Unmarshal(tool.Schema, &got); err != nil {
			t.Fatalf("%s schema is not JSON: %v", advertised.name, err)
		}
		if err := json.Unmarshal([]byte(advertised.schema), &wantSchema); err != nil {
			t.Fatalf("%s fixture schema is not JSON: %v", advertised.name, err)
		}
		if !reflect.DeepEqual(got, wantSchema) {
			t.Fatalf("%s schema = %s, want %s", advertised.name, tool.Schema, advertised.schema)
		}
		if tool.Deferred {
			t.Fatalf("%s was registered deferred", advertised.name)
		}
	}

	res, err := dispatch(t, reg, "mcp."+MarshalStudioServerName+".list_items", map[string]any{
		"projectId": studioTestProjectID,
	})
	if err != nil {
		t.Fatalf("read the items: %v", err)
	}
	if !strings.Contains(res.Content, `"i1"`) || !strings.Contains(res.Content, "Add the thing") {
		t.Fatalf("read result = %q", res.Content)
	}

	// Read-only is the default: the mutation is refused and nothing ran.
	if _, err := dispatch(t, reg, "mcp."+MarshalStudioServerName+".approve_and_queue", map[string]any{
		"projectId": studioTestProjectID, "itemId": "i1", "requestId": "req-1", "expectedVersion": 1,
	}); err == nil {
		t.Fatal("a mutation was admitted under the default read-only access")
	} else if !errors.Is(err, ErrMarshalStudioUnauthorized) {
		t.Fatalf("refusal = %v, want a clear unauthorized cause", err)
	}
	if got := fixture.mutationCount(); got != 0 {
		t.Fatalf("mutations = %d, want 0", got)
	}
	// Every call carried this session's credential, never an anonymous one.
	for _, call := range fixture.recorded() {
		if call.Token != credential.enrollment.Token {
			t.Fatalf("%s carried token %q, want this session's", call.Tool, call.Token)
		}
	}
}

// TestMarshalStudioToolsPermissions proves the authority is the endpoint's and
// the only retry is the one a person's direction admits: a refusal for another
// reason is never retried, and an operation that needs direction is confirmed
// once and then presented in the header.
func TestMarshalStudioToolsPermissions(t *testing.T) {
	fixture := newStudioTestFixture(t)
	credential := newStudioTestCredential(t, fixture, studioTestSessionID)
	_, reg := studioTestRegistry(t, credential)
	approve := "mcp." + MarshalStudioServerName + ".approve_and_queue"

	// Still read-only: a refusal that is not about user direction is returned
	// as it is, and the confirmer is never consulted.
	if _, err := dispatch(t, reg, approve, map[string]any{
		"projectId": studioTestProjectID, "itemId": "i1", "requestId": "req-ro", "expectedVersion": 1,
	}); err == nil {
		t.Fatal("a read-only session mutated the factory")
	}
	if got := credential.confirmations(); got != 0 {
		t.Fatalf("confirmations = %d, want 0 for a refusal that is not user direction", got)
	}
	if got := fixture.mutationCount(); got != 0 {
		t.Fatalf("mutations = %d, want 0", got)
	}

	// A failure payload with another code is not retried either.
	before := len(fixture.recorded())
	if _, err := dispatch(t, reg, "mcp."+MarshalStudioServerName+".refuse_loudly", map[string]any{
		"projectId": studioTestProjectID,
	}); err == nil {
		t.Fatal("refuse_loudly reported success")
	}
	if got, want := len(fixture.recorded()), before+1; got != want {
		t.Fatalf("calls after an unrelated refusal = %d, want %d (it must not be retried)", got, want)
	}
	if got := credential.confirmations(); got != 0 {
		t.Fatalf("confirmations = %d after an unrelated refusal, want 0", got)
	}

	// The person's direction is now available: the same operation is confirmed
	// once, presented in the header, and runs exactly once.
	fixture.setAccess(studioAccessUserDirected)
	if _, err := dispatch(t, reg, approve, map[string]any{
		"projectId": studioTestProjectID, "itemId": "i1", "requestId": "req-1", "expectedVersion": 1,
	}); err != nil {
		t.Fatalf("the directed operation: %v", err)
	}
	if got := credential.confirmations(); got != 1 {
		t.Fatalf("confirmations = %d, want 1", got)
	}
	if got := fixture.mutationCount(); got != 1 {
		t.Fatalf("mutations = %d, want 1", got)
	}

	var attempts []studioTestCall
	for _, call := range fixture.recorded() {
		if call.Tool == "approve_and_queue" && call.RequestID == "req-1" {
			attempts = append(attempts, call)
		}
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (the refusal and the presented receipt)", len(attempts))
	}
	if attempts[0].Direction != "" {
		t.Fatalf("the first attempt presented a receipt: %q", attempts[0].Direction)
	}
	if attempts[1].Direction == "" {
		t.Fatal("the retry presented no receipt")
	}
	if attempts[0].RPCID != attempts[1].RPCID || attempts[0].RequestID != attempts[1].RequestID {
		t.Fatalf("the retry was a new operation: ids %q/%q and requests %q/%q",
			attempts[0].RPCID, attempts[1].RPCID, attempts[0].RequestID, attempts[1].RequestID)
	}
	// The confirmation names the operation the person was shown.
	challenge := credential.challenges[0]
	if challenge.Tool != "approve_and_queue" || challenge.Receipt == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	if got, _ := challenge.Arguments["requestId"].(string); got != "req-1" {
		t.Fatalf("challenge requestId = %q", got)
	}
}

// TestMarshalStudioToolsSessionIsolation proves two sessions never share a
// token or a client identity: each presents its own enrollment, and the
// endpoint sees them as the sessions they are.
func TestMarshalStudioToolsSessionIsolation(t *testing.T) {
	fixture := newStudioTestFixture(t)
	first := newStudioTestCredential(t, fixture, "session-a")
	second := newStudioTestCredential(t, fixture, "session-b")
	if first.enrollment.Token == second.enrollment.Token {
		t.Fatal("two sessions were issued one credential")
	}
	_, firstReg := studioTestRegistry(t, first)
	_, secondReg := studioTestRegistry(t, second)

	read := "mcp." + MarshalStudioServerName + ".list_items"
	if _, err := dispatch(t, firstReg, read, map[string]any{"projectId": studioTestProjectID}); err != nil {
		t.Fatalf("session a reads: %v", err)
	}
	if _, err := dispatch(t, secondReg, read, map[string]any{"projectId": studioTestProjectID}); err != nil {
		t.Fatalf("session b reads: %v", err)
	}

	tokens := map[string]bool{}
	for _, call := range fixture.recorded() {
		tokens[call.Token] = true
	}
	if len(tokens) != 2 || !tokens[first.enrollment.Token] || !tokens[second.enrollment.Token] {
		t.Fatalf("tokens seen by the endpoint = %v, want exactly the two enrollments", tokens)
	}
	if got := fixture.enrollmentCount(); got != 2 {
		t.Fatalf("enrollments = %d, want 2 (one per session)", got)
	}
	// Each session's registry holds its own client's tools, and neither
	// registry is the other's.
	if names := studioTestToolNames(firstReg); len(names) != len(studioTestTools) {
		t.Fatalf("session a's tools = %v", names)
	}
	if names := studioTestToolNames(secondReg); len(names) != len(studioTestTools) {
		t.Fatalf("session b's tools = %v", names)
	}
}

// TestMarshalStudioToolsOfflineStartup proves an instance that is not there
// costs the session nothing: enrollment reports a status, no built-in tool is
// registered, and every other tool is untouched.
func TestMarshalStudioToolsOfflineStartup(t *testing.T) {
	dir, err := os.MkdirTemp("", "ms")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// Nothing listens on this path: the instance is absent.
	socket := filepath.Join(dir, "absent.sock")

	enrollment, err := daemon.NewClient(socket).EnrollMarshal(context.Background(), "/project", studioTestSessionID)
	if err == nil {
		t.Fatal("enrolling against an absent instance succeeded")
	}
	if enrollment.Status.State != daemon.MarshalStateUnavailable {
		t.Fatalf("enrollment state = %q, want %q", enrollment.Status.State, daemon.MarshalStateUnavailable)
	}
	if enrollment.Token != "" {
		t.Fatal("an unavailable instance issued a credential")
	}

	// No credential means no built-in client: the attach refuses and registers
	// nothing, and the session's own tools still register.
	offline := &studioTestCredential{socket: socket, sessionID: studioTestSessionID}
	manager := NewManager(&config.Config{}, WithManagerLogger(studioTestLogger()))
	opts := offline.options()
	opts.Revalidate = nil
	if err := manager.AttachMarshalStudio(context.Background(), opts); err == nil {
		t.Fatal("attaching without an enrollment succeeded")
	} else if !errors.Is(err, ErrMarshalStudioUnauthorized) && !errors.Is(err, ErrMarshalStudioUnavailable) {
		t.Fatalf("attach error = %v, want a clear unauthorized or unavailable cause", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	reg := registry.New()
	if err := manager.RegisterTools(reg); err != nil {
		t.Fatalf("register tools: %v", err)
	}
	if names := studioTestToolNames(reg); len(names) != 0 {
		t.Fatalf("an offline session registered %v", names)
	}
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "Read a file.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("the session's own tools still register: %v", err)
	}
}

// TestMarshalStudioToolsReconnect proves a credential the instance no longer
// accepts is revalidated once — and that the retry is the same request, not a
// second action.
func TestMarshalStudioToolsReconnect(t *testing.T) {
	fixture := newStudioTestFixture(t)
	credential := newStudioTestCredential(t, fixture, studioTestSessionID)
	_, reg := studioTestRegistry(t, credential)
	read := "mcp." + MarshalStudioServerName + ".list_items"

	// The instance moves on: the credential this session holds is no longer
	// accepted, exactly as after a restart or an owner change.
	fixture.revoke()
	firstToken := credential.enrollment.Token

	if _, err := dispatch(t, reg, read, map[string]any{"projectId": studioTestProjectID}); err != nil {
		t.Fatalf("reading after a revalidation: %v", err)
	}
	if got := credential.revalidationCount(); got != 1 {
		t.Fatalf("revalidations = %d, want 1", got)
	}
	if got := fixture.enrollmentCount(); got != 2 {
		t.Fatalf("enrollments = %d, want 2 (the startup one and the revalidation)", got)
	}
	credential.mu.Lock()
	fresh := credential.enrollment.Token
	credential.mu.Unlock()
	if fresh == "" || fresh == firstToken {
		t.Fatalf("the session did not pick up the revalidated credential: %q", fresh)
	}

	// The call that provoked the revalidation succeeded with the new
	// credential, and it was the same request: same JSON-RPC id, same params.
	var reads []studioTestCall
	for _, call := range fixture.recorded() {
		if call.Tool == "list_items" {
			reads = append(reads, call)
		}
	}
	if len(reads) != 2 {
		t.Fatalf("read attempts = %d, want 2", len(reads))
	}
	if reads[0].RPCID != reads[1].RPCID || reads[0].RequestID != reads[1].RequestID {
		t.Fatalf("the revalidated retry was a new request: %q/%q", reads[0].RPCID, reads[1].RPCID)
	}
	if reads[1].Token != fresh {
		t.Fatalf("the retry carried %q, want the revalidated credential", reads[1].Token)
	}

	// A second call needs no second revalidation.
	if _, err := dispatch(t, reg, read, map[string]any{"projectId": studioTestProjectID}); err != nil {
		t.Fatalf("reading on the revalidated credential: %v", err)
	}
	if got := credential.revalidationCount(); got != 1 {
		t.Fatalf("revalidations = %d after a healthy call, want 1", got)
	}
}

// TestMarshalStudioToolsChildCeiling proves a child agent inherits these tools
// with the session's identity and authority: the child's registry holds the
// session's handler, so its calls carry the session's credential, and it is
// bound by the same ceiling no matter what the call itself claims.
func TestMarshalStudioToolsChildCeiling(t *testing.T) {
	fixture := newStudioTestFixture(t)
	credential := newStudioTestCredential(t, fixture, studioTestSessionID)
	_, parent := studioTestRegistry(t, credential)

	// The child's registry inherits the session's tools the way app.go builds
	// one: a fresh registry, the child's own native tools, then the parent's
	// non-native tools copied verbatim.
	child := registry.New()
	if err := child.Inherit(agent.SubtaskScopeView(parent)); err != nil {
		t.Fatalf("inherit the session's tools: %v", err)
	}
	approve := "mcp." + MarshalStudioServerName + ".approve_and_queue"
	parentTool, ok := parent.Lookup(approve)
	if !ok {
		t.Fatal("the parent registry holds no approve tool")
	}
	childTool, ok := child.Lookup(approve)
	if !ok {
		t.Fatal("the child registry inherited no approve tool")
	}
	if childTool.Risk != parentTool.Risk || string(childTool.Schema) != string(parentTool.Schema) {
		t.Fatal("the inherited tool does not have the session's risk and schema")
	}

	// The inherited tool acts with the session's binding, whatever the call
	// names: the credential is the session's, and its authority is unchanged.
	if _, err := dispatch(t, child, approve, map[string]any{
		"projectId": "another-project", "itemId": "i1", "requestId": "req-child", "expectedVersion": 1,
	}); err == nil {
		t.Fatal("a child widened the session's authority")
	} else if !errors.Is(err, ErrMarshalStudioUnauthorized) {
		t.Fatalf("child refusal = %v, want a clear unauthorized cause", err)
	}
	if got := fixture.mutationCount(); got != 0 {
		t.Fatalf("mutations = %d, want 0 (the child inherited a ceiling, not an authority)", got)
	}
	var childCalls []studioTestCall
	for _, call := range fixture.recorded() {
		if call.Tool == "approve_and_queue" {
			childCalls = append(childCalls, call)
		}
	}
	if len(childCalls) != 1 {
		t.Fatalf("child calls = %d, want 1", len(childCalls))
	}
	if childCalls[0].Token != credential.enrollment.Token {
		t.Fatalf("the child called with %q, want the session's credential", childCalls[0].Token)
	}

	// Inheritance is a copy, never a re-derivation: a child registry's own
	// registration wins and the session's tools are not replaced by it.
	if err := child.Inherit(parent); err != nil {
		t.Fatalf("inheriting twice: %v", err)
	}
	if tool, ok := child.Lookup(approve); !ok || tool.Risk != parentTool.Risk {
		t.Fatal("a second inheritance changed the inherited tool")
	}
}
