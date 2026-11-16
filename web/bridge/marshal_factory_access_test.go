package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// marshalAccessFixture is one bridge with a registered project and repository
// and a factory journal on disk: the harness the grant tests share, with no MCP
// client registered, because a built-in session reaches the factory through a
// verified binding rather than through a client record.
type marshalAccessFixture struct{ *factoryAuthFixture }

func newMarshalAccessFixture(t *testing.T) *marshalAccessFixture {
	t.Helper()
	return &marshalAccessFixture{factoryAuthFixture: newFactoryAuthFixture(t)}
}

// binding is one verified session association on this fixture's project.
func (fx *marshalAccessFixture) binding(sessionID string) MarshalSessionBinding {
	return MarshalSessionBinding{
		InstanceID: "instance-1",
		ProjectID:  fx.projectID,
		SessionID:  sessionID,
		Generation: "gen-1",
	}
}

// authorityPtr names one approval authority in a stored access value.
func authorityPtr(authority string) *string { return &authority }

// projectState is a project's stored defaults, or the state of an absent one:
// the zero access value and its stable version, which is what a first write is
// conditional on.
func (fx *marshalAccessFixture) projectState(t *testing.T) marshalFactoryAccessState {
	t.Helper()
	if state, ok := fx.s.fleet.ws.marshalAccessDefault(fx.projectID); ok {
		return state
	}
	return marshalAccessState(fx.projectID, "", "", MarshalFactoryAccess{})
}

// overrideState is one session's stored override, or the state of an absent
// one.
func (fx *marshalAccessFixture) overrideState(t *testing.T, sessionID string) marshalFactoryAccessState {
	t.Helper()
	if state, ok := fx.s.fleet.ws.marshalAccessOverride(fx.projectID, sessionID, ""); ok {
		return state
	}
	return marshalAccessState(fx.projectID, sessionID, "", MarshalFactoryAccess{})
}

// setDefault stores a project's built-in defaults the way an editor does: read
// the version, write against it.
func (fx *marshalAccessFixture) setDefault(t *testing.T, access MarshalFactoryAccess) marshalFactoryAccessState {
	t.Helper()
	current := fx.projectState(t)
	wrote, state, err := fx.s.factoryAuth.putMarshalAccessDefault(fx.projectID, access, current.Version)
	if err != nil || !wrote {
		t.Fatalf("put default: wrote %v, err %v", wrote, err)
	}
	return state
}

// setOverride stores one session's override under the same rule.
func (fx *marshalAccessFixture) setOverride(t *testing.T, sessionID, workerID string, access MarshalFactoryAccess) marshalFactoryAccessState {
	t.Helper()
	current := fx.overrideState(t, sessionID)
	wrote, state, err := fx.s.factoryAuth.putMarshalAccessOverride(fx.projectID, sessionID, workerID, access, current.Version)
	if err != nil || !wrote {
		t.Fatalf("put override: wrote %v, err %v", wrote, err)
	}
	return state
}

// draft is the candidate one built-in create proposes: its journal identity is
// namespaced to the authenticated principal, exactly as the tools path does.
func (fx *marshalAccessFixture) draft(p factoryPrincipal, requestID string) FactoryCreate {
	return FactoryCreate{
		RequestID: p.scoped(requestID),
		Title:     "handoff",
		Plan:      "plan",
		Spec:      "spec",
		Config:    FactoryConfig{RepoID: "app", ProjectRoot: fx.root, TargetBranch: "main", Native: true},
	}
}

// createDraft journals one draft for a built-in session, the way the tools path
// does: authorize and append under the policy lock, with provenance naming the
// request the session sent.
func (fx *marshalAccessFixture) createDraft(binding MarshalSessionBinding, requestID string) (FactoryItem, error) {
	return fx.s.factoryAuth.WithAuthorizedMarshalMutation(binding, "app", factoryCapabilityCreateDraft,
		func(p factoryPrincipal) (FactoryItem, error) {
			req := fx.draft(p, requestID)
			p.RequestID = requestID
			return fx.store.CreateAuthorized(req, req, p, time.Now().UTC())
		})
}

