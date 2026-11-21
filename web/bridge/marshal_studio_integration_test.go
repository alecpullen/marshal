package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/web/bridge/contract"
)

// These tests are the bridge half of task 7: a real bridge Server, a real
// factory journal and the real owner-only socket handler, driven by a session
// that enrolled through it exactly as a Marshal session does. Nothing here
// reaches a forge, a git remote, a container runtime or a model provider.
//
// They are written against the workflow task 7 requires — a built-in session
// reads its current project, writes only when the access surface grants it,
// and its mutations are journalled as that session's — so a path the bridge
// does not implement fails here rather than being worked around.

// marshalStudioRealSession is one enrolled session on one real bridge: the
// daemon identity, the server-owned agent association enrollment resolves
// against, a draft for the mutation paths to act on, and the credential the
// enrollment issued.
type marshalStudioRealSession struct {
	*handoffBridge
	sessionID string
	itemID    string
	client    *handoffClient
}

// newMarshalStudioRealSession enrolls one session under the given project
// default. The draft is authored by the fixture's external client, so this
// session's own capabilities are the only thing under test.
func newMarshalStudioRealSession(t *testing.T, access MarshalFactoryAccess) *marshalStudioRealSession {
	t.Helper()
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	author := newHandoffClient(b, b.token)
	author.initialize(t)
	created, _ := author.mustCall(t, factoryToolCreateDraft, b.handoffProposal("session-author"))
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("the fixture authored no draft: %v", created)
	}

	const sessionID = "session-1"
	if err := b.fleet.ws.PutAgent(Agent{
		ID: "a1", Project: b.root, SessionID: sessionID, OwnerID: DefaultOwnerID, Origin: "cli",
	}); err != nil {
		t.Fatal(err)
	}

	// The access layer is written the way the editor writes it: read the
	// version, write against it.
	version := marshalAccessState(b.projectID, "", "", MarshalFactoryAccess{}).Version
	if current, ok := b.fleet.ws.marshalAccessDefault(b.projectID); ok {
		version = current.Version
	}
	if _, _, err := b.srv.factoryAuth.putMarshalAccessDefault(b.projectID, access, version); err != nil {
		t.Fatalf("put project default: %v", err)
	}

	body, err := json.Marshal(MarshalEnrollmentRequest{ProjectRoot: b.root, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, marshalEnrollPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.srv.SocketHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body)
	}
	var enrollment MarshalEnrollmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &enrollment); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	if enrollment.State != marshalStateConnected || enrollment.Token == "" {
		t.Fatalf("enrollment = %+v, want a connected binding with a credential", enrollment)
	}
	if enrollment.SessionID != sessionID || enrollment.ProjectID != b.projectID {
		t.Fatalf("the binding is %+v, not this session on this project", enrollment)
	}

	client := newHandoffClient(b, enrollment.Token)
	client.initialize(t)

	// The credential must resolve to a built-in principal through the real
	// authentication path, so these tests exercise the built-in authorization
	// and not an external client that happens to hold no grants.
	authReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authReq.Header.Set("Authorization", "Bearer "+enrollment.Token)
	principal, ok := b.srv.authenticateMCPPrincipal(authReq)
	if !ok || !principal.Builtin {
		t.Fatalf("the enrollment's credential resolved to %+v (ok=%v), want a verified built-in principal", principal, ok)
	}
	return &marshalStudioRealSession{handoffBridge: b, sessionID: sessionID, itemID: itemID, client: client}
}

// TestMarshalStudioRealSessionReadsItsProject is the read half of the intended
// workflow: a session that enrolled on this bridge, holding read on its own
// project, discovers that project and reads its items through the real MCP
// tools.
func TestMarshalStudioRealSessionReadsItsProject(t *testing.T) {
	s := newMarshalStudioRealSession(t, MarshalFactoryAccess{Read: boolPtr(true)})

	projects, _ := s.client.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	listed := false
	for _, entry := range entries {
		if view, ok := entry.(map[string]any); ok && view["projectId"] == s.projectID {
			listed = true
			if capabilities, _ := view["capabilities"].([]any); len(capabilities) == 0 {
				t.Fatalf("the session's own project reports no capability: %v", view)
			}
		}
	}
	if !listed {
		t.Fatalf("a built-in session holding read on its own project cannot see it: %s returned %v, want project %s listed — the read and draft tools authorize by client grant only, and an enrolled session holds none",
			factoryToolProjects, projects["projects"], s.projectID)
	}

	if _, _, err := s.client.call(factoryToolList, map[string]any{"projectId": s.projectID}); err != nil {
		t.Fatalf("%s: %v — a built-in session holding read cannot list its own project", factoryToolList, err)
	}
	if _, _, err := s.client.call(factoryToolGet, map[string]any{"projectId": s.projectID, "itemId": s.itemID}); err != nil {
		t.Fatalf("%s: %v — a built-in session holding read cannot read its own item", factoryToolGet, err)
	}
}

// TestMarshalStudioRealSessionWritesAfterAccessGranted is the write half: a
// session that was granted create and edit writes a draft, and the journal
// records this session as the actor — not the bridge, not an external client.
func TestMarshalStudioRealSessionWritesAfterAccessGranted(t *testing.T) {
	s := newMarshalStudioRealSession(t, MarshalFactoryAccess{
		Read: boolPtr(true), CreateDraft: boolPtr(true), EditDraft: boolPtr(true),
	})

	reply, created, err := s.client.call(factoryToolCreateDraft, s.handoffProposal("session-create"))
	if err != nil || reply.IsError {
		t.Fatalf("%s: %v %v — a session granted createDraft cannot draft", factoryToolCreateDraft, err, created)
	}
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("create returned no item id: %v", created)
	}

	item, err := s.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	create := item.History[len(item.History)-1]
	if create.Provenance == nil {
		t.Fatalf("the create carries no provenance: %+v", create)
	}
	if create.Provenance.SessionID != s.sessionID || create.Provenance.ClientID != "" {
		t.Fatalf("the journal attributed the create to %+v, want this session (%q) and no external client",
			create.Provenance, s.sessionID)
	}
}

