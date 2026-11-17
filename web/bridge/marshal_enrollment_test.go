package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// marshalEnrollFixture is one daemon-shaped bridge: a daemon identity, two
// registered projects, and the server-owned session associations enrollment
// resolves against. It reuses the grant tests' harness, so the access a binding
// carries is the same store those tests drive.
type marshalEnrollFixture struct {
	*marshalAccessFixture
	rootB    string
	projectB string
}

const (
	testMarshalSocket  = "/state/daemon/marshal.sock"
	testMarshalPID     = 4242
	testMarshalStarted = int64(1700000000000)
)

// instance is what GET /api/daemon reports: the identity every binding is
// scoped to.
func testMarshalInstance(pid int, startedAt int64) DaemonInfo {
	return DaemonInfo{
		Version:   "test",
		Profile:   "local",
		PID:       pid,
		StartedAt: startedAt,
		Socket:    testMarshalSocket,
	}
}

func newMarshalEnrollFixture(t *testing.T) *marshalEnrollFixture {
	t.Helper()
	fx := &marshalEnrollFixture{marshalAccessFixture: newMarshalAccessFixture(t)}
	// The second project, so a test can prove two projects bind two IDs and
	// that naming the wrong one is refused.
	fx.rootB = t.TempDir()
	if err := fx.s.fleet.ws.AddProject(fx.rootB); err != nil {
		t.Fatal(err)
	}
	projectB, ok := fx.s.fleet.ws.ProjectID(fx.rootB)
	if !ok || projectB == fx.projectID {
		t.Fatalf("the second registered project has a distinct factory id: %q", projectB)
	}
	fx.projectB = projectB
	fx.s.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))
	return fx
}

// agent registers the server-owned association one agent's session has: the
// project it belongs to and, for a factory worker, the item and role the server
// spawned it for.
func (fx *marshalEnrollFixture) agent(id, root, sessionID, itemID, role string) Agent {
	fx.t.Helper()
	a := Agent{
		ID:            id,
		Project:       root,
		SessionID:     sessionID,
		OwnerID:       DefaultOwnerID,
		Origin:        "cli",
		FactoryItemID: itemID,
		FactoryRole:   role,
	}
	if err := fx.s.fleet.ws.PutAgent(a); err != nil {
		fx.t.Fatal(err)
	}
	return a
}

// enroll posts one enrollment request the way the trusted transport does: on
// the owner-only socket handler, never on the network listener.
func (fx *marshalEnrollFixture) enroll(root, sessionID string) (int, MarshalEnrollmentResponse, string) {
	fx.t.Helper()
	body, err := json.Marshal(MarshalEnrollmentRequest{ProjectRoot: root, SessionID: sessionID})
	if err != nil {
		fx.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, marshalEnrollPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.s.SocketHandler().ServeHTTP(rec, req)
	var out MarshalEnrollmentResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			fx.t.Fatalf("decode enrollment answer: %v (%s)", err, rec.Body)
		}
	}
	return rec.Code, out, strings.TrimSpace(rec.Body.String())
}

// principal resolves one credential the way mcpHandler resolves a /mcp request.
func (fx *marshalEnrollFixture) principal(token string) (mcpPrincipal, bool) {
	fx.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return fx.s.authenticateMCPPrincipal(req)
}

// enrolledClients is every client record enrollment minted.
func (fx *marshalEnrollFixture) enrolledClients() []MCPClient {
	var out []MCPClient
	for _, c := range fx.s.fleet.ws.Clients() {
		if strings.HasPrefix(c.ID, marshalClientPrefix) {
			out = append(out, c)
		}
	}
	return out
}