// TestMarshalFactoryAccessDefaultRead proves that a verified built-in session
// with nothing stored is read-only, and that a binding the server has not
// established gets nothing rather than the default.
func TestMarshalFactoryAccessDefaultRead(t *testing.T) {
	fx := newMarshalAccessFixture(t)
	a := fx.s.factoryAuth
	binding := fx.binding("s1")

	if err := a.AuthorizeMarshalRead(binding, "app"); err != nil {
		t.Fatalf("a verified session could not read: %v", err)
	}
	for _, capability := range []string{
		factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
		factoryCapabilityDelegatedAnswer, factoryCapabilityAssessPlanChanges,
		factoryCapabilityApproveRoutineRevision,
	} {
		if err := a.AuthorizeMarshalCapability(binding, "app", capability); !errors.Is(err, errFactoryDenied) {
			t.Fatalf("%s with nothing stored: err = %v, want denial", capability, err)
		}
	}
	// The denial is the one opaque value: it names no identity and no field.
	denied := a.AuthorizeMarshalCapability(binding, "app", factoryCapabilityCreateDraft)
	if denied.Error() != errFactoryDenied.Error() {
		t.Fatalf("the denial body = %q", denied.Error())
	}

	// The reported access has every pointer resolved to the read-only default.
	status, err := a.marshalStatus(binding)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != marshalStateConnected || status.InstanceID != binding.InstanceID ||
		status.ProjectID != fx.projectID || status.SessionID != "s1" {
		t.Fatalf("status = %+v", status)
	}
	if status.VerifiedAt == "" {
		t.Fatal("a verified status carries no verification time")
	}
	access := status.Access
	for _, field := range []struct {
		name  string
		value *bool
	}{
		{"read", access.Read}, {"createDraft", access.CreateDraft}, {"editDraft", access.EditDraft},
		{"answerQuestion", access.AnswerQuestion}, {"approveAndQueue", access.ApproveAndQueue},
	} {
		if field.value == nil {
			t.Fatalf("%s was reported unset: the control plane must resolve every pointer", field.name)
		}
	}
	if !*access.Read || *access.CreateDraft || *access.EditDraft || *access.AnswerQuestion || *access.ApproveAndQueue {
		t.Fatalf("the effective default is not read-only: %+v", access)
	}
	if access.ApprovalAuthority == nil || *access.ApprovalAuthority != factoryAuthorityNone {
		t.Fatalf("the effective default carries authority %v", access.ApprovalAuthority)
	}
	if status.Source["project"] != "default" {
		t.Fatalf("the reported source = %v", status.Source)
	}
	if _, ok := status.Source["session"]; ok {
		t.Fatal("a session override was reported for a project that stores none")
	}

	// A partly verified binding is a denial, not the default: every identity
	// the association is made of must be present.
	for name, broken := range map[string]MarshalSessionBinding{
		"no instance":      {ProjectID: fx.projectID, SessionID: "s1", Generation: "gen-1"},
		"no project":       {InstanceID: "instance-1", SessionID: "s1", Generation: "gen-1"},
		"no session":       {InstanceID: "instance-1", ProjectID: fx.projectID, Generation: "gen-1"},
		"no generation":    {InstanceID: "instance-1", ProjectID: fx.projectID, SessionID: "s1"},
		"blank generation": {InstanceID: "instance-1", ProjectID: fx.projectID, SessionID: "s1", Generation: " "},
	} {
		if err := a.AuthorizeMarshalRead(broken, "app"); !errors.Is(err, errFactoryDenied) {
			t.Fatalf("%s: err = %v, want denial", name, err)
		}
	}
	// A project ID this store did not mint names no project.
	unknown := binding
	unknown.ProjectID = "00000000000000000000000000000000"
	if err := a.AuthorizeMarshalRead(unknown, "app"); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("an unregistered project: err = %v, want denial", err)
	}
	// A capability this bridge does not define is a caller bug, not a denial.
	if err := a.AuthorizeMarshalCapability(binding, "app", "deleteProject"); !errors.Is(err, errFactoryCapabilityUnsupported) {
		t.Fatalf("an unknown capability: err = %v", err)
	}
}

