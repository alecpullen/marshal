package bridge

import (
	"net/http"
	"strings"
	"testing"
)

// boolValue resolves one access field for a test assertion. An unset field is
// not true: reading it through the pointer keeps "unset" visible.
func boolValue(b *bool) bool { return b != nil && *b }

// studioAccessFixture is one bridge that reports itself as a daemon instance,
// with two registered projects and the agent records the server derives a
// session's associations from. Nothing in a request decides those: every
// candidate a view reports comes from what the store holds.
type studioAccessFixture struct {
	t             *testing.T
	s             *Server
	rootA, rootB  string
	projectA      string
	projectB      string
	daemonEnabled bool
}

func newStudioAccessFixture(t *testing.T) *studioAccessFixture {
	t.Helper()
	fx := &studioAccessFixture{t: t, s: NewServer(testFleet(t))}
	fx.s.SetDaemonInfo(DaemonInfo{
		Version:   "v1.2.3",
		Profile:   "local",
		PID:       4242,
		StartedAt: 1700000000000,
		Socket:    "/run/user/501/marshal/daemon.sock",
	})
	fx.daemonEnabled = true
	fx.rootA, fx.rootB = t.TempDir(), t.TempDir()
	fx.projectA = fx.addProject(fx.rootA)
	fx.projectB = fx.addProject(fx.rootB)
	return fx
}

// addProject registers one project root and reports its opaque ID.
func (fx *studioAccessFixture) addProject(root string) string {
	fx.t.Helper()
	if err := fx.s.fleet.ws.AddProject(root); err != nil {
		fx.t.Fatal(err)
	}
	id, ok := fx.s.fleet.ws.ProjectID(root)
	if !ok {
		fx.t.Fatal("a registered project has no project id")
	}
	return id
}

// agent records one tracked agent session, which is what makes the session one
// of a project's associations.
func (fx *studioAccessFixture) agent(id, sessionID, root, workerID string) {
	fx.t.Helper()
	a := Agent{ID: id, Project: root, SessionID: sessionID, OwnerID: DefaultOwnerID, Origin: OriginUI}
	if workerID != "" {
		a.FactoryItemID, a.FactoryRole = "item-1", "implementer"
	}
	if err := fx.s.fleet.ws.PutAgent(a); err != nil {
		fx.t.Fatal(err)
	}
}

// get reads the access view the surface starts from.
func (fx *studioAccessFixture) get(query string) MarshalAccessView {
	fx.t.Helper()
	rec := doReq(fx.t, fx.s, http.MethodGet, "/api/marshal/access"+query, nil, nil)
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("GET access%s: %d %s", query, rec.Code, rec.Body)
	}
	var view MarshalAccessView
	decodeBody(fx.t, rec, &view)
	return view
}

// put writes one layer, returning the status and the view the answer carries.
// A conflict is an answer, not a failure, so it is reported rather than fatal.
func (fx *studioAccessFixture) put(path string, body any) (int, MarshalAccessView) {
	fx.t.Helper()
	rec := doReq(fx.t, fx.s, http.MethodPut, path, body, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
		fx.t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
	}
	var view MarshalAccessView
	decodeBody(fx.t, rec, &view)
	return rec.Code, view
}

// putDefault stores the project's defaults the way an editor does: read the
// layer and its version, then write against it.
func (fx *studioAccessFixture) putDefault(access MarshalFactoryAccess) MarshalAccessView {
	fx.t.Helper()
	current := fx.get("?projectId=" + fx.projectA).Default
	code, view := fx.put("/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA, "access": access, "version": current.Version,
	})
	if code != http.StatusOK {
		fx.t.Fatalf("put default: status %d", code)
	}
	return view
}

// putOverride stores one session's override under the same rule.
func (fx *studioAccessFixture) putOverride(sessionID, workerID string, access MarshalFactoryAccess) MarshalAccessView {
	fx.t.Helper()
	query := "?projectId=" + fx.projectA + "&sessionId=" + sessionID
	if workerID != "" {
		query += "&workerId=" + workerID
	}
	current := fx.get(query)
	version := marshalAccessState(fx.projectA, sessionID, "", MarshalFactoryAccess{}).Version
	if current.Override != nil {
		version = current.Override.Version
	}
	code, view := fx.put("/api/marshal/access/override", map[string]any{
		"projectId": fx.projectA, "sessionId": sessionID, "workerId": workerID,
		"access": access, "version": version,
	})
	if code != http.StatusOK {
		fx.t.Fatalf("put override: status %d", code)
	}
	return view
}