// ── Recovery and isolation, at the endpoint ────────────────────────────
//
// The tests below extend the two above to what task 7's third step names:
// two sessions with different grants, cross-project denial, a revocation
// between calls, a daemon restart, a lost response and its replay, the
// external client's untouched semantics, and no leak of the credential or a
// host path. Each drives the real /mcp endpoint with the credential a real
// enrollment issued; none of them reimplements what the endpoint decides.

// marshalStudioEnroll posts one enrollment on the owner-only socket handler and
// requires a connected binding, returning the credential the endpoint accepts.
func marshalStudioEnroll(t *testing.T, b *handoffBridge, sessionID string) MarshalEnrollmentResponse {
	t.Helper()
	body, err := json.Marshal(MarshalEnrollmentRequest{ProjectRoot: b.root, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, marshalEnrollPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.srv.SocketHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll %s: %d %s", sessionID, rec.Code, rec.Body)
	}
	var enrollment MarshalEnrollmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &enrollment); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	if enrollment.State != marshalStateConnected || enrollment.Token == "" {
		t.Fatalf("enrollment for %s = %+v, want a connected binding with a credential", sessionID, enrollment)
	}
	return enrollment
}

// enrollStudioSession registers one more session on this bridge and enrolls it,
// writing that session's access override the way the editor does — read the
// version, write against it. access is the session layer; nil leaves the
// project default in force, which is the read-only one.
func enrollStudioSession(t *testing.T, b *handoffBridge, agent Agent, access *MarshalFactoryAccess) *handoffClient {
	t.Helper()
	agent.Project = b.root
	agent.OwnerID = DefaultOwnerID
	if agent.Origin == "" {
		agent.Origin = "cli"
	}
	if err := b.fleet.ws.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	if access != nil {
		version := marshalAccessState(b.projectID, agent.SessionID, "", MarshalFactoryAccess{}).Version
		if current, ok := b.fleet.ws.marshalAccessOverride(b.projectID, agent.SessionID, ""); ok {
			version = current.Version
		}
		wrote, _, err := b.srv.factoryAuth.putMarshalAccessOverride(b.projectID, agent.SessionID, "", *access, version)
		if err != nil || !wrote {
			t.Fatalf("put override for %s: wrote %v, err %v", agent.SessionID, wrote, err)
		}
	}
	enrollment := marshalStudioEnroll(t, b, agent.SessionID)
	client := newHandoffClient(b, enrollment.Token)
	client.initialize(t)

	authReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authReq.Header.Set("Authorization", "Bearer "+enrollment.Token)
	principal, ok := b.srv.authenticateMCPPrincipal(authReq)
	if !ok || !principal.Builtin {
		t.Fatalf("%s's credential resolved to %+v (ok=%v), want a verified built-in principal", agent.SessionID, principal, ok)
	}
	if principal.Binding.ProjectID != b.projectID || principal.Binding.SessionID != agent.SessionID {
		t.Fatalf("%s enrolled as %+v", agent.SessionID, principal.Binding)
	}
	return client
}

// proposalFor is a create-draft argument object naming projectID, so a test can
// aim one at a project the session is not bound to.
func (b *handoffBridge) proposalFor(projectID, requestID string) map[string]any {
	args := b.handoffProposal(requestID)
	args["projectId"] = projectID
	return args
}

// journalBytes is the journal as it sits on disk: what "the token never
// reached the journal" is checked against.
func (b *handoffBridge) journalBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.dir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	return raw
}

// TestMarshalStudioRealSessionTwoSessionsDistinctAccess proves the endpoint
// resolves each session's own access: two sessions in one project, one
// read-only and one granted createDraft, get different answers from the same
// tool, and neither can name a project other than the one its binding names.
func TestMarshalStudioRealSessionTwoSessionsDistinctAccess(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	// A second registered project: a session naming it must be refused, and no
	// session's listing may mention it.
	rootB := t.TempDir()
	if err := b.fleet.ws.AddProject(rootB); err != nil {
		t.Fatal(err)
	}
	projectB, ok := b.fleet.ws.ProjectID(rootB)
	if !ok || projectB == b.projectID {
		t.Fatalf("the second project has a distinct factory id: %q vs %q", projectB, b.projectID)
	}

	readOnly := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"}, nil)
	writer := enrollStudioSession(t, b, Agent{ID: "a2", SessionID: "session-2"}, &MarshalFactoryAccess{
		Read: boolPtr(true), CreateDraft: boolPtr(true),
	})

	// The difference, at the endpoint, on the same tool.
	code, denial := readOnly.mustFail(t, factoryToolCreateDraft, b.handoffProposal("read-only-create"))
	if code != factoryCodeNotFound {
		t.Fatalf("a read-only session's %s = %q, want %q", factoryToolCreateDraft, code, factoryCodeNotFound)
	}
	if containsAny(denial, b.root, b.projectID, "session-1") {
		t.Fatalf("the denial names an identity or a path: %q", denial)
	}
	created, _ := writer.mustCall(t, factoryToolCreateDraft, b.handoffProposal("writer-create"))
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("the granted session drafted nothing: %v", created)
	}
	if items := b.store.List(); len(items) != 1 {
		t.Fatalf("the journal holds %d items, want the one the granted session wrote", len(items))
	}

	// Each session is told exactly what the endpoint will enforce, and neither
	// sees the other project.
	for name, client := range map[string]*handoffClient{"read-only": readOnly, "writer": writer} {
		projects, _ := client.mustCall(t, factoryToolProjects, map[string]any{})
		entries, _ := projects["projects"].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s lists %d projects, want its own one: %v", name, len(entries), projects["projects"])
		}
		view, _ := entries[0].(map[string]any)
		if view["projectId"] != b.projectID {
			t.Fatalf("%s is bound to %v, want %s", name, view["projectId"], b.projectID)
		}
		if containsAny(mustJSONString(t, projects), projectB) {
			t.Fatalf("%s's listing mentions a project it is not bound to: %v", name, projects)
		}
	}

	// Naming the other project is refused for both sessions, whatever their
	// access on their own one.
	for name, client := range map[string]*handoffClient{"read-only": readOnly, "writer": writer} {
		code, text := client.mustFail(t, factoryToolList, map[string]any{"projectId": projectB})
		if code != factoryCodeNotFound {
			t.Fatalf("%s naming another project = %q, want %q", name, code, factoryCodeNotFound)
		}
		if containsAny(text, projectB, b.projectID, rootB, b.root) {
			t.Fatalf("%s's cross-project refusal discloses an id or a path: %q", name, text)
		}
	}
}