// TestMarshalFactoryAccessOverridesAndReset proves the layer order and the two
// answers a pointer makes possible: an unset field inherits the layer beneath
// it, and an explicit false keeps denying.
func TestMarshalFactoryAccessOverridesAndReset(t *testing.T) {
	fx := newMarshalAccessFixture(t)
	a := fx.s.factoryAuth
	session, sibling := fx.binding("s1"), fx.binding("s2")

	// A project default reaches its sessions.
	fx.setDefault(t, MarshalFactoryAccess{CreateDraft: boolPtr(true)})
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityCreateDraft); err != nil {
		t.Fatalf("the project default did not reach a session: %v", err)
	}
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityEditDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a field the default leaves unset: err = %v, want denial", err)
	}

	// A session override replaces the fields it carries, and only those: the
	// explicit false denies drafting under a default that grants it, while the
	// sibling session keeps the default.
	fx.setOverride(t, "s1", "", MarshalFactoryAccess{CreateDraft: boolPtr(false), EditDraft: boolPtr(true)})
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityEditDraft); err != nil {
		t.Fatalf("the session override did not grant editing: %v", err)
	}
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityCreateDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("an explicit false was overridden by the default: err = %v", err)
	}
	if err := a.AuthorizeMarshalCapability(sibling, "app", factoryCapabilityCreateDraft); err != nil {
		t.Fatalf("one session's override reached another: %v", err)
	}
	status, err := a.marshalStatus(session)
	if err != nil {
		t.Fatal(err)
	}
	if status.Source["project"] != "override" || status.Source["session"] != "override" {
		t.Fatalf("the reported source = %v", status.Source)
	}

	// Resetting the override to unset restores inheritance: editing falls back
	// to the default's refusal and drafting to its grant.
	fx.setOverride(t, "s1", "", MarshalFactoryAccess{})
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityCreateDraft); err != nil {
		t.Fatalf("a reset field did not inherit the project default: %v", err)
	}
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityEditDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a reset field kept its old grant: err = %v", err)
	}

	// Approval needs an authority as well as the switch, and an authority this
	// bridge does not know — a hand-edited file — fails closed.
	fx.setDefault(t, MarshalFactoryAccess{ApproveAndQueue: boolPtr(true), ApprovalAuthority: authorityPtr("owner")})
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityApproveAndQueue); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("an unknown approval authority: err = %v, want denial", err)
	}
	fx.setDefault(t, MarshalFactoryAccess{ApproveAndQueue: boolPtr(true), ApprovalAuthority: authorityPtr(factoryAuthorityStanding)})
	if err := a.AuthorizeMarshalCapability(session, "app", factoryCapabilityApproveAndQueue); err != nil {
		t.Fatalf("a standing authority with the switch on: %v", err)
	}

	// A worker override names one worker: its sibling inherits the default, and
	// the role and item it carries widen nothing.
	fx.setDefault(t, MarshalFactoryAccess{})
	worker := fx.binding("s1")
	worker.WorkerID, worker.Role, worker.ItemID = "w1", "implementer", "item-1"
	fx.setOverride(t, "s1", "w1", MarshalFactoryAccess{CreateDraft: boolPtr(true)})
	if err := a.AuthorizeMarshalCapability(worker, "app", factoryCapabilityCreateDraft); err != nil {
		t.Fatalf("the worker's own override did not apply: %v", err)
	}
	other := worker
	other.WorkerID = "w2"
	if err := a.AuthorizeMarshalCapability(other, "app", factoryCapabilityCreateDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a worker override reached its sibling: err = %v", err)
	}
	if err := a.AuthorizeMarshalCapability(worker, "app", factoryCapabilityApproveAndQueue); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("an implementer's role widened its access: err = %v", err)
	}

	// Two editors naming one version: exactly one wins, and the loser is shown
	// the value that did.
	base := fx.projectState(t)
	if wrote, _, err := a.putMarshalAccessDefault(fx.projectID, MarshalFactoryAccess{CreateDraft: boolPtr(true)}, base.Version); err != nil || !wrote {
		t.Fatalf("the winning write: wrote %v, err %v", wrote, err)
	}
	wrote, current, err := a.putMarshalAccessDefault(fx.projectID, MarshalFactoryAccess{Read: boolPtr(false)}, base.Version)
	if err != nil || wrote {
		t.Fatalf("a stale write: wrote %v, err %v", wrote, err)
	}
	if current.Access.CreateDraft == nil || !*current.Access.CreateDraft {
		t.Fatalf("the conflict did not report the value that won: %+v", current.Access)
	}
	if _, _, err := a.putMarshalAccessDefault(fx.projectID, MarshalFactoryAccess{}, ""); err == nil {
		t.Fatal("a write naming no version was accepted")
	}

	// A save that fails restores the exact prior value.
	before := fx.projectState(t)
	breakWorkspacePersistence(t, fx.s.fleet.ws)
	if _, _, err := a.putMarshalAccessDefault(fx.projectID, MarshalFactoryAccess{EditDraft: boolPtr(true)}, before.Version); err == nil {
		t.Fatal("a write against a store that cannot save reported success")
	}
	if after := fx.projectState(t); after.Version != before.Version {
		t.Fatalf("a failed save changed the stored access: %q -> %q", before.Version, after.Version)
	}
}

