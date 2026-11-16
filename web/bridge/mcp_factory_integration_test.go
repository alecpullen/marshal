package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These three tests are the end-to-end proof of the MCP-to-factory handoff.
// They drive the bridge the way an external client does — a real loopback
// listener, the 2025-06-18 Streamable HTTP transport, JSON-RPC initialize,
// tools/list and tools/call — and then drive the process controller over the
// same journal with fake factory effects. Nothing here reaches a forge, a git
// remote, a container runtime, a model provider or a live credential: the
// factory's effects are the fixture's, and every failure a client could
// provoke is answered inside the JSON-RPC envelope.

// handoffAPIToken is the fixture's shared API token, which stands in for the
// operator's Studio session. It is a test fixture, not a credential.
const handoffAPIToken = "secret"

// errHandoffClientStopped is how a stopped external client reports use after
// it has gone away.
var errHandoffClientStopped = errors.New("handoff client stopped")

// handoffBridge is one bridge listening on a real loopback socket, with a
// count of the requests that reached it. The count is how "the factory
// continued without the client" is asserted instead of assumed.
type handoffBridge struct {
	*mcpFactoryToolFixture
	url      string
	server   *httptest.Server
	http     *http.Client
	requests atomic.Int64
	// loseResponse makes the next request run its handler against a discarded
	// writer: the bridge commits exactly as it always does, and the client
	// reads an empty body, which is a lost response from its side.
	loseResponse atomic.Bool
}

func newHandoffBridge(t *testing.T) *handoffBridge {
	t.Helper()
	fx := newMCPFactoryToolFixture(t, fullGrant())
	b := &handoffBridge{mcpFactoryToolFixture: fx, http: &http.Client{Timeout: 30 * time.Second}}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.requests.Add(1)
		if b.loseResponse.CompareAndSwap(true, false) {
			fx.srv.ServeHTTP(&discardWriter{header: http.Header{}}, r)
			return
		}
		fx.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(b.server.Close)
	b.url = b.server.URL
	return b
}

// discardWriter is an http.ResponseWriter whose response never reaches the
// client. The handler runs to completion — including the journal append — and
// the client reads an empty body.
type discardWriter struct{ header http.Header }

func (d *discardWriter) Header() http.Header         { return d.header }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// handoffProposal is the create_draft argument object these tests send. The
// round allowance is pinned so the approval's grant carries the rounds the
// controller needs to reach the implementer.
func (b *handoffBridge) handoffProposal(requestID string) map[string]any {
	args := b.proposal(requestID)
	args["config"].(map[string]any)["maxRounds"] = 5
	return args
}