// TestMarshalStudioAccessInheritanceAndReset proves the two rules the levels
// rest on: an unset field inherits the layer beneath it, and a reset to unset
// restores that inheritance instead of storing a denial.
func TestMarshalStudioAccessInheritanceAndReset(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")

	// Nothing stored: the read-only default, and the project's absent layer
	// still carries a version, so the first write is conditional on it.
	view := fx.get("?sessionId=s1")
	if view.State != marshalStateConnected || view.ProjectID != fx.projectA {
		t.Fatalf("view = %+v", view)
	}
	if view.Default.Version == "" || view.Override != nil {
		t.Fatalf("nothing stored yet: default %+v override %+v", view.Default, view.Override)
	}
	if !boolValue(view.Effective.Read) || boolValue(view.Effective.CreateDraft) || boolValue(view.Effective.EditDraft) {
		t.Fatalf("the effective default is not read-only: %+v", view.Effective)
	}

	editing := true
	view = fx.putDefault(MarshalFactoryAccess{CreateDraft: boolPtr(true), EditDraft: boolPtr(editing)})
	if !boolValue(view.Effective.CreateDraft) || !boolValue(view.Effective.EditDraft) {
		t.Fatalf("the project defaults the write stored: %+v", view.Effective)
	}

	// The session's override denies one field and leaves the rest unset: what
	// it leaves alone is inherited rather than defaulted.
	view = fx.putOverride("s1", "", MarshalFactoryAccess{EditDraft: boolPtr(false)})
	if boolValue(view.Effective.EditDraft) {
		t.Fatalf("the session's denial did not apply: %+v", view.Effective)
	}
	if !boolValue(view.Effective.CreateDraft) || !boolValue(view.Effective.Read) {
		t.Fatalf("the session lost what it inherited: %+v", view.Effective)
	}
	if view.Override == nil || view.Source["session"] != "override" || view.Source["project"] != "override" {
		t.Fatalf("sources = %+v, override = %+v", view.Source, view.Override)
	}

	// Resetting the field — writing a layer with nothing set — restores
	// inheritance. The denial is gone, not hidden behind an equal-looking value.
	view = fx.putOverride("s1", "", MarshalFactoryAccess{})
	if view.Override == nil {
		t.Fatal("a reset stores a layer, it does not delete the record")
	}
	if view.Override.Access.EditDraft != nil {
		t.Fatalf("the reset stored a value: %+v", view.Override.Access)
	}
	if !boolValue(view.Effective.EditDraft) {
		t.Fatalf("the reset did not restore inheritance: %+v", view.Effective)
	}

	// The same is true of the project level: resetting it returns the session
	// to the read-only default, and the version still moves.
	before := view.Default.Version
	view.Default = fx.get("?projectId=" + fx.projectA).Default
	code, reset := fx.put("/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA, "access": MarshalFactoryAccess{}, "version": before,
	})
	if code != http.StatusOK {
		t.Fatalf("reset default: status %d", code)
	}
	if boolValue(reset.Effective.CreateDraft) || boolValue(reset.Effective.EditDraft) {
		t.Fatalf("the default reset kept a grant: %+v", reset.Effective)
	}
}

