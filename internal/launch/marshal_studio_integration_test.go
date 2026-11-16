package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/daemon"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/trust"
	"marshal/web/bridge"
)

// These tests are the real-session half of task 7. They run a real Marshal
// runtime through launch.Run — the same composition root `marshal` uses — with
// a scripted deterministic provider, and point the daemon socket the runtime
// enrolls on at a real bridge Server listening on daemon.PathsFor(home).Socket
// over a real factory journal. Nothing here replaces the agent runtime, the MCP
// endpoint or the factory: the session's built-in client speaks the real
// Streamable HTTP transport to the real /mcp route, and the tools it calls are
// the ones the bridge advertises.
//
// Two limits on what a draft can prove at this level are worth stating, because
// they decide which mutation each test drives:
//
//   - A project root is trusted here through the injected resolver, but
//     bridge.NewFleet spawns its control agent from a marshal binary this test
//     has none of, so `config/get` cannot answer. factory_create_draft and
//     factory_update_draft resolve and preflight their routing against exactly
//     that control call before authorizing, so they fail as unavailable rather
//     than reaching the access gate. They are covered at the endpoint instead,
//     by TestMarshalStudioRealSessionWritesAfterAccessGranted and its siblings
//     in web/bridge, where the control agent is the fixture's.
//   - factory_approve_and_queue authorizes first and needs no control agent: it
//     is the write this file drives. It is a durable factory mutation with the
//     same access gate, the same journal and the same provenance, so it proves
//     what the task asks — writes are off by default, enabling them makes the
//     mutation succeed, and the journal records this session as the actor.

// studioBridge is one real bridge Server listening on the local daemon's socket
// over a real factory journal, with one project registered and one session
// association to enroll.
type studioBridge struct {
	srv       *bridge.Server
	store     *bridge.FactoryStore
	root      string
	projectID string
	socket    string
}

const studioSessionID = "sess_studio"

// The tool names the scripted provider calls. They are the aliases the runner
// puts on the wire — sanitizeToolName maps a canonical name's non-word
// characters to underscores — and the runner maps them back before dispatch.
const (
	studioToolProjects = "mcp_marshal-studio_factory_projects"
	studioToolApprove  = "mcp_marshal-studio_factory_approve_and_queue"
)