// studio issues one Studio (operator surface) request over the same loopback
// listener the external client uses, authenticated with the bridge's shared
// API token. It reports an error rather than failing the test, so a goroutine
// can race it against an MCP call.
func (b *handoffBridge) studio(method, path string, body any) (int, []byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequest(method, b.url+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+handoffAPIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (b *handoffBridge) grantPath() string {
	return "/api/clients/" + url.PathEscape(b.clientID) + "/factory-grants/" + url.PathEscape(b.projectID)
}

// grant reads the grant the Studio editor shows.
func (b *handoffBridge) grant(t *testing.T) FactoryGrantState {
	t.Helper()
	status, raw, err := b.studio(http.MethodGet, b.grantPath(), nil)
	if err != nil {
		t.Fatalf("read the grant: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("read the grant: status %d: %s", status, raw)
	}
	var state FactoryGrantState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode the grant %q: %v", raw, err)
	}
	return state
}

// revoke switches every capability off through the management route, which is
// the write the Studio editor makes.
func (b *handoffBridge) revoke(version string) error {
	status, raw, err := b.studio(http.MethodPut, b.grantPath(), map[string]any{
		"grant":   MCPFactoryProjectGrant{ProjectID: b.projectID, ApprovalAuthority: factoryAuthorityNone},
		"version": version,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("revoke: status %d: %s", status, raw)
	}
	return nil
}

// handoffClient is the external MCP client in these tests: an HTTP client
// speaking JSON-RPC to the bridge's /mcp endpoint over the loopback socket.
type handoffClient struct {
	url     string
	token   string
	http    *http.Client
	nextID  int
	version string
	stopped bool
}

func newHandoffClient(b *handoffBridge, token string) *handoffClient {
	return &handoffClient{
		url:   b.url + "/mcp",
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// stop closes the client's connections and refuses further use: from this
// point the bridge has no external client, which is the state the handoff is
// supposed to survive.
func (c *handoffClient) stop() {
	c.stopped = true
	c.http.CloseIdleConnections()
}

// post sends one JSON-RPC message and returns the HTTP status, the response
// headers and the response body, which is empty for a notification.
func (c *handoffClient) post(body any) (int, http.Header, []byte, error) {
	if c.stopped {
		return 0, nil, nil, errHandoffClientStopped
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.version != "" {
		req.Header.Set(mcpProtocolVersionHeader, c.version)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// rpc sends one request and returns its result.
func (c *handoffClient) rpc(method string, params any) (json.RawMessage, error) {
	c.nextID++
	body := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		body["params"] = params
	}
	status, _, payload, err := c.post(body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d: %s", method, status, payload)
	}
	var out rpcResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("%s: decode %q: %w", method, payload, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: rpc error %d: %s", method, out.Error.Code, out.Error.Message)
	}
	result, err := json.Marshal(out.Result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// initialize runs the transport's opening exchange: the client asks for
// another revision, the bridge negotiates the one it speaks, and the
// notification that follows expects an empty body.
func (c *handoffClient) initialize(t *testing.T) {
	t.Helper()
	c.nextID++
	status, header, payload, err := c.post(map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "handoff-proof", "version": "1"},
		},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("initialize: status %d: %s", status, payload)
	}
	if got := header.Get(mcpProtocolVersionHeader); got != mcpProtocolVersion {
		t.Fatalf("initialize %s = %q, want %q", mcpProtocolVersionHeader, got, mcpProtocolVersion)
	}
	// The answer is a JSON-RPC envelope: the negotiated version and the
	// advertised capabilities live in its result member, not at the top
	// level.
	var envelope struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode initialize %q: %v", payload, err)
	}
	init := envelope.Result
	if init.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("initialize negotiated %q, want %q", init.ProtocolVersion, mcpProtocolVersion)
	}
	if _, ok := init.Capabilities["tools"]; !ok {
		t.Fatalf("initialize advertised %v, want a tools capability", init.Capabilities)
	}
	if init.ServerInfo.Name == "" {
		t.Fatal("initialize named no server")
	}
	c.version = init.ProtocolVersion

	status, _, payload, err = c.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err != nil {
		t.Fatalf("initialized notification: %v", err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("initialized notification: status %d: %s", status, payload)
	}
	if len(payload) != 0 {
		t.Fatalf("initialized notification returned a body %q, want none", payload)
	}
}

// tools lists the tools the bridge advertises, with their schemas.
func (c *handoffClient) tools(t *testing.T) map[string]map[string]any {
	t.Helper()
	result, err := c.rpc("tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	out := make(map[string]map[string]any, len(listed.Tools))
	for _, tool := range listed.Tools {
		out[tool.Name] = tool.InputSchema
	}
	return out
}

// handoffToolCall is the params object of one tools/call.
type handoffToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// call sends one tools/call and decodes the standard MCP tool result.
func (c *handoffClient) call(tool string, args any) (factoryToolReply, map[string]any, error) {
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return factoryToolReply{}, nil, err
	}
	result, err := c.rpc("tools/call", handoffToolCall{Name: tool, Arguments: rawArgs})
	if err != nil {
		return factoryToolReply{}, nil, err
	}
	var reply factoryToolReply
	if err := json.Unmarshal(result, &reply); err != nil {
		return reply, nil, fmt.Errorf("decode %s result: %w", tool, err)
	}
	if len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		return reply, nil, fmt.Errorf("%s returned %d content blocks, want one text block", tool, len(reply.Content))
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(reply.Content[0].Text), &value); err != nil {
		return reply, nil, fmt.Errorf("%s result text is not a JSON object: %w (%s)", tool, err, reply.Content[0].Text)
	}
	return reply, value, nil
}

// mustCall requires a successful domain result and returns it with the raw
// text, so a test can assert nothing the bridge should not disclose is in it.
func (c *handoffClient) mustCall(t *testing.T, tool string, args any) (map[string]any, string) {
	t.Helper()
	reply, value, err := c.call(tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if reply.IsError {
		t.Fatalf("%s: domain failure %v", tool, value)
	}
	return value, reply.Content[0].Text
}

// mustFail requires a domain failure and returns its code and text.
func (c *handoffClient) mustFail(t *testing.T, tool string, args any) (string, string) {
	t.Helper()
	reply, value, err := c.call(tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if !reply.IsError {
		t.Fatalf("%s: succeeded, want a domain failure: %v", tool, value)
	}
	code, _ := value["code"].(string)
	if code == "" {
		t.Fatalf("%s: domain failure carries no code: %v", tool, value)
	}
	return code, reply.Content[0].Text
}

// handoffEffects is the factory's fake effect runner. It records each effect
// the durable state asked for and settles them with fixture evidence: a
// worker for a spawn, Git evidence for seed and publish, a draft PR for
// create_pr, and a question for the implementation. The question channel is
// the barrier that proves the controller reached the worker's question with
// no client attached.
type handoffEffects struct {
	executed chan string
	question chan string
	mu       sync.Mutex
	kinds    []string
}

func newHandoffEffects() *handoffEffects {
	return &handoffEffects{executed: make(chan string, 32), question: make(chan string, 1)}
}

func (f *handoffEffects) Observe(_ context.Context, item FactoryItem) (FactoryObservation, error) {
	obs := FactoryObservation{
		RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true,
		HeadSHA: "head", TreeSHA: "tree", RemoteHead: "head", BaseSHA: "base",
		ObservedAt: time.Now().UTC(),
	}
	if len(item.PRs) > 0 {
		p := item.PRs[len(item.PRs)-1]
		obs.PR = &p
	}
	if item.Decision != nil && item.Decision.Kind == "question" {
		select {
		case f.question <- item.Decision.ID:
		default:
		}
	}
	return obs, nil
}

func (f *handoffEffects) Execute(_ context.Context, item FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	f.mu.Lock()
	f.kinds = append(f.kinds, e.Kind)
	f.mu.Unlock()
	f.executed <- e.Kind

	out := FactoryOutcome{EffectID: e.ID, Confirmed: true, HeadSHA: "head", TreeSHA: "tree", BaseSHA: "base"}
	switch e.Kind {
	case "spawn":
		out.Worker = &FactoryWorker{AgentID: e.ID, SessionID: e.ID, Role: e.WorkerRole, SpawnEffectID: e.ID}
	case "create_pr":
		if len(item.Approvals) == 0 {
			return FactoryOutcome{}, fmt.Errorf("%w: an unapproved item reached create_pr", ErrFactoryRefused)
		}
		out.PR = &FactoryPR{
			Number: 1, HeadRef: item.Branch, BaseRef: item.Approvals[len(item.Approvals)-1].Config.TargetBranch,
			State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base",
		}
	case "implement":
		out.Report = &FactoryReport{
			OperationID: e.ID, Outcome: "needs_input",
			Question: "which branch should the change land on?",
			Reason:   "the plan names two branches",
		}
	}
	return out, nil
}

// seen reports the effects executed so far, for a failure message.
func (f *handoffEffects) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.kinds...)
}

// runFactory starts the process controller over the fixture's journal with
// the fake effects. The returned function stops it and waits for it.
func (b *handoffBridge) runFactory(t *testing.T, effects *handoffEffects) func() {
	t.Helper()
	controller := NewFactoryController(b.store, effects, nil)
	if err := controller.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// waitEffect waits for the factory to dispatch the next effect and requires it
// to be the one the durable state implies.
func waitEffect(t *testing.T, effects *handoffEffects, want string) {
	t.Helper()
	select {
	case got := <-effects.executed:
		if got != want {
			t.Fatalf("the factory dispatched %q, want %q (executed %v)", got, want, effects.seen())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the factory never dispatched %q (executed %v)", want, effects.seen())
	}
}

// studioSocketPath returns an absolute Unix-socket path inside an owner-only
// directory. t.TempDir() is 0700 already; the path is kept clear of sun_path
// (104 bytes on macOS, 108 on Linux) by falling back to a short directory on a
// platform whose temp root is long.
func studioSocketPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir() is not owner-only on every toolchain, and sun_path's
	// directory must be: set the mode rather than assume it.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restrict the socket directory: %v", err)
	}
	if sock := filepath.Join(dir, "s.sock"); len(sock) < 100 {
		return sock
	}
	short, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatalf("short socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	if err := os.Chmod(short, 0o700); err != nil {
		t.Fatalf("restrict the short socket directory: %v", err)
	}
	return filepath.Join(short, "s.sock")
}

// studioUnixClient returns an *http.Client whose transport dials sock. The
// request's host is a name, not a lookup: the socket is the address, exactly
// as the daemon's own client dials it.
func studioUnixClient(sock string) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

// TestStudioIntegrationMCPOverOwnerOnlySocket serves the real bridge Server on
// an owner-only Unix-socket listener — the transport a Marshal session has on
// the local daemon — and drives it with the fixture's MCP client over real
// JSON-RPC: initialize, tools/list, and an authorized read through the real
// handler and the real factory grant.
//
// The production client (internal/tools/mcp.HTTPClient) is proven over the
// same wire in its own package: web/ must not import marshal/internal
// (TestWebIsStdlibOnly has no allowlist), so no single package can span both
// halves of this proof.
func TestStudioIntegrationMCPOverOwnerOnlySocket(t *testing.T) {
	b := newHandoffBridge(t)
	sock := studioSocketPath(t)
	if info, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("stat the socket directory: %v", err)
	} else if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("the socket directory mode is %04o, want owner-only", perm)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("this platform cannot host a Unix socket: %v", err)
	}
	srv := &http.Server{Handler: b.srv}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// The client speaks to the socket; the URL's host never resolves.
	client := &handoffClient{url: "http://marshal-daemon/mcp", token: b.token, http: studioUnixClient(sock)}
	client.initialize(t)

	// Tool discovery over the socket: the real handler's advertised factory
	// tools, with no authority tool among them.
	tools := client.tools(t)
	for _, name := range []string{
		factoryToolProjects, factoryToolList, factoryToolGet, factoryToolCreateDraft,
		factoryToolUpdateDraft, factoryToolApproveAndQueue, factoryToolAnswerQuestion,
	} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("tools/list over the socket is missing %s", name)
		}
	}

	// An authorized read: the project this client's token is granted, with the
	// authority the grant carries, and no host path in the result.
	projects, text := client.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	if len(entries) != 1 {
		t.Fatalf("factory_projects over the socket listed %v, want the one granted project", projects["projects"])
	}
	entry, _ := entries[0].(map[string]any)
	if entry["projectId"] != b.projectID {
		t.Fatalf("factory_projects listed %v, want %s", entry["projectId"], b.projectID)
	}
	capabilities, _ := entry["capabilities"].([]any)
	if len(capabilities) == 0 {
		t.Fatalf("the granted project reported no capability: %v", entry)
	}
	assertNoHostPath(t, text, b.root)

	// Identity is the bearer's, and only an enrolled client token is identity.
	// The shared API token that unlocks /api never reaches /mcp, and neither
	// does an unknown token.
	for name, token := range map[string]string{
		"the bridge's shared API token": handoffAPIToken,
		"an unknown token":              "not-an-enrolled-client",
	} {
		stranger := &handoffClient{url: "http://marshal-daemon/mcp", token: token, http: studioUnixClient(sock)}
		status, _, payload, err := stranger.post(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": mcpProtocolVersion},
		})
		if err != nil {
			t.Fatalf("%s over the socket: %v", name, err)
		}
		if status != http.StatusUnauthorized {
			t.Fatalf("%s over the socket: /mcp answered %d, want 401 (%s)", name, status, payload)
		}
	}

	// A client that is enrolled but holds no grant on this project reads
	// nothing: the same answer an unrelated project gets, naming neither.
	_, otherToken := testAddClient(t, b.srv, MCPClient{ID: "c2", Name: "ungranted"})
	stranger := &handoffClient{url: "http://marshal-daemon/mcp", token: otherToken, http: studioUnixClient(sock)}
	stranger.initialize(t)
	assertNoUnauthorizedAccess(t, stranger, b, "")
}