// TestMarshalStudioAccessPerSessionDifferences proves two sessions of one
// project hold different effective access, and that a worker override belongs
// to the worker it names rather than to its session.
func TestMarshalStudioAccessPerSessionDifferences(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")
	fx.agent("a2", "s2", fx.rootA, "")

	fx.putDefault(MarshalFactoryAccess{CreateDraft: boolPtr(true)})
	fx.putOverride("s1", "", MarshalFactoryAccess{AnswerQuestion: boolPtr(true)})

	one := fx.get("?sessionId=s1")
	two := fx.get("?sessionId=s2")
	if !boolValue(one.Effective.AnswerQuestion) {
		t.Fatalf("s1 did not get its own answer capability: %+v", one.Effective)
	}
	if boolValue(two.Effective.AnswerQuestion) {
		t.Fatalf("s2 inherited its sibling's override: %+v", two.Effective)
	}
	if !boolValue(two.Effective.CreateDraft) || !boolValue(one.Effective.CreateDraft) {
		t.Fatalf("the project default did not reach both sessions: %+v %+v", one.Effective, two.Effective)
	}

	// A worker override names its worker: it applies to that worker, and a
	// sibling worker — and the session's own view, which names no worker —
	// inherits the project default instead.
	fx.putOverride("s3", "worker-a", MarshalFactoryAccess{EditDraft: boolPtr(false), CreateDraft: boolPtr(false)})
	worker := fx.get("?projectId=" + fx.projectA + "&sessionId=s3&workerId=worker-a")
	if boolValue(worker.Effective.CreateDraft) || boolValue(worker.Effective.EditDraft) {
		t.Fatalf("worker-a's override did not apply: %+v", worker.Effective)
	}
	if worker.Override == nil {
		t.Fatal("the worker's own view reported no override")
	}
	sibling := fx.get("?projectId=" + fx.projectA + "&sessionId=s3&workerId=worker-b")
	if !boolValue(sibling.Effective.CreateDraft) {
		t.Fatalf("worker-b inherited worker-a's denial: %+v", sibling.Effective)
	}
	if sibling.Override != nil {
		t.Fatalf("worker-b reported a sibling's override: %+v", sibling.Override)
	}
}

// TestMarshalStudioAccessDisabledRead proves read is a capability like any
// other: an explicit false denies it through inheritance, the other fields
// resolve on their own, and an override may re-grant it for one session.
func TestMarshalStudioAccessDisabledRead(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")

	fx.putDefault(MarshalFactoryAccess{Read: boolPtr(false)})
	view := fx.get("?sessionId=s1")
	if boolValue(view.Effective.Read) {
		t.Fatalf("read was disabled but the effective value allows it: %+v", view.Effective)
	}
	// The denial is one field, not a lock: a layer replaces only the fields it
	// carries, so the same view still reports the mutations as denied by the
	// read-only default beneath it.
	if boolValue(view.Effective.CreateDraft) || boolValue(view.Effective.EditDraft) {
		t.Fatalf("a read denial granted a mutation: %+v", view.Effective)
	}

	// An override may re-grant read for one session: the layers replace what
	// they carry, and nothing refuses a widening the operator asked for.
	view = fx.putOverride("s1", "", MarshalFactoryAccess{Read: boolPtr(true)})
	if !boolValue(view.Effective.Read) {
		t.Fatalf("the session's re-grant did not apply: %+v", view.Effective)
	}
	// The project default itself still denies read for everyone else.
	other := fx.get("?projectId=" + fx.projectA)
	if boolValue(other.Effective.Read) {
		t.Fatalf("the project default lost its denial: %+v", other.Effective)
	}
}

