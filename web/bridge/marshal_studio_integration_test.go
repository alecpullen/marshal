package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