// TestMarshalStudioRealSessionCrossProjectDenied is the cross-project case on
// every tool that names a project: a session holding access on its own project
// is refused when it names another, and the refusal names nothing.
func TestMarshalStudioRealSessionCrossProjectDenied(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	rootB := t.TempDir()
	if err := b.fleet.ws.AddProject(rootB); err != nil {
		t.Fatal(err)
	}
	projectB, ok := b.fleet.ws.ProjectID(rootB)
	if !ok || projectB == b.projectID {
		t.Fatalf("the second project has a distinct factory id: %q", projectB)
	}

	// A draft in this session's own project, so a read naming the other one has
	// something real to be refused over.
	client := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"},
		&MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)})
	created, _ := client.mustCall(t, factoryToolCreateDraft, b.handoffProposal("own-project"))
	itemID, _ := created["itemId"].(string)
	lines := b.journalLines(t)

	calls := map[string]map[string]any{
		factoryToolList:        {"projectId": projectB},
		factoryToolGet:         {"projectId": projectB, "itemId": itemID},
		factoryToolCreateDraft: b.proposalFor(projectB, "cross-project"),
		factoryToolUpdateDraft: func() map[string]any {
			args := b.proposalFor(projectB, "cross-project-edit")
			args["itemId"], args["expectedVersion"] = itemID, 1
			return args
		}(),
		factoryToolApproveAndQueue: b.approve(itemID, "cross-project-approve", 1, factoryAuthorityStanding),
	}
	for tool, args := range calls {
		code, text := client.mustFail(t, tool, args)
		if code != factoryCodeNotFound {
			t.Fatalf("%s naming another project = %q, want %q", tool, code, factoryCodeNotFound)
		}
		if text == "" || containsAny(text, projectB, b.projectID, rootB, b.root, itemID) {
			t.Fatalf("%s's cross-project refusal discloses an identity or a path: %q", tool, text)
		}
	}
	if after := b.journalLines(t); after != lines {
		t.Fatalf("a cross-project refusal journalled something: %d lines, want %d", after, lines)
	}
}

// TestMarshalStudioRealSessionRevokedBetweenCalls proves a changed access value
// takes effect at the next operation: denying read and drafting between two
// calls refuses both, and the refusal writes nothing.
func TestMarshalStudioRealSessionRevokedBetweenCalls(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	client := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"},
		&MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)})
	if _, _, err := client.call(factoryToolList, map[string]any{"projectId": b.projectID}); err != nil {
		t.Fatalf("%s before the revocation: %v", factoryToolList, err)
	}
	created, _ := client.mustCall(t, factoryToolCreateDraft, b.handoffProposal("before-revocation"))
	itemID, _ := created["itemId"].(string)
	lines := b.journalLines(t)

	// The editor's write: the read the session relied on, and the drafting it
	// used, both denied explicitly. A revocation is a value, not a signal.
	current, ok := b.fleet.ws.marshalAccessOverride(b.projectID, "session-1", "")
	if !ok {
		t.Fatal("the session override is missing")
	}
	wrote, _, err := b.srv.factoryAuth.putMarshalAccessOverride(b.projectID, "session-1", "",
		MarshalFactoryAccess{Read: boolPtr(false), CreateDraft: boolPtr(false)}, current.Version)
	if err != nil || !wrote {
		t.Fatalf("revoke: wrote %v, err %v", wrote, err)
	}

	for tool, args := range map[string]map[string]any{
		factoryToolList:        {"projectId": b.projectID},
		factoryToolGet:         {"projectId": b.projectID, "itemId": itemID},
		factoryToolCreateDraft: b.handoffProposal("after-revocation"),
	} {
		code, text := client.mustFail(t, tool, args)
		if code != factoryCodeNotFound {
			t.Fatalf("%s after the revocation = %q, want %q", tool, code, factoryCodeNotFound)
		}
		if containsAny(text, b.root, b.projectID, itemID) {
			t.Fatalf("%s after the revocation discloses an identity or a path: %q", tool, text)
		}
	}
	if after := b.journalLines(t); after != lines {
		t.Fatalf("a revoked operation journalled something: %d lines, want %d", after, lines)
	}
	// What the session was told it may do is what the endpoint enforces.
	projects, _ := client.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	if len(entries) != 1 {
		t.Fatalf("the revoked session lists %v", projects["projects"])
	}
	if caps, _ := entries[0].(map[string]any)["capabilities"].([]any); len(caps) != 0 {
		t.Fatalf("the revoked session is still told it holds %v", caps)
	}
}