// TestMarshalStudioAccessStaleSaveConflict proves a save is conditional: a
// write naming a version that has moved is refused with 409, and the answer
// carries the value that won so the editor can show what it lost to.
func TestMarshalStudioAccessStaleSaveConflict(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")

	stale := fx.get("?projectId=" + fx.projectA).Default.Version
	first := fx.putDefault(MarshalFactoryAccess{CreateDraft: boolPtr(true)})

	code, conflict := fx.put("/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA,
		"access":    MarshalFactoryAccess{CreateDraft: boolPtr(false)},
		"version":   stale,
	})
	if code != http.StatusConflict {
		t.Fatalf("a stale default save: status %d, want 409", code)
	}
	if conflict.Default.Version != first.Default.Version {
		t.Fatalf("the conflict named version %q, the winning write left %q", conflict.Default.Version, first.Default.Version)
	}
	if !boolValue(conflict.Default.Access.CreateDraft) {
		t.Fatalf("the conflict did not carry the winning value: %+v", conflict.Default.Access)
	}
	// The losing write changed nothing.
	if stored, ok := fx.s.fleet.ws.marshalAccessDefault(fx.projectA); !ok || !boolValue(stored.Access.CreateDraft) {
		t.Fatalf("a refused write changed the store: %+v", stored)
	}

	// The same rule guards an override, and a write that names no version at
	// all is a malformed request rather than a conflict.
	overrideVersion := fx.get("?projectId=" + fx.projectA + "&sessionId=s1").SelectionVersion
	fx.putOverride("s1", "", MarshalFactoryAccess{AnswerQuestion: boolPtr(true)})
	code, conflict = fx.put("/api/marshal/access/override", map[string]any{
		"projectId": fx.projectA, "sessionId": "s1",
		"access": MarshalFactoryAccess{AnswerQuestion: boolPtr(false)}, "version": overrideVersion,
	})
	if code != http.StatusConflict {
		t.Fatalf("a stale override save: status %d, want 409", code)
	}
	if conflict.Override == nil || !boolValue(conflict.Override.Access.AnswerQuestion) {
		t.Fatalf("the override conflict did not carry the winner: %+v", conflict.Override)
	}
	if rec := doReq(t, fx.s, http.MethodPut, "/api/marshal/access/override", map[string]any{
		"projectId": fx.projectA, "sessionId": "s1", "access": MarshalFactoryAccess{},
	}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("an override write with no version: status %d, want 400", rec.Code)
	}
}

// TestMarshalStudioAccessStandingAuthority proves approval authority is chosen
// explicitly: user-directed and standing are both writable, and a layer that
// asks for approval with no authority at all is refused.
func TestMarshalStudioAccessStandingAuthority(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")

	view := fx.putDefault(MarshalFactoryAccess{
		ApproveAndQueue:   boolPtr(true),
		ApprovalAuthority: authorityPtr(factoryAuthorityStanding),
	})
	if view.Effective.ApprovalAuthority == nil || *view.Effective.ApprovalAuthority != factoryAuthorityStanding {
		t.Fatalf("standing authority was not stored: %+v", view.Effective)
	}

	userDirected := fx.putOverride("s1", "", MarshalFactoryAccess{ApprovalAuthority: authorityPtr(factoryAuthorityUserDirected)})
	if userDirected.Effective.ApprovalAuthority == nil || *userDirected.Effective.ApprovalAuthority != factoryAuthorityUserDirected {
		t.Fatalf("user-directed authority did not override the project's standing one: %+v", userDirected.Effective)
	}

	// Approval with no authority is a contradiction: it would authorize an
	// operation the resolution then refuses.
	current := fx.get("?projectId=" + fx.projectA).Default.Version
	rec := doReq(t, fx.s, http.MethodPut, "/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA,
		"access":    MarshalFactoryAccess{ApproveAndQueue: boolPtr(true), ApprovalAuthority: authorityPtr(factoryAuthorityNone)},
		"version":   current,
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approval with no authority: status %d, want 400", rec.Code)
	}
	rec = doReq(t, fx.s, http.MethodPut, "/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA,
		"access":    MarshalFactoryAccess{ApprovalAuthority: authorityPtr("sometimes")},
		"version":   current,
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown authority: status %d, want 400", rec.Code)
	}
}