// newStudioBridge builds the bridge and starts serving its owner-only socket
// handler where a Marshal runtime enrolls. It must be listening before
// launch.Run starts, because the runtime enrolls while it is being built.
func newStudioBridge(t *testing.T, home, root string) *studioBridge {
	t.Helper()

	socket := daemon.PathsFor(home).Socket
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatalf("create the daemon directory: %v", err)
	}

	stateDir := t.TempDir()
	ws := bridge.NewWorkspace(filepath.Join(stateDir, "fleet.json"))
	if err := ws.AddProject(root); err != nil {
		t.Fatalf("register the project: %v", err)
	}
	projectID, ok := ws.ProjectID(root)
	if !ok || projectID == "" {
		t.Fatalf("the registered project has no factory id")
	}
	// The server-owned association enrollment resolves: this session runs on
	// this project. Nothing in an enrollment request can name it.
	if err := ws.PutAgent(bridge.Agent{
		ID: "a1", Project: root, SessionID: studioSessionID,
		OwnerID: bridge.DefaultOwnerID, Origin: "cli",
	}); err != nil {
		t.Fatalf("register the session association: %v", err)
	}
	if err := ws.PutRepo(bridge.Repo{
		ID: "repo-1", URL: "https://example.invalid/repo.git", Branch: "main",
		OwnerID: bridge.DefaultOwnerID, Forge: "github",
	}); err != nil {
		t.Fatalf("register the repository: %v", err)
	}

	store, err := bridge.OpenFactoryStore(filepath.Join(stateDir, "factory"))
	if err != nil {
		t.Fatalf("open the factory journal: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A host-process fleet: this test starts no container and spawns no worker.
	// The controller is built without effects, so an approval is journalled and
	// nothing is dispatched.
	fleet := bridge.NewFleet(ws, "", nil, stateDir, bridge.Limits{}, "test", nil, "")
	t.Cleanup(fleet.Close)

	api := bridge.NewFactoryAPI(store, bridge.NewFactoryController(store, nil, nil), fleet)
	srv := bridge.NewServer(fleet, "", "", api)
	// The daemon identity the enrollment binds to. Its socket is the instance,
	// and its PID and start time are the generation a credential dies with.
	srv.SetDaemonInfo(bridge.DaemonInfo{
		Version: "test", Profile: daemonProfileLocal,
		PID: 4242, StartedAt: 1700000000000, Socket: socket,
	})

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on the daemon socket %s: %v", socket, err)
	}
	httpSrv := &http.Server{Handler: srv.SocketHandler()}
	go func() { _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	return &studioBridge{srv: srv, store: store, root: root, projectID: projectID, socket: socket}
}

// accessHandler is the bridge's owner-only socket handler, which is where the
// whole access surface is served.
func (b *studioBridge) accessHandler() http.Handler { return b.srv.SocketHandler() }

// grant writes one session's access layer through the real access surface,
// exactly as the editor does: read the version the surface reports, then write
// against it.
func (b *studioBridge) grant(t *testing.T, access bridge.MarshalFactoryAccess) {
	t.Helper()
	handler := b.accessHandler()

	read := httptest.NewRequest(http.MethodGet,
		"/api/marshal/access?projectId="+b.projectID+"&sessionId="+studioSessionID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, read)
	if rec.Code != http.StatusOK {
		t.Fatalf("read the access surface: %d %s", rec.Code, rec.Body)
	}
	var view bridge.MarshalAccessView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the access view: %v", err)
	}
	if view.OverrideVersion == "" {
		t.Fatal("the access surface reported no override version to write against")
	}

	body, err := json.Marshal(map[string]any{
		"projectId": b.projectID, "sessionId": studioSessionID,
		"access": access, "version": view.OverrideVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/api/marshal/access/override", bytes.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("write the access surface: %d %s", rec.Code, rec.Body)
	}
}

// seedDraft journals one draft directly, so an approval has something to act
// on. It is the store's own create path — no preflight, because the routing this
// test would have to resolve needs the control agent the fleet cannot start —
// and it carries the acknowledgement an approval requires.
func (b *studioBridge) seedDraft(t *testing.T, requestID string) bridge.FactoryItem {
	t.Helper()
	item, err := b.store.Create(bridge.FactoryCreate{
		RequestID: requestID,
		Title:     "Add the thing",
		Plan:      "implement the thing",
		Spec:      "the specification",
		Config: bridge.FactoryConfig{
			RepoID: "repo-1", ProjectRoot: b.root, TargetBranch: "main",
			Native: true, NoRequiredCIAcknowledged: true,
		},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed a draft: %v", err)
	}
	return item
}

// studioScript is one tool call the scripted provider asks for.
type studioScript struct {
	marker string // a phrase in the user's message that selects this call
	tool   string
	args   string
}

// studioProvider is a deterministic OpenAI-compatible provider. It answers the
// turn scripted for the user's message: a tool call while that turn has no tool
// result yet, and a final message after one. It records every request, so a
// test reads what the model was actually shown — the tool results included.
type studioProvider struct {
	mu       sync.Mutex
	requests []string
	script   []studioScript
}

// handler serves one chat completion.
func (p *studioProvider) handler(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, string(raw))
	p.mu.Unlock()

	script, ok := p.next(string(raw))
	if !ok {
		writeStudioContent(w, "done")
		return
	}
	writeStudioToolCall(w, script.tool, script.args)
}

// next decides what this request gets. Only an agent step — one that carries
// the tool list and a user message this test scripted — is scripted; a title or
// compaction request gets a plain final message. The script is spent once: a
// step that already carries a tool result for that message gets a final
// message.
func (p *studioProvider) next(body string) (studioScript, bool) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return studioScript{}, false
	}
	if len(req.Tools) == 0 || len(req.Messages) == 0 {
		// This is the runtime's title or compaction request, not a step.
		return studioScript{}, false
	}
	// The runner appends its own user-role notes (the context budget and the
	// interaction state) after the text this test sent, so the marker is found
	// on whichever user message carries it, not on the last one.
	at, found := -1, studioScript{}
	for i := len(req.Messages) - 1; i >= 0 && at < 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		text := string(req.Messages[i].Content)
		for _, script := range p.script {
			if strings.Contains(text, script.marker) {
				at, found = i, script
				break
			}
		}
	}
	if at < 0 {
		return studioScript{}, false
	}
	for _, m := range req.Messages[at+1:] {
		if m.Role == "tool" {
			return studioScript{}, false
		}
	}
	return found, true
}

// recorded is every request body the provider answered, in order.
func (p *studioProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// toolResults is every tool result the model was shown, in order. It is how
// "this session read its project" and "the refusal named nothing" are asserted
// without matching the system prompt, which legitimately names the workspace.
func (p *studioProvider) toolResults() []string {
	var out []string
	for _, body := range p.recorded() {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal([]byte(body), &req) != nil {
			continue
		}
		for _, m := range req.Messages {
			if m.Role != "tool" {
				continue
			}
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				out = append(out, text)
				continue
			}
			out = append(out, string(m.Content))
		}
	}
	return out
}