// TestMarshalStudioRealSessionRestartRevokesCredential proves a credential does
// not outlive the generation it was issued on: after the daemon's identity
// moves, the old token is refused, and only a re-enrollment validates again.
func TestMarshalStudioRealSessionRestartRevokesCredential(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	client := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"},
		&MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)})
	oldToken := client.token
	if _, _, err := client.call(factoryToolProjects, map[string]any{}); err != nil {
		t.Fatalf("%s before the restart: %v", factoryToolProjects, err)
	}

	// The restart: a new process, so a new generation. Everything bound to the
	// old one is now authority nobody revalidated.
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID+1, testMarshalStarted+1))
	status, _, payload, err := client.post(map[string]any{
		"jsonrpc": "2.0", "id": 9000, "method": "tools/call",
		"params": handoffToolCall{Name: factoryToolProjects, Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("the post-restart call: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("the post-restart call = %d %s, want %d", status, payload, http.StatusUnauthorized)
	}

	// Re-enrollment is what revalidates, and it mints a credential that is not
	// the old one.
	fresh := marshalStudioEnroll(t, b, "session-1")
	if fresh.Token == oldToken {
		t.Fatal("re-enrollment issued the credential that had just been refused")
	}
	if fresh.Generation != marshalGeneration(testMarshalPID+1, testMarshalStarted+1) {
		t.Fatalf("re-enrollment is bound to generation %q", fresh.Generation)
	}
	freshClient := newHandoffClient(b, fresh.Token)
	freshClient.initialize(t)
	if _, _, err := freshClient.call(factoryToolProjects, map[string]any{}); err != nil {
		t.Fatalf("%s with the revalidated credential: %v", factoryToolProjects, err)
	}

	// The old token never works again, not even after a credential for the same
	// session exists on the current generation.
	status, _, payload, err = client.post(map[string]any{
		"jsonrpc": "2.0", "id": 9001, "method": "tools/call",
		"params": handoffToolCall{Name: factoryToolProjects, Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("the stale token's second call: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("the stale token = %d %s, want %d even after re-enrollment", status, payload, http.StatusUnauthorized)
	}
}

// TestMarshalStudioRealSessionLostResponseReplayed proves a built-in session's
// lost response costs nothing: the operation is durable, and the retry of the
// same request id returns it instead of writing a second one.
func TestMarshalStudioRealSessionLostResponseReplayed(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	client := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"},
		&MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)})
	lines := b.journalLines(t)

	args := b.handoffProposal("lost-response")
	b.loseResponse.Store(true)
	if _, _, err := client.call(factoryToolCreateDraft, args); err == nil {
		t.Fatal("the session read a response the bridge never delivered")
	}
	if after := b.journalLines(t); after != lines+1 {
		t.Fatalf("the lost response wrote %d journal records, want exactly one", after-lines)
	}
	items := b.store.List()
	if len(items) != 1 {
		t.Fatalf("the lost response left %d items, want the one committed operation", len(items))
	}
	itemID := items[0].ID
	if items[0].Draft.RequestID == factoryScopedRequestID(b.clientID, "lost-response") {
		t.Fatal("the session's request was scoped as an external client's")
	}

	// The replay is the journal's own answer, attributed to this session, with
	// no second append.
	replayed, text := client.mustCall(t, factoryToolCreateDraft, args)
	if replayed["itemId"] != itemID || replayed["version"].(float64) != 1 {
		t.Fatalf("the replay returned %v, want the committed item %s at version 1", replayed, itemID)
	}
	if after := b.journalLines(t); after != lines+1 {
		t.Fatalf("the replay wrote a second journal record: %d", after-lines)
	}
	if len(b.store.List()) != 1 {
		t.Fatal("the replay created a second item")
	}
	assertNoHostPath(t, text, b.root)

	// A retry that is not the same request is a conflict, not a silent second
	// operation under the first one's identity.
	different := b.handoffProposal("lost-response")
	different["title"] = "A different draft"
	if code, _ := client.mustFail(t, factoryToolCreateDraft, different); code != factoryCodeConflict {
		t.Fatalf("a reused request id with new content = %q, want %q", code, factoryCodeConflict)
	}
	if after := b.journalLines(t); after != lines+1 {
		t.Fatalf("the conflict wrote a journal record: %d", after-lines)
	}
}

// TestMarshalStudioRealSessionExternalClientAndWorkerUnchanged proves the
// built-in path adds nothing to the two identities that already existed: an
// external MCP client keeps its grant semantics and its provenance bytes, and a
// worker binding's role grants nothing beyond the access stored for it.
func TestMarshalStudioRealSessionExternalClientAndWorkerUnchanged(t *testing.T) {
	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))

	// The fixture's external client holds read, create and edit on this project.
	external := newHandoffClient(b, b.token)
	external.initialize(t)
	created, _ := external.mustCall(t, factoryToolCreateDraft, b.handoffProposal("external-create"))
	externalItem, _ := created["itemId"].(string)
	if externalItem == "" {
		t.Fatalf("the external client drafted nothing: %v", created)
	}
	item, err := b.store.Get(externalItem)
	if err != nil {
		t.Fatal(err)
	}
	entry := item.History[len(item.History)-1]
	if entry.Provenance == nil {
		t.Fatalf("the external create carries no provenance: %+v", item.History)
	}
	// The bytes are the client's own shape: no built-in field appears.
	want := `{"clientId":"` + b.clientID + `","projectId":"` + b.projectID +
		`","action":"createDraft","requestId":"external-create"}`
	if got := string(mustJSON(t, entry.Provenance)); got != want {
		t.Fatalf("the external client's provenance = %s, want %s", got, want)
	}

	// Its grant semantics are untouched by a built-in default that grants a
	// project's sessions more than this client holds on another project.
	version := marshalAccessState(b.projectID, "", "", MarshalFactoryAccess{}).Version
	if current, ok := b.fleet.ws.marshalAccessDefault(b.projectID); ok {
		version = current.Version
	}
	if _, _, err := b.srv.factoryAuth.putMarshalAccessDefault(b.projectID,
		MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)}, version); err != nil {
		t.Fatalf("put project default: %v", err)
	}
	if _, _, err := external.call(factoryToolList, map[string]any{"projectId": b.projectID}); err != nil {
		t.Fatalf("the external client's own read grant: %v", err)
	}

	// Put the project default back to read-only, so the worker below holds
	// exactly the stored default: its role is then the only thing that could
	// widen what it holds.
	defaultState, _ := b.fleet.ws.marshalAccessDefault(b.projectID)
	if wrote, _, err := b.srv.factoryAuth.putMarshalAccessDefault(b.projectID,
		MarshalFactoryAccess{Read: boolPtr(true)}, defaultState.Version); err != nil || !wrote {
		t.Fatalf("reset the project default: wrote %v, err %v", wrote, err)
	}

	// A worker binding: the item and role the server spawned it for are
	// recorded, never consulted. Holding the read-only default, a reviewer-role
	// worker can neither approve nor draft.
	worker := enrollStudioSession(t, b, Agent{
		ID: "w1", SessionID: "session-worker", FactoryItemID: externalItem, FactoryRole: "reviewer",
	}, nil)
	projects, _ := worker.mustCall(t, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	if len(entries) != 1 {
		t.Fatalf("the worker lists %v", projects["projects"])
	}
	view, _ := entries[0].(map[string]any)
	caps, _ := view["capabilities"].([]any)
	for _, capability := range caps {
		if capability != factoryCapabilityRead {
			t.Fatalf("a worker's role granted %v: %v", capability, caps)
		}
	}
	if _, ok := view["approvalAuthority"]; ok {
		t.Fatalf("a worker with no approval switch reports an authority: %v", view)
	}
	if code, _ := worker.mustFail(t, factoryToolApproveAndQueue,
		b.approve(externalItem, "worker-approve", 1, factoryAuthorityStanding)); code != factoryCodeNotFound {
		t.Fatalf("a reviewer-role worker's approval = %q, want %q", code, factoryCodeNotFound)
	}
	if code, _ := worker.mustFail(t, factoryToolCreateDraft, b.handoffProposal("worker-create")); code != factoryCodeNotFound {
		t.Fatalf("a reviewer-role worker's draft = %q, want %q", code, factoryCodeNotFound)
	}

	// Its own override grants drafting and nothing else: the journal records the
	// worker and its role, and the authority is still the store's.
	wrote, _, err := b.srv.factoryAuth.putMarshalAccessOverride(b.projectID, "session-worker", "w1",
		MarshalFactoryAccess{CreateDraft: boolPtr(true)},
		marshalAccessState(b.projectID, "session-worker", "", MarshalFactoryAccess{}).Version)
	if err != nil || !wrote {
		t.Fatalf("grant the worker's drafting: wrote %v, err %v", wrote, err)
	}
	byWorker, _ := worker.mustCall(t, factoryToolCreateDraft, b.handoffProposal("worker-granted"))
	workerItem, _ := byWorker["itemId"].(string)
	stored, err := b.store.Get(workerItem)
	if err != nil {
		t.Fatal(err)
	}
	workerEntry := stored.History[len(stored.History)-1]
	if workerEntry.Provenance == nil {
		t.Fatalf("the worker's create carries no provenance: %+v", stored.History)
	}
	provenance := *workerEntry.Provenance
	if provenance.SessionID != "session-worker" || provenance.WorkerID != "w1" ||
		provenance.Role != "reviewer" || provenance.ClientID != "" ||
		provenance.AuthorityOrigin != factoryAuthorityOriginStanding {
		t.Fatalf("the worker's provenance = %+v", provenance)
	}
}