// TestMarshalStudioIntegrationAccessAmbiguousInstanceSelection proves an
// ambiguous session reports its candidates and refuses to guess, that a
// person's selection resolves it, and that clearing the selection makes it
// ambiguous again.
func TestMarshalStudioIntegrationAccessAmbiguousInstanceSelection(t *testing.T) {
	fx := newStudioAccessFixture(t)
	// One session, tracked by two projects: the server cannot tell which it
	// acts on, and grants neither.
	fx.agent("a1", "shared", fx.rootA, "")
	fx.agent("a2", "shared", fx.rootB, "")

	view := fx.get("?sessionId=shared")
	if view.State != marshalStateAmbiguous {
		t.Fatalf("state = %q, want ambiguous", view.State)
	}
	if len(view.Candidates) != 2 || view.SelectedProjectID != "" {
		t.Fatalf("candidates = %v, selected = %q", view.Candidates, view.SelectedProjectID)
	}
	if view.InstanceID == "" || view.Generation == "" {
		t.Fatalf("the ambiguous view named no instance: %+v", view)
	}

	// A project that is not one of the candidates cannot be selected: a
	// selection answers the question the server asked.
	stranger := fx.addProject(t.TempDir())
	rec := doReq(t, fx.s, http.MethodPut, "/api/marshal/access/selection", map[string]any{
		"sessionId": "shared", "projectId": stranger, "version": view.SelectionVersion,
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("selecting a non-candidate: status %d, want 400", rec.Code)
	}

	code, chosen := fx.put("/api/marshal/access/selection", map[string]any{
		"sessionId": "shared", "projectId": fx.projectA, "version": view.SelectionVersion,
	})
	if code != http.StatusOK {
		t.Fatalf("selecting a candidate: status %d", code)
	}
	if chosen.State != marshalStateConnected || chosen.ProjectID != fx.projectA || chosen.SelectedProjectID != fx.projectA {
		t.Fatalf("the selection did not resolve the session: %+v", chosen)
	}

	// A second editor working from the pre-selection version loses, and is
	// told what won.
	code, conflict := fx.put("/api/marshal/access/selection", map[string]any{
		"sessionId": "shared", "projectId": fx.projectB, "version": view.SelectionVersion,
	})
	if code != http.StatusConflict {
		t.Fatalf("a stale selection save: status %d, want 409", code)
	}
	if conflict.SelectedProjectID != fx.projectA || conflict.SelectionVersion != chosen.SelectionVersion {
		t.Fatalf("the conflict did not carry the winning selection: %+v", conflict)
	}

	// Clearing the selection is the reset: the session is ambiguous again.
	code, cleared := fx.put("/api/marshal/access/selection", map[string]any{
		"sessionId": "shared", "version": chosen.SelectionVersion,
	})
	if code != http.StatusOK {
		t.Fatalf("clearing the selection: status %d", code)
	}
	if cleared.State != marshalStateAmbiguous || cleared.SelectedProjectID != "" {
		t.Fatalf("the cleared selection still resolved: %+v", cleared)
	}
	// The session is still tracked by both projects, so nothing was revoked.
	if len(cleared.Candidates) != 2 {
		t.Fatalf("candidates after the reset = %v", cleared.Candidates)
	}
}

// TestMarshalStudioIntegrationAccessOfflineStatus proves every state that is
// not a connection is reported as a state, and that none of them hides or
// blocks the stored policy.
func TestMarshalStudioIntegrationAccessOfflineStatus(t *testing.T) {
	fx := newStudioAccessFixture(t)

	// A session this instance does not track: unavailable, and it names no
	// project to edit.
	offline := fx.get("?sessionId=untracked")
	if offline.State != marshalStateUnavailable {
		t.Fatalf("state = %q, want unavailable", offline.State)
	}
	if offline.Editable {
		t.Fatalf("an untracked session reported an editable view: %+v", offline)
	}

	// Naming a project the server does not track for this session is
	// unavailable, and naming one the session is tracked by another project
	// for is unauthorized. Either way the project's defaults stay readable and
	// writable: they are the project's setting, not the session's.
	unauthorized := fx.get("?projectId=" + fx.projectA + "&sessionId=elsewhere")
	if unauthorized.State != marshalStateUnavailable || !unauthorized.Editable {
		t.Fatalf("unavailable view = %+v", unauthorized)
	}
	if unauthorized.Default.Version == "" {
		t.Fatal("an unavailable state hid the project's stored defaults")
	}
	fx.agent("a1", "s1", fx.rootA, "")
	mismatch := fx.get("?projectId=" + fx.projectB + "&sessionId=s1")
	if mismatch.State != marshalStateUnauthorized || !mismatch.Editable {
		t.Fatalf("unauthorized view = %+v", mismatch)
	}
	if mismatch.Default.Version == "" {
		t.Fatal("an unauthorized state hid the project's stored defaults")
	}

	// A bridge that is not a daemon instance reports unsupported, and still
	// serves the defaults an operator may edit.
	fx.s.SetDaemonInfo(DaemonInfo{})
	plain := fx.get("?projectId=" + fx.projectA)
	if plain.State != marshalStateUnsupported {
		t.Fatalf("state = %q, want unsupported", plain.State)
	}
	if !plain.Editable || plain.Default.Version == "" {
		t.Fatalf("an unsupported instance refused its own policy: %+v", plain)
	}

	// A write still lands while the instance reports no connection: the policy
	// is stored, and an offline state never blocks an ordinary edit.
	code, view := fx.put("/api/marshal/access/default", map[string]any{
		"projectId": fx.projectA,
		"access":    MarshalFactoryAccess{CreateDraft: boolPtr(true)},
		"version":   plain.Default.Version,
	})
	if code != http.StatusOK || !boolValue(view.Effective.CreateDraft) {
		t.Fatalf("an offline instance refused a policy write: %d %+v", code, view.Effective)
	}
}

// TestMarshalStudioAccessIsUserSideOnly proves the new operations are not
// reachable by a model-facing caller: they live on the /api/ mux, no MCP tool
// names them, and a request to the MCP endpoint changes no policy.
func TestMarshalStudioAccessIsUserSideOnly(t *testing.T) {
	fx := newStudioAccessFixture(t)
	fx.agent("a1", "s1", fx.rootA, "")
	before := fx.get("?projectId=" + fx.projectA).Default

	// The tool surface a model is offered names nothing that administers
	// access: every tool there acts on the fleet or on a factory item.
	for _, def := range mcpToolDefinitions() {
		name, _ := def["name"].(string)
		if strings.Contains(name, "access") || strings.Contains(name, "grant") || strings.Contains(name, "settings") {
			t.Fatalf("the model-facing tools expose %q", name)
		}
	}

	// An MCP-shaped call against the management path answers the endpoint's
	// own protocol error and writes nothing.
	for _, path := range []string{
		"/mcp/marshal/access/default",
		"/mcp/marshal/access/override",
		"/mcp/marshal/access/selection",
	} {
		rec := doReq(t, fx.s, http.MethodPut, path, map[string]any{
			"projectId": fx.projectA, "sessionId": "s1",
			"access": MarshalFactoryAccess{CreateDraft: boolPtr(true)}, "version": before.Version,
		}, nil)
		if strings.Contains(rec.Body.String(), `"result"`) {
			t.Fatalf("%s answered as a served call: %s", path, rec.Body)
		}
	}
	after := fx.get("?projectId=" + fx.projectA).Default
	if after.Version != before.Version {
		t.Fatalf("a request outside /api/ changed the policy: %+v -> %+v", before, after)
	}

	// The routes are on /api/, where the shared token guards them.
	if rec := doReq(t, fx.s, http.MethodGet, "/api/marshal/access?sessionId=s1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/marshal/access: %d %s", rec.Code, rec.Body)
	}
}