// TestMCPFactoryEndToEnd drives a complete authorized handoff: initialize,
// tools/list, create, read, edit, approve-and-queue, then the factory
// continuing on its own, a worker's question, and the answer. The client is
// stopped before the factory runs, and stays stopped until the question is
// durable.
func TestMCPFactoryEndToEnd(t *testing.T) {
	b := newHandoffBridge(t)
	client := newHandoffClient(b, b.token)
	client.initialize(t)

	// The seven factory tools, and only the arguments they document: no tool
	// grants authority and none takes a project root.
	tools := client.tools(t)
	for _, name := range []string{
		factoryToolProjects, factoryToolList, factoryToolGet, factoryToolCreateDraft,
		factoryToolUpdateDraft, factoryToolApproveAndQueue, factoryToolAnswerQuestion,
	} {
		schema, ok := tools[name]
		if !ok {
			t.Fatalf("tools/list is missing %s", name)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s inputSchema type = %v, want object", name, schema["type"])
		}
		properties, _ := schema["properties"].(map[string]any)
		if _, ok := properties["projectRoot"]; ok {
			t.Fatalf("%s offers a projectRoot argument, which is resolved server-side", name)
		}
	}
	for name := range tools {
		if name == "grant" || name == "revoke" {
			t.Fatalf("tools/list exposes an authority tool: %q", name)
		}
	}

	// The client learns its project id and the authority it holds, and the
	// approval authority is the configured one, so the assertion can match the
	// grant instead of guessing.
	projects, _ := client.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	if len(entries) != 1 {
		t.Fatalf("factory_projects listed %v, want the one granted project", projects["projects"])
	}
	entry, _ := entries[0].(map[string]any)
	if entry["projectId"] != b.projectID {
		t.Fatalf("factory_projects listed %v, want %s", entry["projectId"], b.projectID)
	}
	if entry["approvalAuthority"] != factoryAuthorityUserDirected {
		t.Fatalf("approvalAuthority = %v, want the granted authority", entry["approvalAuthority"])
	}
	capabilities, _ := entry["capabilities"].([]any)
	held := map[string]bool{}
	for _, capability := range capabilities {
		held[capability.(string)] = true
	}
	for _, capability := range []string{
		factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
	} {
		if !held[capability] {
			t.Fatalf("factory_projects did not report %q: %v", capability, capabilities)
		}
	}

	// Create: one durable operation, attributed to this client and its own
	// request id, with no host path and no server-side root in the result.
	create := b.handoffProposal("handoff-create")
	create["plan"] = "implement the handoff"
	create["spec"] = "the handoff specification"
	created, createdText := client.mustCall(t, factoryToolCreateDraft, create)
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("create returned no item id: %v", created)
	}
	if created["state"] != "draft" || created["version"].(float64) != 1 {
		t.Fatalf("created %v, want a draft at version 1", created)
	}
	if link, _ := created["link"].(string); !containsFactoryLink(link) {
		t.Fatalf("create returned link %q, want a Studio item link", created["link"])
	}
	if lines := b.journalLines(t); lines != 1 {
		t.Fatalf("the create wrote %d journal records, want exactly one", lines)
	}
	if n := len(b.api.List()); n != 1 {
		t.Fatalf("the journal holds %d items, want the one this client created", n)
	}

	// Read: the plan, the specification, the effective configuration and the
	// journal's attribution.
	read, readText := client.mustCall(t, factoryToolGet, map[string]any{"projectId": b.projectID, "itemId": itemID})
	if read["version"].(float64) != 1 || read["state"] != "draft" {
		t.Fatalf("get returned %v", read)
	}
	draft, _ := read["draft"].(map[string]any)
	if draft["plan"] != "implement the handoff" || draft["spec"] != "the handoff specification" {
		t.Fatalf("get returned draft %v", draft)
	}
	history, _ := read["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("get returned %d history entries, want the create's own", len(history))
	}
	provenance, _ := history[0].(map[string]any)["provenance"].(map[string]any)
	if provenance["clientId"] != b.clientID || provenance["projectId"] != b.projectID ||
		provenance["action"] != factoryCapabilityCreateDraft || provenance["requestId"] != "handoff-create" {
		t.Fatalf("create provenance = %v", history[0].(map[string]any)["provenance"])
	}

	// Edit: the draft's content moves and its version with it.
	edit := b.handoffProposal("handoff-edit")
	edit["itemId"] = itemID
	edit["expectedVersion"] = 1
	edit["title"] = "Add the handoff, revised"
	edit["plan"] = "implement the handoff, revised"
	edit["spec"] = "the revised handoff specification"
	edited, editedText := client.mustCall(t, factoryToolUpdateDraft, edit)
	if edited["version"].(float64) != 2 || edited["title"] != "Add the handoff, revised" {
		t.Fatalf("update returned %v", edited)
	}
	if lines := b.journalLines(t); lines != 2 {
		t.Fatalf("the edit wrote %d journal records, want exactly one more", lines)
	}

	// Approve and queue: the exact stored draft, under the configured
	// authority, journalled beside its attribution.
	approved, approvedText := client.mustCall(t, factoryToolApproveAndQueue, b.approve(itemID, "handoff-approve", 2, factoryAuthorityUserDirected))
	if approved["state"] != "queued" || approved["version"].(float64) != 3 {
		t.Fatalf("approve returned %v", approved)
	}
	if lines := b.journalLines(t); lines != 3 {
		t.Fatalf("the approval wrote %d journal records, want exactly one more", lines)
	}
	queued, err := b.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Approvals) != 1 {
		t.Fatalf("the approval recorded %d approvals", len(queued.Approvals))
	}
	approval := queued.Approvals[0]
	// The approval is the exact content the last edit produced — not the
	// draft as it was first created — and its hash is that content's hash.
	if approval.Plan != "implement the handoff, revised" || approval.Spec != "the revised handoff specification" {
		t.Fatalf("the approval queued %q/%q, want the edited draft", approval.Plan, approval.Spec)
	}
	if want := FactoryPlanHash("implement the handoff, revised", "the revised handoff specification"); approval.PlanHash != want {
		t.Fatalf("the approval hash is %q, want %q", approval.PlanHash, want)
	}
	if queued.Draft.RequestID != factoryScopedRequestID(b.clientID, "handoff-create") {
		t.Fatalf("the journal's request identity %q is not namespaced", queued.Draft.RequestID)
	}
	if last := queued.History[len(queued.History)-1]; last.Kind != "approve" || last.Provenance == nil || last.Provenance.Authority != factoryAuthorityUserDirected {
		t.Fatalf("the approval entry %+v", last)
	}

	// Nothing the tools returned may name this host's project root, and no
	// failure may name a project or an item.
	for _, text := range []string{createdText, readText, editedText, approvedText} {
		assertNoHostPath(t, text, b.root)
	}

	// The external client goes away. Everything above is durable, and what
	// follows must not depend on it.
	before := b.requests.Load()
	client.stop()

	// The factory continues from the durable queue on its own: a worker, a
	// seed, a publish, a draft PR, and the implementation that asks a
	// question.
	effects := newHandoffEffects()
	stopFactory := b.runFactory(t, effects)
	defer stopFactory()
	for _, want := range []string{"spawn", "seed", "publish", "create_pr", "implement"} {
		waitEffect(t, effects, want)
	}
	var decisionID string
	select {
	case decisionID = <-effects.question:
	case <-time.After(30 * time.Second):
		t.Fatalf("the factory never opened the worker's question (executed %v)", effects.seen())
	}
	stopFactory()

	if got := b.requests.Load(); got != before {
		t.Fatalf("the bridge answered %d requests while the client was gone, want 0", got-before)
	}

	// The question is durable, and it is the worker's own: the controller
	// recorded it through the same append that settled the effect.
	waiting, err := b.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != "needs_input" || waiting.Decision == nil {
		t.Fatalf("the item is %q with decision %+v, want needs_input with a question", waiting.State, waiting.Decision)
	}
	if waiting.Decision.Kind != "question" || waiting.Decision.ID != decisionID {
		t.Fatalf("the decision is %+v, want the question %q", waiting.Decision, decisionID)
	}
	// A question decision records the worker's text in Reason — that is the
	// field Studio renders and the MCP projection carries — while
	// Decision.Question stays empty for the kinds that do not use it.
	if waiting.Decision.Reason != "which branch should the change land on?" {
		t.Fatalf("the decision recorded %q, want the worker's question", waiting.Decision.Reason)
	}

	// The client comes back — a new connection, the same token — reads the
	// question and answers it.
	back := newHandoffClient(b, b.token)
	back.initialize(t)
	asked, askedText := back.mustCall(t, factoryToolGet, map[string]any{"projectId": b.projectID, "itemId": itemID})
	assertNoHostPath(t, askedText, b.root)
	decision, _ := asked["decision"].(map[string]any)
	if decision["id"] != decisionID || decision["kind"] != "question" {
		t.Fatalf("factory_get returned decision %v, want the open question %q", asked["decision"], decisionID)
	}
	lines := b.journalLines(t)
	answered, answeredText := back.mustCall(t, factoryToolAnswerQuestion, map[string]any{
		"projectId": b.projectID, "itemId": itemID, "requestId": "handoff-answer",
		"expectedVersion": asked["version"], "decisionId": decisionID,
		"answer": "land it on main", "authority": factoryAuthorityUserDirected,
	})
	if answered["state"] != "repairing" {
		t.Fatalf("the answer returned %v, want the item repaired", answered)
	}
	if got := b.journalLines(t); got != lines+1 {
		t.Fatalf("the answer wrote %d journal records, want exactly one", got-lines)
	}
	assertNoHostPath(t, answeredText, b.root)

	final, err := b.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Decision != nil {
		t.Fatalf("the answered decision is still open: %+v", final.Decision)
	}
	answerEvent := final.History[len(final.History)-1]
	if answerEvent.Kind != "answer" || answerEvent.Message != "land it on main" {
		t.Fatalf("the answer entry %+v", answerEvent)
	}
	// The answer is attributed to exactly this client's instruction, naming
	// the decision it resolved and the authority it was admitted under.
	want := FactoryProvenance{
		ClientID: b.clientID, ProjectID: b.projectID,
		Action: factoryCapabilityAnswerQuestion, RequestID: "handoff-answer",
		DecisionID: decisionID, Authority: factoryAuthorityUserDirected,
	}
	if answerEvent.Provenance == nil || *answerEvent.Provenance != want {
		t.Fatalf("the answer provenance = %+v, want %+v", answerEvent.Provenance, want)
	}

	// A client holding no grant on this project reads nothing: the same answer
	// an unknown id gets, naming neither.
	_, otherToken := testAddClient(t, b.srv, MCPClient{ID: "c2", Name: "ungranted"})
	stranger := newHandoffClient(b, otherToken)
	stranger.initialize(t)
	code, text := stranger.mustFail(t, factoryToolGet, map[string]any{"projectId": b.projectID, "itemId": itemID})
	if code != factoryCodeNotFound {
		t.Fatalf("an ungranted client's factory_get = %q, want %q", code, factoryCodeNotFound)
	}
	if text == "" || containsAny(text, itemID, b.projectID, b.root) {
		t.Fatalf("the denial discloses an identity or a path: %q", text)
	}
}