// TestMarshalEnrollmentCanonicalProject proves an actual session binds to the
// project the daemon tracks for it: two projects, two sessions in one project,
// and a launch from a subdirectory all resolve to the right opaque project ID,
// and the credential that comes back is a built-in identity and nothing more.
func TestMarshalEnrollmentCanonicalProject(t *testing.T) {
	fx := newMarshalEnrollFixture(t)
	fx.agent("a1", fx.root, "s1", "", "")
	fx.agent("a2", fx.root, "s2", "", "")

	code, first, body := fx.enroll(fx.root, "s1")
	if code != http.StatusOK || first.State != marshalStateConnected {
		t.Fatalf("enrolling s1: %d %+v (%s)", code, first, body)
	}
	if first.InstanceID != testMarshalSocket || first.ProjectID != fx.projectID || first.SessionID != "s1" {
		t.Fatalf("binding = %+v", first)
	}
	if want := "4242:1700000000000"; first.Generation != want {
		t.Fatalf("generation = %q, want %q", first.Generation, want)
	}
	if first.Token == "" {
		t.Fatal("a connected enrollment carried no credential")
	}
	if first.WorkerID != "" || first.ItemID != "" || first.Role != "" {
		t.Fatalf("a plain session carried worker identity: %+v", first)
	}

	// The credential is the one thing not persisted: the workspace keeps its
	// hash, under a record a surface can tell apart from an external client's.
	clients := fx.enrolledClients()
	if len(clients) != 1 {
		t.Fatalf("enrollment minted %d client records, want 1", len(clients))
	}
	if clients[0].TokenHash == first.Token || clients[0].TokenHash == "" {
		t.Fatal("the credential was stored in the clear")
	}
	if clients[0].OwnerID != DefaultOwnerID {
		t.Fatalf("client owner = %q", clients[0].OwnerID)
	}

	// It authenticates as a built-in principal, and the authorizer accepts the
	// binding it carries: read-only, which is the default with nothing stored.
	principal, ok := fx.principal(first.Token)
	if !ok || !principal.Builtin {
		t.Fatalf("the credential did not authenticate as built-in: %+v", principal)
	}
	if principal.Binding.ProjectID != fx.projectID || principal.Binding.SessionID != "s1" ||
		principal.Binding.Generation != first.Generation {
		t.Fatalf("principal binding = %+v", principal.Binding)
	}
	if err := fx.s.factoryAuth.AuthorizeMarshalRead(principal.Binding, "app"); err != nil {
		t.Fatalf("a read of the bound project was denied: %v", err)
	}
	if err := fx.s.factoryAuth.AuthorizeMarshalCapability(principal.Binding, "app", factoryCapabilityCreateDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a fresh enrollment proposed work: %v", err)
	}

	// Two sessions in one project are two enrollments with two credentials.
	_, second, _ := fx.enroll(fx.root, "s2")
	if second.State != marshalStateConnected || second.ProjectID != fx.projectID {
		t.Fatalf("second session = %+v", second)
	}
	if second.SessionID != "s2" || second.Token == first.Token {
		t.Fatalf("two sessions shared one credential: %+v", second)
	}

	// The second project binds its own opaque ID, never the first one's.
	fx.agent("a3", fx.rootB, "s3", "", "")
	_, third, _ := fx.enroll(fx.rootB, "s3")
	if third.State != marshalStateConnected || third.ProjectID != fx.projectB {
		t.Fatalf("the second project's session = %+v", third)
	}
	if third.ProjectID == first.ProjectID {
		t.Fatal("two projects shared one project ID")
	}

	// A launch from a subdirectory resolves to the same project. The internal
	// side resolves the git root before asking, and an unresolved subdirectory
	// is not a registered root either, so the tracked association still decides
	// — a path can never introduce a project.
	sub := filepath.Join(fx.root, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	fromSub, subOut, subBody := fx.enroll(sub, "s1")
	if fromSub != http.StatusOK || subOut.State != marshalStateConnected || subOut.ProjectID != fx.projectID {
		t.Fatalf("a subdirectory launch bound %+v (%d %s)", subOut, fromSub, subBody)
	}
	if len(fx.enrolledClients()) != 3 {
		t.Fatalf("re-enrolling a session accumulated records: %d", len(fx.enrolledClients()))
	}

	// One session that two projects both claim is ambiguous: neither is
	// guessed. Naming the launching root selects one, which is the whole of the
	// selection enrollment supports here.
	fx.agent("a4", fx.root, "s4", "", "")
	fx.agent("a5", fx.rootB, "s4", "", "")
	code, ambiguous, _ := fx.enroll("", "s4")
	if code != http.StatusOK || ambiguous.State != marshalStateAmbiguous {
		t.Fatalf("an ambiguous mapping answered %d %+v", code, ambiguous)
	}
	if ambiguous.Token != "" {
		t.Fatal("an ambiguous mapping issued a credential")
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("candidates = %v", ambiguous.Candidates)
	}
	code, chosen, _ := fx.enroll(fx.root, "s4")
	if code != http.StatusOK || chosen.State != marshalStateConnected || chosen.ProjectID != fx.projectID {
		t.Fatalf("the selected project did not bind: %d %+v", code, chosen)
	}

	// Enrollment lives on the owner-only socket. The network listener's routing
	// has no such route, so a remote that is not served it learns nothing.
	rec := doReq(t, fx.s, http.MethodPost, marshalEnrollPath,
		MarshalEnrollmentRequest{ProjectRoot: fx.root, SessionID: "s1"}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the network listener answered enrollment %d, want 404", rec.Code)
	}
}

// TestMarshalEnrollmentWorkerOwnedMapping proves a worker binds through the
// item, role and session the server spawned it for, even when its checkout path
// is somewhere the workspace never registered as a project.
func TestMarshalEnrollmentWorkerOwnedMapping(t *testing.T) {
	fx := newMarshalEnrollFixture(t)

	// The worker's checkout is its own directory — a worktree, not the host
	// project root — and the workspace registers it as nothing.
	checkout := filepath.Join(t.TempDir(), "worktrees", "w1")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := fx.s.fleet.ws.ProjectID(checkout); ok {
		t.Fatal("the worker's checkout is a registered project, which the case needs it not to be")
	}
	worker := fx.agent("w1", fx.root, "ws1", "item-7", "implementer")

	code, out, body := fx.enroll(checkout, "ws1")
	if code != http.StatusOK || out.State != marshalStateConnected {
		t.Fatalf("enrolling the worker: %d %+v (%s)", code, out, body)
	}
	// The opaque project ID is the host project's, taken from the server's own
	// association rather than from the path the worker runs in.
	if out.ProjectID != fx.projectID {
		t.Fatalf("the worker bound project %q, want the host project %q", out.ProjectID, fx.projectID)
	}
	if out.WorkerID != worker.ID || out.ItemID != "item-7" || out.Role != "implementer" {
		t.Fatalf("worker identity = %+v", out)
	}

	// A sibling worker of the same project is not this worker: its override
	// does not apply, and the default still grants nothing but read.
	principal, ok := fx.principal(out.Token)
	if !ok || !principal.Builtin || principal.Binding.WorkerID != worker.ID {
		t.Fatalf("worker principal = %+v", principal)
	}
	if err := fx.s.factoryAuth.AuthorizeMarshalRead(principal.Binding, "app"); err != nil {
		t.Fatalf("the worker could not read its project: %v", err)
	}
	sibling := principal.Binding
	sibling.WorkerID = "w2"
	if err := fx.s.factoryAuth.AuthorizeMarshalCapability(sibling, "app", factoryCapabilityRead); err != nil {
		// A sibling inherits the project default, which is read; the point is
		// that nothing about the role grants more.
		t.Fatalf("a sibling worker could not read: %v", err)
	}
	if err := fx.s.factoryAuth.AuthorizeMarshalCapability(principal.Binding, "app", factoryCapabilityApproveAndQueue); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a role granted approval: %v", err)
	}

	// The worker's session named against a different registered project is
	// refused: the request cannot move an association to another project.
	refused, workerOut, refusal := fx.enroll(fx.rootB, "ws1")
	if refused != http.StatusForbidden {
		t.Fatalf("the worker's session named another project and answered %d %+v (%s)", refused, workerOut, refusal)
	}
	if workerOut.Token != "" {
		t.Fatal("a refused request minted a credential")
	}
}