// TestMarshalFactoryAccessTwoSessions proves the journal's request identity is
// bound to the authenticated principal, so two sessions in one project naming
// one request id cannot collide, while one session's own retry still replays.
func TestMarshalFactoryAccessTwoSessions(t *testing.T) {
	fx := newMarshalAccessFixture(t)
	fx.setDefault(t, MarshalFactoryAccess{CreateDraft: boolPtr(true)})
	first, second := fx.binding("s1"), fx.binding("s2")

	one, err := fx.createDraft(first, "handoff-1")
	if err != nil {
		t.Fatalf("the first session's draft: %v", err)
	}
	two, err := fx.createDraft(second, "handoff-1")
	if err != nil {
		t.Fatalf("the second session's draft: %v", err)
	}
	if one.ID == two.ID {
		t.Fatal("two sessions naming one request id share a work item")
	}
	if items := fx.store.List(); len(items) != 2 {
		t.Fatalf("the journal holds %d items, want two", len(items))
	}

	// The same session retrying the same request is the same operation.
	again, err := fx.createDraft(first, "handoff-1")
	if err != nil {
		t.Fatalf("the replay: %v", err)
	}
	if again.ID != one.ID {
		t.Fatalf("a retry created %q instead of replaying %q", again.ID, one.ID)
	}
	if items := fx.store.List(); len(items) != 2 {
		t.Fatalf("the replay wrote a second item: the journal holds %d", len(items))
	}

	// The scope is the association, not the request id alone.
	scope := func(b MarshalSessionBinding, workerID string) string {
		b.WorkerID = workerID
		p := b.principal(factoryCapabilityCreateDraft, marshalFactoryAccessDefault())
		return p.scoped("handoff-1")
	}
	if scope(first, "") == scope(second, "") {
		t.Fatal("two sessions scope one request id to one identity")
	}
	if scope(first, "") == scope(first, "w1") {
		t.Fatal("a worker's request is scoped as its session's")
	}
	if one.Draft.RequestID != scope(first, "") || two.Draft.RequestID != scope(second, "") {
		t.Fatalf("the journal identities %q and %q are not scoped to their sessions", one.Draft.RequestID, two.Draft.RequestID)
	}
	// An MCP client's scope is exactly the one it always had.
	client := factoryPrincipal{ClientID: "c1", ProjectID: fx.projectID}
	if client.scoped("handoff-1") != factoryScopedRequestID("c1", "handoff-1") {
		t.Fatal("an MCP client's request identity moved")
	}

	// Provenance names the authenticated principal and the request the session
	// sent, and its authority origin is the store's.
	for _, item := range []FactoryItem{one, two} {
		if len(item.History) != 1 || item.History[0].Provenance == nil {
			t.Fatalf("item %q journalled %d entries, want one attribution", item.ID, len(item.History))
		}
		provenance := *item.History[0].Provenance
		if provenance.ClientID != "" || provenance.InstanceID != first.InstanceID ||
			provenance.ProjectID != fx.projectID || provenance.Action != factoryCapabilityCreateDraft ||
			provenance.RequestID != "handoff-1" || provenance.AuthorityOrigin != factoryAuthorityOriginStanding {
			t.Fatalf("provenance = %+v", provenance)
		}
	}
	if one.History[0].Provenance.SessionID != "s1" || two.History[0].Provenance.SessionID != "s2" {
		t.Fatalf("the two sessions were journalled as %q and %q",
			one.History[0].Provenance.SessionID, two.History[0].Provenance.SessionID)
	}
}