// TestMCPFactoryEndToEndLostResponse proves a lost response costs nothing: the
// bridge committed, the client never saw the answer, and the retry returns the
// operation that is already in the journal instead of writing a second one.
func TestMCPFactoryEndToEndLostResponse(t *testing.T) {
	b := newHandoffBridge(t)
	client := newHandoffClient(b, b.token)
	client.initialize(t)

	args := b.handoffProposal("handoff-lost")
	b.loseResponse.Store(true)
	if _, _, err := client.call(factoryToolCreateDraft, args); err == nil {
		t.Fatal("the client read a response the bridge never delivered")
	}

	// The operation is durable even though the caller never learned its id.
	if n := len(b.api.List()); n != 1 {
		t.Fatalf("the lost response left %d items, want the one committed operation", n)
	}
	if lines := b.journalLines(t); lines != 1 {
		t.Fatalf("the lost response wrote %d journal records, want exactly one", lines)
	}
	var itemID string
	for _, item := range b.api.List() {
		itemID = item.ID
		if item.Draft.RequestID != factoryScopedRequestID(b.clientID, "handoff-lost") {
			t.Fatalf("the committed draft's request identity is %q, not this client's", item.Draft.RequestID)
		}
	}

	// The retry is the same operation: the journal's own answer, with no
	// second append.
	retried, text := client.mustCall(t, factoryToolCreateDraft, args)
	if retried["itemId"] != itemID || retried["version"].(float64) != 1 {
		t.Fatalf("the retry returned %v, want the committed item %s at version 1", retried, itemID)
	}
	if lines := b.journalLines(t); lines != 1 {
		t.Fatalf("the retry wrote a second journal record: %d", lines)
	}
	if n := len(b.api.List()); n != 1 {
		t.Fatalf("the retry created a second item: %d", n)
	}
	assertNoHostPath(t, text, b.root)

	// A retry that is not the same request is a conflict, not a silent
	// second operation under the identity of the first.
	different := b.handoffProposal("handoff-lost")
	different["title"] = "A different draft"
	code, denial := client.mustFail(t, factoryToolCreateDraft, different)
	if code != factoryCodeConflict {
		t.Fatalf("a reused request id with new content = %q, want %q", code, factoryCodeConflict)
	}
	if lines := b.journalLines(t); lines != 1 {
		t.Fatalf("the conflict wrote a journal record: %d", lines)
	}
	if containsAny(denial, itemID, b.root) {
		t.Fatalf("the conflict discloses an identity or a path: %q", denial)
	}

	// The same is true of a command: an update whose response is lost is
	// journalled once, and its retry replays it.
	update := b.handoffProposal("handoff-lost-edit")
	update["itemId"] = itemID
	update["expectedVersion"] = 1
	b.loseResponse.Store(true)
	if _, _, err := client.call(factoryToolUpdateDraft, update); err == nil {
		t.Fatal("the client read an update response the bridge never delivered")
	}
	replayed, _ := client.mustCall(t, factoryToolUpdateDraft, update)
	if replayed["version"].(float64) != 2 {
		t.Fatalf("the replayed update returned %v, want version 2", replayed)
	}
	if lines := b.journalLines(t); lines != 2 {
		t.Fatalf("the lost update and its retry wrote %d records, want one", lines-1)
	}
}