// TestMarshalEnrollmentRejectsSpoofedSession proves a request cannot introduce a
// session, a project or a worker: only the daemon's own association can, and a
// disagreement is one opaque refusal that mints nothing.
func TestMarshalEnrollmentRejectsSpoofedSession(t *testing.T) {
	fx := newMarshalEnrollFixture(t)
	fx.agent("a1", fx.root, "s1", "", "")

	// A session the daemon does not run cannot be enrolled by asking for it.
	code, out, body := fx.enroll(fx.root, "ghost")
	if code != http.StatusForbidden {
		t.Fatalf("a session the daemon does not run answered %d (%s)", code, body)
	}
	if out.Token != "" || out.State == marshalStateConnected {
		t.Fatalf("a spoofed session was granted: %+v", out)
	}
	if body != `{"error":"`+errMarshalEnrollmentRefused.Error()+`"}` {
		t.Fatalf("refusal body = %q", body)
	}
	if strings.Contains(body, "ghost") || strings.Contains(body, fx.projectID) {
		t.Fatalf("the refusal named an identity: %q", body)
	}
	if len(fx.enrolledClients()) != 0 || fx.s.marshalEnroll.len() != 0 {
		t.Fatal("a refusal minted a credential or a record")
	}

	// A request that names a project the tracked association does not agree
	// with is refused too, and mints nothing either.
	code, _, body = fx.enroll(fx.rootB, "s1")
	if code != http.StatusForbidden {
		t.Fatalf("a mismatched project answered %d (%s)", code, body)
	}
	if len(fx.enrolledClients()) != 0 {
		t.Fatal("a refusal minted a client record")
	}

	// An empty session is not an enrollment request: it grants nothing, and it
	// is not a refusal the caller can probe with.
	code, empty, _ := fx.enroll(fx.root, "")
	if code != http.StatusOK || empty.State != marshalStateUnsupported || empty.Token != "" {
		t.Fatalf("an empty session answered %d %+v", code, empty)
	}

	// A bridge that is not a daemon names no instance, so there is nothing to
	// bind: unsupported, and no credential.
	fx.s.SetDaemonInfo(DaemonInfo{})
	code, notDaemon, _ := fx.enroll(fx.root, "s1")
	if code != http.StatusOK || notDaemon.State != marshalStateUnsupported || notDaemon.Token != "" {
		t.Fatalf("a bridge without an instance identity answered %d %+v", code, notDaemon)
	}
}