// TestMarshalFactoryAccessRevocationRace is the model of the grant race, for a
// built-in principal: a revocation cannot land between an admission and its
// journal append, and it is in force for the next operation.
func TestMarshalFactoryAccessRevocationRace(t *testing.T) {
	fx := newMarshalAccessFixture(t)
	a := fx.s.factoryAuth
	binding := fx.binding("s1")
	override := fx.setOverride(t, "s1", "", MarshalFactoryAccess{CreateDraft: boolPtr(true)})
	if err := a.AuthorizeMarshalCapability(binding, "app", factoryCapabilityCreateDraft); err != nil {
		t.Fatalf("the stored override was not in force: %v", err)
	}

	admitted := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		item FactoryItem
		err  error
	}
	mutated := make(chan outcome, 1)
	// The mutation is admitted, then waits before its journal append — the
	// window a revocation must not be able to land in.
	go func() {
		item, err := a.WithAuthorizedMarshalMutation(binding, "app", factoryCapabilityCreateDraft,
			func(p factoryPrincipal) (FactoryItem, error) {
				close(admitted)
				<-release
				req := fx.draft(p, "race-1")
				p.RequestID = "race-1"
				return fx.store.CreateAuthorized(req, req, p, time.Now().UTC())
			})
		mutated <- outcome{item: item, err: err}
	}()
	<-admitted

	type revocation struct {
		wrote bool
		err   error
	}
	revoking := make(chan struct{})
	revoked := make(chan revocation, 1)
	// Resetting the override to unset revokes the capability the mutation was
	// admitted under.
	go func() {
		close(revoking)
		wrote, _, err := a.putMarshalAccessOverride(fx.projectID, "s1", "", MarshalFactoryAccess{}, override.Version)
		revoked <- revocation{wrote: wrote, err: err}
	}()
	<-revoking
	select {
	case got := <-revoked:
		t.Fatalf("the revocation returned (wrote %v, err %v) while a mutation was admitted and not yet committed", got.wrote, got.err)
	default:
	}

	close(release)
	got := <-mutated
	if got.err != nil || got.item.ID == "" {
		t.Fatalf("the admitted mutation: item %q, err %v", got.item.ID, got.err)
	}
	if result := <-revoked; result.err != nil || !result.wrote {
		t.Fatalf("the revocation after the commit: wrote %v, err %v", result.wrote, result.err)
	}

	// The revocation is in force for the next operation, and the item the
	// earlier one committed stays: revoking authority is not retroactive.
	called := false
	if _, err := a.WithAuthorizedMarshalMutation(binding, "app", factoryCapabilityCreateDraft,
		func(factoryPrincipal) (FactoryItem, error) {
			called = true
			return FactoryItem{}, nil
		}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a mutation after the revocation: err = %v, want denial", err)
	}
	if called {
		t.Fatal("the callback ran after the override was reset")
	}
	if items := fx.store.List(); len(items) != 1 || items[0].ID != got.item.ID {
		t.Fatalf("the journal holds %d items, want the one committed before the revocation", len(items))
	}
	// The default it fell back to is still read-only, not nothing at all.
	if err := a.AuthorizeMarshalRead(binding, "app"); err != nil {
		t.Fatalf("reading after the revocation: %v", err)
	}
}