// TestMarshalStudioIntegrationAccessSelectionPersists proves a selection is a
// stored record like the rest: it survives a reload, and a workspace written
// before selections existed loads with none.
func TestMarshalStudioIntegrationAccessSelectionPersists(t *testing.T) {
	path := t.TempDir() + "/fleet.json"
	ws := NewWorkspace(path)
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	version := marshalSelectionState("s1", "").Version
	wrote, state, err := ws.putMarshalSelectionIfMatch("s1", "project-1", version)
	if err != nil || !wrote {
		t.Fatalf("the first selection write: wrote %v, err %v", wrote, err)
	}
	if state.ProjectID != "project-1" || state.Version != marshalSelectionVersion("project-1") {
		t.Fatalf("state = %+v", state)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.marshalSelection("s1")
	if !ok || got.ProjectID != "project-1" {
		t.Fatalf("the selection did not survive the reload: %+v (ok %v)", got, ok)
	}
	// Clearing it removes the record rather than storing an empty choice.
	if wrote, _, err := reloaded.putMarshalSelectionIfMatch("s1", "", state.Version); err != nil || !wrote {
		t.Fatalf("clearing the selection: wrote %v, err %v", wrote, err)
	}
	if _, ok := reloaded.marshalSelection("s1"); ok {
		t.Fatal("a cleared selection still reports one")
	}
}