// TestMarshalEnrollmentRestartInvalidates proves a credential does not outlive
// the generation it was issued under: a restart revokes it, and the session
// re-enrolls rather than extending it.
func TestMarshalEnrollmentRestartInvalidates(t *testing.T) {
	fx := newMarshalEnrollFixture(t)
	fx.agent("a1", fx.root, "s1", "", "")

	code, first, body := fx.enroll(fx.root, "s1")
	if code != http.StatusOK || first.State != marshalStateConnected {
		t.Fatalf("enrolling: %d %+v (%s)", code, first, body)
	}
	if _, ok := fx.principal(first.Token); !ok {
		t.Fatal("the fresh credential did not authenticate")
	}

	// The daemon restarts: same socket, new PID and start time, so a new
	// generation.
	fx.s.SetDaemonInfo(testMarshalInstance(5151, 1700000099999))
	if _, ok := fx.principal(first.Token); ok {
		t.Fatal("a credential enrolled before the restart still authenticated")
	}
	if fx.s.marshalEnroll.len() != 0 {
		t.Fatalf("the stale binding survived the restart: %d", fx.s.marshalEnroll.len())
	}
	if len(fx.enrolledClients()) != 0 {
		t.Fatalf("the revoked credential's record survived: %v", fx.enrolledClients())
	}

	// The same session enrolls again under the new generation and works.
	code, second, _ := fx.enroll(fx.root, "s1")
	if code != http.StatusOK || second.State != marshalStateConnected {
		t.Fatalf("re-enrolling after the restart: %d %+v", code, second)
	}
	if second.Generation != "5151:1700000099999" {
		t.Fatalf("generation = %q", second.Generation)
	}
	if second.Token == first.Token {
		t.Fatal("the restarted daemon reissued the old credential")
	}
	principal, ok := fx.principal(second.Token)
	if !ok || !principal.Builtin {
		t.Fatalf("the new credential did not authenticate: %+v", principal)
	}
	if principal.Binding.Generation != second.Generation {
		t.Fatalf("principal generation = %q", principal.Binding.Generation)
	}
}