// sawToolResult reports whether a tool result the model was shown contains want.
func (p *studioProvider) sawToolResult(want string) bool {
	for _, result := range p.toolResults() {
		if strings.Contains(result, want) {
			return true
		}
	}
	return false
}

// writeStudioToolCall streams one tool call in the provider's wire format.
func writeStudioToolCall(w http.ResponseWriter, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\","+
		"\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%q}}]}}]}\n\n", name, args)
	_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// writeStudioContent streams one final message.
func writeStudioContent(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
	_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// studioConfig is a provider-flexible config whose only route is the scripted
// provider, so a real turn reaches it and no other endpoint.
func studioConfig(baseURL string) config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"scripted": {Type: "openai_compatible", BaseURL: baseURL, APIKey: "test-key", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		// An explicit window: a local preset otherwise resolves the 8K local
		// default, and this session's prompt alone would put the runner into
		// compaction on its first step instead of calling the tool.
		"scripted-coder": {
			Name: "scripted-coder", Provider: "scripted", Model: "test-model",
			ToolCalling: "native", LocalOnly: true,
			ContextWindow: 200_000, MaxOutputTokens: 8_192,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "scripted-coder"},
				routing.RolePlanner:     {Preset: "scripted-coder"},
				routing.RoleRepoScout:   {Preset: "scripted-coder"},
				routing.RoleTester:      {Preset: "scripted-coder"},
				routing.RoleReviewer:    {Preset: "scripted-coder"},
			},
		},
	}
	return cfg
}

// studioAuth is this test's credential dependency: a real resolver over a
// private in-memory store, so nothing can reach the developer's keychain.
func studioAuth(t *testing.T) provider.AuthResolver {
	t.Helper()
	store := credentials.NewMemStore()
	resolver, err := provider.NewAuthResolver(func(string) (credentials.Store, error) { return store, nil })
	if err != nil {
		t.Fatalf("NewAuthResolver: %v", err)
	}
	return resolver
}

// studioProject is a disposable repository, with the daemon home beside it.
func studioProject(t *testing.T) (home, root string) {
	t.Helper()
	// A short home and root: the daemon's socket lives under the home, and a
	// unix socket path is length-bounded.
	home, err := os.MkdirTemp("", "mhome")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	root, err = os.MkdirTemp("", "mproj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return home, root
}

// TestMarshalStudioRealSessionReadsItsProject drives a real session through
// launch.Run and proves it enrolls and reads its current project through the
// built-in MCP client: the runtime's own client speaks to the real bridge on
// the daemon socket, and the project it reports is the one the server enrolled
// it on.
func TestMarshalStudioRealSessionReadsItsProject(t *testing.T) {
	home, root := studioProject(t)
	b := newStudioBridge(t, home, root)

	scripted := &studioProvider{script: []studioScript{{
		marker: "read my project", tool: studioToolProjects, args: "{}",
	}}}
	modelSrv := httptest.NewServer(http.HandlerFunc(scripted.handler))
	defer modelSrv.Close()

	var toolNames []string
	served := &studioServed{}

	err := Run(t.Context(), io.Discard,
		WithApp(app.WithHomeDir(home)),
		WithApp(app.WithWorkingDir(root)),
		WithApp(app.WithSessionID(studioSessionID)),
		WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })),
		WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return studioConfig(modelSrv.URL), nil
		})),
		WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})),
		WithApp(app.WithAuthResolver(studioAuth(t))),
		served.option(),
		WithProgramRunner(func(ctx context.Context, _ tea.Model, _ io.Writer) ProgramResult {
			sess := served.session(t)
			toolNames, _ = sess.Tools()
			if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "read my project"}); err != nil {
				t.Errorf("the turn: %v", err)
			}
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// The session's built-in client discovered the bridge's tools through the
	// real tools/list, which only happens after a successful enrollment.
	if len(toolNames) == 0 {
		t.Fatal("the session holds no tools: the built-in client never attached")
	}
	attached := false
	for _, name := range toolNames {
		if name == "mcp.marshal-studio.factory_projects" {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("the session holds %v, want the built-in client's tools", toolNames)
	}

	// The model was shown the project this session is bound to, and nothing
	// about where it lives on this host.
	results := scripted.toolResults()
	if len(results) == 0 {
		t.Fatal("the session's tool call produced no result the model was shown")
	}
	if !scripted.sawToolResult(b.projectID) {
		t.Fatalf("no tool result reported project %s; the results were:\n%s",
			b.projectID, strings.Join(results, "\n"))
	}
	for _, result := range results {
		if strings.Contains(result, root) {
			t.Fatalf("a tool result disclosed this host's project root: %s", result)
		}
	}
}