// TestMarshalStudioRealSessionNoCredentialOrPathLeak proves the enrollment's
// credential and this host's paths stay inside the transport: neither appears
// in a tool result, in the journal, or in a log line, and an enrolled session's
// every answer is path-free.
func TestMarshalStudioRealSessionNoCredentialOrPathLeak(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := newHandoffBridge(t)
	b.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))
	client := enrollStudioSession(t, b, Agent{ID: "a1", SessionID: "session-1"},
		&MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)})
	token := client.token
	if token == "" {
		t.Fatal("the session holds no credential to check for")
	}

	// Every tool result this session reads, including one it is refused.
	results := map[string]string{}
	for tool, args := range map[string]map[string]any{
		factoryToolProjects: {},
		factoryToolList:     {"projectId": b.projectID},
	} {
		_, text := client.mustCall(t, tool, args)
		results[tool] = text
	}
	created, text := client.mustCall(t, factoryToolCreateDraft, b.handoffProposal("leak-check"))
	results[factoryToolCreateDraft] = text
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("the session drafted nothing: %v", created)
	}
	_, text = client.mustCall(t, factoryToolGet, map[string]any{"projectId": b.projectID, "itemId": itemID})
	results[factoryToolGet] = text
	_, text = client.mustFail(t, factoryToolList, map[string]any{"projectId": "00000000000000000000000000000000"})
	results["denied "+factoryToolList] = text

	for tool, result := range results {
		if strings.Contains(result, token) {
			t.Fatalf("%s disclosed the enrollment's credential: %s", tool, result)
		}
		// The path is checked whole. The directory's own name is a short
		// temp-dir segment (`.../TestThing123/001`), so matching it alone
		// fires by coincidence whenever those digits appear inside a hash —
		// the flake that made this test fail on CI while passing locally.
		assertNoHostPath(t, result, b.root)
	}

	// The journal is durable and readable long after the session is gone.
	journal := b.journalBytes(t)
	if bytes.Contains(journal, []byte(token)) {
		t.Fatal("the journal holds the enrollment's credential")
	}

	// So is the log. Nothing on this path logs the credential, the token, or a
	// host path it resolved server-side.
	logged := logs.String()
	if strings.Contains(logged, token) {
		t.Fatalf("a log line holds the enrollment's credential: %s", logged)
	}
	if strings.Contains(logged, b.root) {
		t.Fatalf("a log line holds this host's project root: %s", logged)
	}
}