// TestMarshalFactoryAccessExternalMCPUnchanged proves the built-in default
// never reaches an external MCP client, and that its grant, its version, its
// scope and its provenance are the ones it always had.
func TestMarshalFactoryAccessExternalMCPUnchanged(t *testing.T) {
	fx := newMarshalAccessFixture(t)
	a := fx.s.factoryAuth
	// One external client holding exactly read on this project.
	testAddClient(t, fx.s, MCPClient{
		ID: "c1", Name: "claude",
		FactoryGrants: []MCPFactoryProjectGrant{{
			ProjectID: fx.projectID, Read: true, ApprovalAuthority: factoryAuthorityNone,
		}},
	})
	before, ok := fx.s.fleet.ws.FactoryGrant("c1", fx.projectID)
	if !ok {
		t.Fatal("the client's grant is missing")
	}

	// The built-in defaults grant a project's sessions what this client was
	// never granted.
	fx.setDefault(t, MarshalFactoryAccess{CreateDraft: boolPtr(true), EditDraft: boolPtr(true)})

	if err := a.AuthorizeCapability("c1", fx.projectID, "app", factoryCapabilityRead); err != nil {
		t.Fatalf("the client's own read grant: %v", err)
	}
	if err := a.AuthorizeMarshalRead(fx.binding("s1"), "app"); err != nil {
		t.Fatalf("the built-in read default: %v", err)
	}
	if err := a.AuthorizeCapability("c1", fx.projectID, "app", factoryCapabilityCreateDraft); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a built-in default reached an external client: err = %v", err)
	}
	// A client holding no grant gets nothing — not the built-in read default.
	testAddClient(t, fx.s, MCPClient{ID: "c2", Name: "other"})
	if err := a.AuthorizeCapability("c2", fx.projectID, "app", factoryCapabilityRead); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a client with no grant: err = %v, want denial", err)
	}
	// The mutation path is still the grant's, and it never runs the callback.
	called := false
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft,
		func() (FactoryItem, error) {
			called = true
			return FactoryItem{}, nil
		}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a client mutation under a built-in default: err = %v", err)
	}
	if called {
		t.Fatal("the callback ran for a capability the client was never granted")
	}

	// A built-in access write leaves the client's grant and its version alone.
	after, ok := fx.s.fleet.ws.FactoryGrant("c1", fx.projectID)
	if !ok || after.Version != before.Version || after.Grant != before.Grant {
		t.Fatalf("the client's grant moved: %+v -> %+v", before, after)
	}

	// A client principal keeps the scope and the provenance bytes it always
	// had: no field of the built-in extension appears.
	client := factoryPrincipal{
		ClientID: "c1", ProjectID: fx.projectID, Action: factoryCapabilityCreateDraft, RequestID: "r1",
	}
	if got, want := client.scoped("r1"), factoryScopedRequestID("c1", "r1"); got != want {
		t.Fatalf("client scope = %q, want %q", got, want)
	}
	want := `{"clientId":"c1","projectId":"` + fx.projectID + `","action":"createDraft","requestId":"r1"}`
	if got := string(mustJSON(t, client.provenance(""))); got != want {
		t.Fatalf("client provenance = %s, want %s", got, want)
	}
}