// TestMCPFactoryEndToEndRevokedAndOffline races Studio and MCP writes against
// a revocation, then takes the client away: the ordering the policy lock
// guarantees holds either way, and a revoked client stays revoked with no
// disclosure of what it used to reach.
func TestMCPFactoryEndToEndRevokedAndOffline(t *testing.T) {
	b := newHandoffBridge(t)
	client := newHandoffClient(b, b.token)
	client.initialize(t)

	// Work accepted before the revocation, so the proof has something durable
	// to protect afterwards.
	created, _ := client.mustCall(t, factoryToolCreateDraft, b.handoffProposal("before-revocation"))
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("create returned no item id: %v", created)
	}
	lines := b.journalLines(t)

	// The Studio editor reads the grant it is about to change, then revokes it
	// while the MCP client is writing. Both surfaces take the same policy
	// lock, so exactly one ordering happens and neither write is lost midway.
	version := b.grant(t).Version
	start := make(chan struct{})
	var wg sync.WaitGroup
	var revokeErr error
	var reply factoryToolReply
	var value map[string]any
	var callErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		revokeErr = b.revoke(version)
	}()
	go func() {
		defer wg.Done()
		<-start
		reply, value, callErr = client.call(factoryToolCreateDraft, b.handoffProposal("raced-with-revocation"))
	}()
	close(start)
	wg.Wait()
	if revokeErr != nil {
		t.Fatalf("the Studio revocation failed: %v", revokeErr)
	}
	if callErr != nil {
		t.Fatalf("the racing create: %v", callErr)
	}

	// The only two outcomes are "admitted before the revocation, committed
	// once" and "denied by it, with no write": the journal holds exactly what
	// the outcome says.
	switch {
	case !reply.IsError:
		if n := len(b.api.List()); n != 2 {
			t.Fatalf("the admitted create left %d items, want 2", n)
		}
		if got := b.journalLines(t); got != lines+1 {
			t.Fatalf("the admitted create wrote %d journal records, want one", got-lines)
		}
	case value["code"] == factoryCodeNotFound:
		if n := len(b.api.List()); n != 1 {
			t.Fatalf("the denied create left %d items, want the pre-revocation one", n)
		}
		if got := b.journalLines(t); got != lines {
			t.Fatalf("the denied create wrote %d journal records", got-lines)
		}
	default:
		t.Fatalf("the racing create failed with %v, want an admitted write or a denial", value)
	}

	// The revocation that returned is in force for the next operation.
	if code, _ := client.mustFail(t, factoryToolCreateDraft, b.handoffProposal("after-revocation")); code != factoryCodeNotFound {
		t.Fatalf("a write after the revocation = %q, want %q", code, factoryCodeNotFound)
	}
	assertNoUnauthorizedAccess(t, client, b, itemID)

	// The client goes away. Nothing it was refused becomes reachable, and
	// nothing it created stops existing.
	client.stop()
	state := b.grant(t)
	if state.Grant.Read || state.Grant.CreateDraft || state.Grant.EditDraft ||
		state.Grant.AnswerQuestion || state.Grant.ApproveAndQueue {
		t.Fatalf("the stored grant still carries authority: %+v", state.Grant)
	}
	if state.Grant.ApprovalAuthority != factoryAuthorityNone {
		t.Fatalf("the stored approval authority is %q, want none", state.Grant.ApprovalAuthority)
	}
	kept, err := b.store.Get(itemID)
	if err != nil {
		t.Fatalf("the accepted draft did not survive the revocation: %v", err)
	}
	if kept.State != "draft" || len(kept.Approvals) != 0 {
		t.Fatalf("the accepted draft changed: state %q, %d approvals", kept.State, len(kept.Approvals))
	}

	// Reconnecting with the same token changes nothing: the revocation is
	// durable, not a property of the connection.
	back := newHandoffClient(b, b.token)
	back.initialize(t)
	assertNoUnauthorizedAccess(t, back, b, itemID)

	// A client that never held the grant gets the same answers: no failure
	// distinguishes "revoked" from "was never allowed", and none of them names
	// the item or the project.
	_, otherToken := testAddClient(t, b.srv, MCPClient{ID: "c2", Name: "unrelated"})
	stranger := newHandoffClient(b, otherToken)
	stranger.initialize(t)
	assertNoUnauthorizedAccess(t, stranger, b, itemID)
}