// mustJSONString renders one decoded tool result for a substring check.
func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	return string(mustJSON(t, v))
}

// ── The handoff walkthrough ─────────────────────────────────────────────
//
// The tests below are the API/CLI walkthrough task 7 names: a built-in project
// session hands its work to Marshal, one worker starts in the project it named,
// a correction the person makes in that worker's own thread reaches the summary
// the source thread is given, and reopening the host still shows one delivered
// result.
//
// The fleet is the assignment fixture's fake one, so the instruction a worker
// receives and the record it writes are the test's to observe; the coordinator
// store, the enrollment and the MCP endpoint are the real ones.

// handoffFlowFixture is one daemon holding an API project (whose thread hands the
// work off) and two CLI projects whose names both answer to "the CLI", which is
// what makes the plan's ambiguous target a real ambiguity rather than a setup.
type handoffFlowFixture struct {
	*assignmentFixture
	parent                               string
	apiRoot, cliRoot, cliBackendRoot     string
	apiProject, cliProject, cliBackendID string
	sourceID, sourceSession, token       string
}

// newHandoffFlowFixture builds that daemon and enrolls the API thread's session,
// exactly as the owner-only socket handler enrolls a Marshal session.
func newHandoffFlowFixture(t *testing.T) *handoffFlowFixture {
	t.Helper()
	fx := &handoffFlowFixture{assignmentFixture: newAssignmentFixture(t)}
	fx.parent = t.TempDir()
	register := func(name string) (string, string) {
		root := filepath.Join(fx.parent, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := fx.ws.AddProject(root); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		id, ok := fx.ws.ProjectID(root)
		if !ok {
			t.Fatalf("%s has no project id", name)
		}
		return root, id
	}
	fx.apiRoot, fx.apiProject = register("api")
	fx.cliRoot, fx.cliProject = register("cli-frontend")
	fx.cliBackendRoot, fx.cliBackendID = register("cli-backend")

	// The source thread: an existing conversation in the API project. It is the
	// session the handoff is invoked from, so the source is never an argument.
	threadID, err := fx.f.Spawn(context.Background(), fx.apiRoot, SpawnOptions{Prompt: "Investigate the client compatibility issue"})
	if err != nil {
		t.Fatalf("spawn the source thread: %v", err)
	}
	agent, ok := fx.ws.Agent(threadID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("the source thread %s holds no session: %+v", threadID, agent)
	}
	fx.sourceID, fx.sourceSession = threadID, agent.SessionID
	fx.token = fx.enroll(t)
	return fx
}

// enroll posts the enrollment the owner-only socket handler serves and requires a
// connected binding, returning the credential the MCP endpoint accepts.
func (fx *handoffFlowFixture) enroll(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(MarshalEnrollmentRequest{ProjectRoot: fx.apiRoot, SessionID: fx.sourceSession})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, marshalEnrollPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.srv.SocketHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll the source thread: %d %s", rec.Code, rec.Body)
	}
	var enrollment MarshalEnrollmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &enrollment); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	if enrollment.State != marshalStateConnected || enrollment.Token == "" {
		t.Fatalf("enrollment = %+v, want a connected binding with a credential", enrollment)
	}
	return enrollment.Token
}

// handoff calls the handoff tool the way the built-in transport does: the
// credential authenticates the session, and the only thing the caller adds is the
// arguments.
func (fx *handoffFlowFixture) handoff(t *testing.T, token string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": coordinatorToolHandoff, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpProtocolVersionHeader, mcpProtocolVersion)
	rec := httptest.NewRecorder()
	fx.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", coordinatorToolHandoff, rec.Code, rec.Body)
	}
	var out rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: decode rpc response: %v (%s)", coordinatorToolHandoff, err, rec.Body)
	}
	if out.Error != nil {
		t.Fatalf("%s: rpc error %d: %s", coordinatorToolHandoff, out.Error.Code, out.Error.Message)
	}
	result, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	var reply factoryToolReply
	if err := json.Unmarshal(result, &reply); err != nil {
		t.Fatalf("%s: decode tool result: %v (%s)", coordinatorToolHandoff, err, result)
	}
	if len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		t.Fatalf("%s: tool result has %d content blocks: %s", coordinatorToolHandoff, len(reply.Content), result)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(reply.Content[0].Text), &value); err != nil {
		t.Fatalf("%s: tool result text is not a JSON object: %v (%s)", coordinatorToolHandoff, err, reply.Content[0].Text)
	}
	return value, reply.IsError
}