// TestMarshalFactoryAccessLegacyReplay proves a journal record written before
// a built-in principal existed still replays and re-marshals byte for byte, and
// that a workspace file from before the access records loads with its client
// grants intact.
func TestMarshalFactoryAccessLegacyReplay(t *testing.T) {
	// The provenance shape such a record carries: no instance, session, worker,
	// role or authority origin.
	oldProvenance := `{"clientId":"c1","projectId":"p1","action":"createDraft","requestId":"legacy"}`
	dir := t.TempDir()
	now := time.Now().UTC()
	req := factoryReq()
	item := FactoryItem{
		ID: "legacy-item", Title: req.Title, State: "draft", Version: 1,
		RepoID: req.Config.RepoID, Branch: "marshal/factory/legacy-item", Draft: req,
		History: []FactoryEvent{{
			At: now, Kind: "create", Message: "draft created",
			Provenance: &FactoryProvenance{ClientID: "c1", ProjectID: "p1", Action: factoryCapabilityCreateDraft, RequestID: "legacy"},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	payload, err := json.Marshal(factoryPayload{
		Format: 1, Sequence: 1, RequestID: "legacy", ItemID: item.ID, CommandHash: "legacy", Item: item,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(oldProvenance)) {
		t.Fatalf("the legacy payload does not carry the old provenance shape: %s", payload)
	}
	line, err := json.Marshal(factoryRecord{Payload: payload, Checksum: factoryDigest(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	// The store re-encodes every replayed record and refuses one whose canonical
	// bytes differ from the payload it read, so opening the journal proves the
	// record re-marshals unchanged.
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatalf("the legacy journal did not open: %v", err)
	}
	defer store.Close()
	got, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	provenance := got.History[0].Provenance
	if provenance == nil || provenance.ClientID != "c1" || provenance.ProjectID != "p1" ||
		provenance.Action != factoryCapabilityCreateDraft || provenance.RequestID != "legacy" {
		t.Fatalf("the legacy provenance is no longer readable: %+v", provenance)
	}
	if provenance.InstanceID != "" || provenance.SessionID != "" || provenance.WorkerID != "" ||
		provenance.Role != "" || provenance.AuthorityOrigin != "" {
		t.Fatalf("a legacy record gained a built-in field: %+v", *provenance)
	}
	if raw := string(mustJSON(t, got.History[0])); !bytes.Contains([]byte(raw), []byte(oldProvenance)) {
		t.Fatalf("a replayed legacy entry re-marshals as %s", raw)
	}

	// A workspace file written before the access records existed: one client
	// with a grant, and none of the new keys.
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "fleet.json")
	legacy, err := json.MarshalIndent(workspaceFile{
		Version:  workspaceVersion,
		Projects: []string{root},
		Agents:   []Agent{},
		Clients: []MCPClient{{
			ID: "c1", Name: "claude", OwnerID: DefaultOwnerID,
			FactoryGrants: []MCPFactoryProjectGrant{{
				ProjectID: "project-old", Read: true, ApprovalAuthority: factoryAuthorityNone,
			}},
		}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("marshalFactory")) {
		t.Fatalf("a workspace with no built-in records wrote one: %s", legacy)
	}
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	if backup, err := ws.Load(); err != nil || backup != "" {
		t.Fatalf("the legacy workspace did not load: backup %q, err %v", backup, err)
	}
	projectID, ok := ws.ProjectID(root)
	if !ok {
		t.Fatal("the loaded project has no id")
	}
	if _, ok := ws.marshalAccessDefault(projectID); ok {
		t.Fatal("a legacy workspace reported built-in defaults it never held")
	}
	// One access write, then a reload: the client's grant survives it byte for
	// byte, and the new record is the only thing that was added.
	absent := marshalAccessState(projectID, "", "", MarshalFactoryAccess{})
	if wrote, _, err := ws.putMarshalAccessDefaultIfMatch(projectID, MarshalFactoryAccess{CreateDraft: boolPtr(true)}, absent.Version); err != nil || !wrote {
		t.Fatalf("the first access write: wrote %v, err %v", wrote, err)
	}
	reloaded := NewWorkspace(path)
	if backup, err := reloaded.Load(); err != nil || backup != "" {
		t.Fatalf("the saved workspace did not load: backup %q, err %v", backup, err)
	}
	client, ok := reloaded.Client("c1")
	if !ok || len(client.FactoryGrants) != 1 {
		t.Fatalf("the legacy client record was not preserved: %+v", client)
	}
	grant := client.FactoryGrants[0]
	if grant.ProjectID != "project-old" || !grant.Read || grant.ApprovalAuthority != factoryAuthorityNone {
		t.Fatalf("the preserved grant = %+v", grant)
	}
	state, ok := reloaded.marshalAccessDefault(projectID)
	if !ok || state.Access.CreateDraft == nil || !*state.Access.CreateDraft {
		t.Fatalf("the saved default did not survive the reload: %+v", state.Access)
	}
	if state.Access.Read != nil {
		t.Fatalf("a write added a field it never named: %+v", state.Access)
	}
}