// TestMarshalStudioRealSessionWritesFollowTheAccessSurface proves the write
// half against a real session: the mutation is refused while the session holds
// the read-only default, enabling that session's write access through the
// access surface makes it succeed, and the journal records this session as the
// actor rather than a client or the bridge.
func TestMarshalStudioRealSessionWritesFollowTheAccessSurface(t *testing.T) {
	home, root := studioProject(t)
	b := newStudioBridge(t, home, root)
	draft := b.seedDraft(t, "seeded-draft")

	if got := len(draft.History); got != 0 {
		t.Fatalf("the seeded draft carries %d journal entries", got)
	}

	approve, err := json.Marshal(map[string]any{
		"projectId": b.projectID, "itemId": draft.ID, "requestId": "studio-approve",
		"expectedVersion": 1, "authority": "standing",
	})
	if err != nil {
		t.Fatal(err)
	}
	scripted := &studioProvider{script: []studioScript{
		{marker: "ask to approve", tool: studioToolApprove, args: string(approve)},
	}}
	// The second turn reuses the tool call, after the access surface granted it.
	scripted.script = append(scripted.script, studioScript{
		marker: "ask again", tool: studioToolApprove, args: string(approve),
	})
	modelSrv := httptest.NewServer(http.HandlerFunc(scripted.handler))
	defer modelSrv.Close()

	served := &studioServed{}

	err = Run(t.Context(), io.Discard,
		WithApp(app.WithHomeDir(home)),
		WithApp(app.WithWorkingDir(root)),
		WithApp(app.WithSessionID(studioSessionID)),
		WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })),
		WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return studioConfig(modelSrv.URL), nil
		})),
		WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})),
		WithApp(app.WithAuthResolver(studioAuth(t))),
		served.option(),
		WithProgramRunner(func(ctx context.Context, _ tea.Model, _ io.Writer) ProgramResult {
			sess := served.session(t)

			// The default is read-only, so the approval is refused and nothing
			// is journalled — not even a refusal entry.
			if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "ask to approve"}); err != nil {
				t.Errorf("the refused turn: %v", err)
			}
			refused, err := b.store.Get(draft.ID)
			if err != nil {
				t.Errorf("read the draft after the refusal: %v", err)
				return ProgramResult{}
			}
			if refused.State != "draft" || len(refused.History) != 0 {
				t.Errorf("the refused approval left the draft %q with %d entries, want it untouched",
					refused.State, len(refused.History))
			}
			if !scripted.sawToolResult("not_found") {
				t.Errorf("the model was shown no refusal; the tool results were:\n%s",
					strings.Join(scripted.toolResults(), "\n"))
			}

			// Enabling this session's approval authority through the real
			// access surface is what admits it.
			b.grant(t, bridge.MarshalFactoryAccess{
				Read:              studioBool(true),
				ApproveAndQueue:   studioBool(true),
				ApprovalAuthority: studioString("standing"),
			})

			if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "ask again"}); err != nil {
				t.Errorf("the granted turn: %v", err)
			}
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	approved, err := b.store.Get(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != "queued" {
		t.Fatalf("the draft is %q after the grant, want queued — the real session's approval did not land",
			approved.State)
	}
	if len(approved.History) != 1 {
		t.Fatalf("the approval journalled %d entries, want one", len(approved.History))
	}
	provenance := approved.History[0].Provenance
	if provenance == nil {
		t.Fatalf("the approval carries no provenance: %+v", approved.History[0])
	}
	if provenance.SessionID != studioSessionID {
		t.Fatalf("the journal attributed the approval to session %q, want this session %q",
			provenance.SessionID, studioSessionID)
	}
	if provenance.ClientID != "" {
		t.Fatalf("the approval was attributed to external client %q", provenance.ClientID)
	}
	if provenance.InstanceID == "" || provenance.ProjectID != b.projectID {
		t.Fatalf("the approval's provenance = %+v, want this instance and project", provenance)
	}
	if provenance.AuthorityOrigin != "standing" {
		t.Fatalf("the approval's authority origin = %q, want standing", provenance.AuthorityOrigin)
	}
}

// studioServed captures the service pair each pass serves, so the turn these
// tests drive runs on the session launch built — not on a second one.
type studioServed struct {
	mu   sync.Mutex
	sess *service.Session
}

func (s *studioServed) option() Option {
	return withServed(func(_ *service.Host, sess *service.Session) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sess = sess
	})
}

// session is the pass's session, or a fatal when it served none.
func (s *studioServed) session(t *testing.T) *service.Session {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess == nil {
		t.Fatal("the pass served no session")
	}
	return s.sess
}

func studioBool(b bool) *bool       { return &b }
func studioString(s string) *string { return &s }