// sourceView reads one source thread's handoff the way its thread screen does.
func (fx *handoffFlowFixture) sourceView(t *testing.T, s *Server) contract.CoordinatorSourceView {
	t.Helper()
	rec := doReq(t, s, http.MethodGet,
		"/api/coordinator/sources/"+fx.apiProject+"/"+fx.sourceID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the source view: %d %s", rec.Code, rec.Body)
	}
	var out contract.CoordinatorSourceView
	decodeBody(t, rec, &out)
	return out
}

// prompts is how many turns one agent's session was given, which is what "no
// agent turn was started" is asserted against.
func prompts(fake *fakeAgent) int {
	if fake == nil {
		return 0
	}
	return len(fake.calls("session/prompt"))
}

// TestMarshalStudioHandoffAsksBeforeAnAmbiguousTarget is the plan's ambiguous
// case: "the CLI" names two projects, so the handoff asks one question, links
// nothing and starts nothing — and the answer that names one project by id
// dispatches exactly there.
func TestMarshalStudioHandoffAsksBeforeAnAmbiguousTarget(t *testing.T) {
	fx := newHandoffFlowFixture(t)
	fx.grant(fx.cliProject, standing())
	fx.grant(fx.cliBackendID, standing())

	value, isError := fx.handoff(t, fx.token, map[string]any{
		"requestId": "handoff-ambiguous",
		"outcome":   "Make the CLI accept the new parser flags",
		"findings":  "The API now emits --compat=2; the CLI rejects it.",
		"targets":   []map[string]any{{"project": "the CLI", "instruction": "Accept the new parser flags"}},
	})
	if isError {
		t.Fatalf("an ambiguous target was answered with a failure, want a question: %v", value)
	}
	clarification, _ := value["clarification"].(map[string]any)
	if clarification == nil {
		t.Fatalf("an ambiguous target did not ask a question: %v", value)
	}
	if question, _ := clarification["question"].(string); !strings.Contains(question, "CLI") {
		t.Fatalf("the question does not quote the ambiguous name: %q", question)
	}
	if candidates, _ := clarification["candidates"].([]any); len(candidates) < 2 {
		t.Fatalf("the question named %d candidates, want both CLI projects: %v", len(candidates), clarification)
	}
	if assignments, _ := value["assignments"].([]any); len(assignments) != 0 {
		t.Fatalf("an ambiguous handoff reported %d assignments", len(assignments))
	}
	for _, root := range []string{fx.cliRoot, fx.cliBackendRoot} {
		if agents := fx.agentsIn(root); len(agents) != 0 {
			t.Fatalf("an ambiguous handoff started %d workers in %s", len(agents), root)
		}
	}
	if view := fx.sourceView(t, fx.srv); view.Task != nil {
		t.Fatalf("an ambiguous handoff linked a task: %+v", view.Task)
	}

	// The answer names one project by its canonical id, and exactly one worker
	// starts there.
	value, isError = fx.handoff(t, fx.token, map[string]any{
		"requestId": "handoff-answered",
		"outcome":   "Make the CLI accept the new parser flags",
		"targets":   []map[string]any{{"projectId": fx.cliProject, "instruction": "Accept the new parser flags"}},
	})
	if isError {
		t.Fatalf("the answered handoff failed: %v", value)
	}
	assignments, _ := value["assignments"].([]any)
	if len(assignments) != 1 {
		t.Fatalf("the answered handoff reported %d assignments, want 1: %v", len(assignments), value)
	}
	if agents := fx.agentsIn(fx.cliRoot); len(agents) != 1 {
		t.Fatalf("the answered handoff started %d workers in the CLI project, want 1", len(agents))
	}
	if agents := fx.agentsIn(fx.cliBackendRoot); len(agents) != 0 {
		t.Fatalf("the answered handoff started %d workers in the project it was not asked for", len(agents))
	}
}