// assertNoUnauthorizedAccess requires a client to hold no capability on this
// fixture's project, and none of its failures to disclose an identity.
func assertNoUnauthorizedAccess(t *testing.T, client *handoffClient, b *handoffBridge, itemID string) {
	t.Helper()

	projects, _ := client.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	for _, entry := range entries {
		listed, _ := entry.(map[string]any)
		if listed["projectId"] != b.projectID {
			t.Fatalf("factory_projects disclosed an unheld project: %v", entry)
		}
		if capabilities, _ := listed["capabilities"].([]any); len(capabilities) != 0 {
			t.Fatalf("factory_projects reports authority after the revocation: %v", capabilities)
		}
		if _, ok := listed["approvalAuthority"]; ok {
			t.Fatalf("factory_projects reports an approval authority after the revocation: %v", listed)
		}
	}

	for _, attempt := range []struct {
		tool string
		args map[string]any
	}{
		{factoryToolList, map[string]any{"projectId": b.projectID}},
		{factoryToolGet, map[string]any{"projectId": b.projectID, "itemId": itemID}},
	} {
		code, text := client.mustFail(t, attempt.tool, attempt.args)
		if code != factoryCodeNotFound {
			t.Fatalf("%s without a grant = %q, want %q", attempt.tool, code, factoryCodeNotFound)
		}
		if containsAny(text, itemID, b.projectID, b.root) {
			t.Fatalf("%s discloses an identity or a path: %q", attempt.tool, text)
		}
	}
}

// containsFactoryLink reports whether a tool result's link is the Studio item
// URL this bridge serves.
func containsFactoryLink(link string) bool {
	return len(link) > len("#factory/") && bytes.Contains([]byte(link), []byte("#factory/"))
}

// containsAny reports whether s contains any of the values.
func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if v != "" && bytes.Contains([]byte(s), []byte(v)) {
			return true
		}
	}
	return false
}

// assertNoHostPath requires a projection to carry no host path: the project
// root is resolved server-side and may never be handed back.
func assertNoHostPath(t *testing.T, text, root string) {
	t.Helper()
	if root != "" && bytes.Contains([]byte(text), []byte(root)) {
		t.Fatalf("a tool result disclosed this host's project root: %s", text)
	}
}