// TestMarshalStudioHandoffStartsOneWorkerAndReportsBack is the whole journey the
// task names: the source is linked once, one CLI worker starts, a correction the
// person makes in that worker's own thread is what the eventual summary says, and
// reopening the host still shows one delivered result.
func TestMarshalStudioHandoffStartsOneWorkerAndReportsBack(t *testing.T) {
	fx := newHandoffFlowFixture(t)
	fx.grant(fx.cliProject, standing())
	sourceFake := fx.agentFor(fx.sourceID)
	sourcePrompts := prompts(sourceFake)

	value, isError := fx.handoff(t, fx.token, map[string]any{
		"requestId": "handoff-cli",
		"outcome":   "Make the CLI accept the new parser flags",
		"findings":  "The API now emits --compat=2; the CLI rejects it.",
		"targets":   []map[string]any{{"projectId": fx.cliProject, "instruction": "Accept the new parser flags"}},
	})
	if isError {
		t.Fatalf("the handoff failed: %v", value)
	}

	// The source is the invoking session's own thread, never an argument.
	source, _ := value["source"].(map[string]any)
	if source == nil || source["projectId"] != fx.apiProject || source["id"] != fx.sourceID {
		t.Fatalf("the handoff linked %v, want this thread in the API project", value["source"])
	}
	// One worker, carrying the assignment's identity, with the instruction that
	// the findings travelled with.
	workers := fx.agentsIn(fx.cliRoot)
	if len(workers) != 1 {
		t.Fatalf("the handoff started %d workers in the CLI project, want 1", len(workers))
	}
	workerID := workers[0].ID
	assignmentID, _ := workers[0].CoordinatorAssignmentID, workers[0].CoordinatorTaskID
	if assignmentID == "" || workers[0].CoordinatorTaskID == "" {
		t.Fatalf("the worker does not carry the assignment identity: %+v", workers[0])
	}
	instructions := waitForPrompt(t, fx.agentFor(workerID), 1)
	if len(instructions) != 1 {
		t.Fatalf("the worker received %d instructions, want 1", len(instructions))
	}
	if !strings.Contains(instructions[0], "compat") {
		t.Fatalf("the instruction that reached the worker was %q", instructions[0])
	}
	// The source keeps its own conversation: the handoff and the delivery are
	// updates, not another agent turn.
	if got := prompts(sourceFake); got != sourcePrompts {
		t.Fatalf("the handoff started %d turns in the source thread, want none", got-sourcePrompts)
	}

	// The source is linked once: one task names this thread, and it is the one
	// the assignments belong to.
	view := fx.sourceView(t, fx.srv)
	if view.Task == nil || view.Task.Source == nil || view.Task.Source.ID != fx.sourceID {
		t.Fatalf("the source view names %+v, want the task linked to this thread", view.Task)
	}
	tasks := fx.srv.coordinator.store
	all, err := tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, task := range all {
		if task.Source != nil && *task.Source == *view.Task.Source {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("the source thread is linked by %d tasks, want 1", linked)
	}
	if len(view.Assignments) != 1 || view.Assignments[0].State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("the source view reads %+v, want one running assignment", view.Assignments)
	}

	// The person corrects the CLI worker in its own thread, and the worker's own
	// record says what it now does. That correction is what the eventual summary
	// reports, not the instruction it was handed at dispatch.
	fx.f.deliverInstruction(workerID, "Keep accepting --compat=1 as well")
	corrected, ok := fx.ws.Agent(workerID)
	if !ok {
		t.Fatal("the worker's record is gone")
	}
	corrected.Branch = "fix/compat-negotiation"
	corrected.PRUrl = "https://forge.invalid/cli-frontend/pulls/7"
	corrected.PushedAt = fx.now
	if err := fx.ws.PutAgent(corrected); err != nil {
		t.Fatal(err)
	}

	// Reading the source again observes the correction and refreshes the one
	// delivered result to it.
	view = fx.sourceView(t, fx.srv)
	if view.Delivery == nil {
		t.Fatal("the source view carries no delivered result")
	}
	if !strings.Contains(view.Delivery.Text, corrected.Branch) {
		t.Fatalf("the delivered result does not report the worker's correction: %q", view.Delivery.Text)
	}
	if !strings.Contains(view.Delivery.Text, "cli-frontend") {
		t.Fatalf("the delivered result does not name the project it was handed to: %q", view.Delivery.Text)
	}
	if view.Delivery.EventID == "" {
		t.Fatal("the delivered result carries no stable event id")
	}
	if view.Delivery.Source == nil || view.Delivery.Source.ID != fx.sourceID {
		t.Fatalf("the delivered result names source %+v, want this thread", view.Delivery.Source)
	}
	// The summary was posted, not the agent turned.
	if got := prompts(sourceFake); got != sourcePrompts {
		t.Fatalf("reporting back started %d turns in the source thread, want none", got-sourcePrompts)
	}

	// The update reached the fleet stream as one delta carrying whole rows, which
	// is what a live Studio follows.
	var deltas []contract.CoordinatorDelta
	for _, event := range fx.f.fleetLog.Tail(fleetStreamKey) {
		var delta contract.CoordinatorDelta
		if json.Unmarshal(event.Data, &delta) == nil && delta.Kind == contract.DeltaCoordinator && delta.TaskID == view.Task.ID {
			deltas = append(deltas, delta)
		}
	}
	if len(deltas) == 0 {
		t.Fatal("no coordinator delta reached the fleet stream")
	}
	last := deltas[len(deltas)-1]
	if last.SessionID != fx.sourceID {
		t.Fatalf("the last coordinator delta names session %q, want the source thread", last.SessionID)
	}
	if len(last.Assignments) != 1 || last.Delivery == nil || !strings.Contains(last.Delivery.Text, corrected.Branch) {
		t.Fatalf("the last coordinator delta = %+v, want the corrected assignment and its delivery", last)
	}

	// Reopening the host — a restarted bridge over the same durable state — reads
	// one delivered result, with the stable event id it was written under.
	restarted := fx.restart()
	again := fx.sourceView(t, restarted)
	if again.Delivery == nil {
		t.Fatal("a reopened host lost the delivered result")
	}
	if again.Delivery.EventID != view.Delivery.EventID {
		t.Fatalf("a reopened host reports delivery %q, want the stable %q", again.Delivery.EventID, view.Delivery.EventID)
	}
	reopenedStore := restarted.coordinator.store
	delivered, held, err := reopenedStore.Delivery(view.Task.ID)
	if err != nil || !held {
		t.Fatalf("the reopened store holds no delivery for the task: held=%v err=%v", held, err)
	}
	if delivered.EventID != view.Delivery.EventID {
		t.Fatalf("the reopened store's delivery is %q, want %q", delivered.EventID, view.Delivery.EventID)
	}
	// One task, one delivery: reloading does not post the summary a second time.
	all, err = reopenedStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("the reopened store holds %d tasks for one handoff, want 1", len(all))
	}
}
