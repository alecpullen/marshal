package bridge

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// factoryAuthFixture is one server over a workspace holding a registered
// project, a registered repo and a factory journal on disk: the three things a
// grant, a management write and an authorized mutation all touch.
type factoryAuthFixture struct {
	t         *testing.T
	s         *Server
	root      string
	projectID string
	store     *FactoryStore
}

func newFactoryAuthFixture(t *testing.T) *factoryAuthFixture {
	t.Helper()
	s := testServer(t)
	root := t.TempDir()
	if err := s.fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	projectID, ok := s.fleet.ws.ProjectID(root)
	if !ok {
		t.Fatal("a registered project has no factory id")
	}
	if err := s.fleet.ws.PutRepo(Repo{
		ID: "app", URL: "https://example.com/app.git", Branch: "main",
		OwnerID: DefaultOwnerID, Forge: "github",
	}); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &factoryAuthFixture{t: t, s: s, root: root, projectID: projectID, store: store}
}

func (fx *factoryAuthFixture) addClient(id string, c MCPClient) {
	fx.t.Helper()
	c.ID = id
	testAddClient(fx.t, fx.s, c)
}

func (fx *factoryAuthFixture) grantPath(clientID string) string {
	return "/api/clients/" + clientID + "/factory-grants/" + fx.projectID
}

// readGrant is the GET the grant editor starts from.
func (fx *factoryAuthFixture) readGrant(clientID string) FactoryGrantState {
	fx.t.Helper()
	rec := doReq(fx.t, fx.s, http.MethodGet, fx.grantPath(clientID), nil, nil)
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("GET grant: %d %s", rec.Code, rec.Body)
	}
	var state FactoryGrantState
	decodeBody(fx.t, rec, &state)
	return state
}

// readGrantRec is the same GET as the response the test inspects, for the
// assertions about bodies that must not leak.
func (fx *factoryAuthFixture) readGrantRec(clientID, projectID string) int {
	rec := doReq(fx.t, fx.s, http.MethodGet, "/api/clients/"+clientID+"/factory-grants/"+projectID, nil, nil)
	return rec.Code
}

// setGrant stores a grant the way the editor does: read the version, write
// against it.
func (fx *factoryAuthFixture) setGrant(clientID string, g MCPFactoryProjectGrant) FactoryGrantState {
	fx.t.Helper()
	version := fx.readGrant(clientID).Version
	rec := doReq(fx.t, fx.s, http.MethodPut, fx.grantPath(clientID),
		map[string]any{"grant": fx.scoped(g), "version": version}, nil)
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("PUT grant: %d %s", rec.Code, rec.Body)
	}
	var state FactoryGrantState
	decodeBody(fx.t, rec, &state)
	return state
}

// forceGrant writes a grant straight into the workspace, bypassing the
// management API's validation: it stands in for a hand-edited file that the
// editor would never produce.
func (fx *factoryAuthFixture) forceGrant(clientID string, g MCPFactoryProjectGrant) {
	fx.t.Helper()
	c, ok := fx.s.fleet.ws.Client(clientID)
	if !ok {
		fx.t.Fatalf("no client %q", clientID)
	}
	c.FactoryGrants = append(c.FactoryGrants[:0:0], g)
	c.FactoryGrants[0].ProjectID = fx.projectID
	if err := fx.s.fleet.ws.PutClient(c); err != nil {
		fx.t.Fatal(err)
	}
}

// scoped returns g naming this fixture's project, which is what a grant body
// must carry.
func (fx *factoryAuthFixture) scoped(g MCPFactoryProjectGrant) MCPFactoryProjectGrant {
	g.ProjectID = fx.projectID
	return g
}

// appendItem is the body of an authorized mutation: the one durable factory
// write, performed while the policy lock is held.
func (fx *factoryAuthFixture) appendItem(requestID string) (FactoryItem, error) {
	fx.t.Helper()
	return fx.store.Create(FactoryCreate{
		RequestID: requestID,
		Title:     "handoff",
		Plan:      "plan",
		Spec:      "spec",
		Config:    FactoryConfig{RepoID: "app", ProjectRoot: fx.root, TargetBranch: "main", Native: true},
	}, time.Now().UTC())
}

// settingsPayload is this project's settings with its delegation fields set,
// as the Studio route writes them.
func (fx *factoryAuthFixture) settingsPayload(enabled bool, clientID string) ProjectSettings {
	ps := fx.s.fleet.ws.ProjectSettingsFor(fx.root)
	ps.PlannerDelegationEnabled = enabled
	ps.DesignatedMCPClientID = clientID
	return ps
}

// putSettingsStatus writes project settings and returns the status, so a
// goroutine can report an outcome without failing the test itself.
func (fx *factoryAuthFixture) putSettingsStatus(ps ProjectSettings) int {
	rec := doReq(fx.t, fx.s, http.MethodPut, "/api/projects/settings?root="+url.QueryEscape(fx.root), ps, nil)
	return rec.Code
}

// fullGrant is every capability the editor can switch on.
func fullGrant() MCPFactoryProjectGrant {
	return MCPFactoryProjectGrant{
		Read: true, CreateDraft: true, EditDraft: true,
		AnswerQuestion: true, ApproveAndQueue: true,
		ApprovalAuthority: factoryAuthorityUserDirected,
	}
}

// breakWorkspacePersistence makes the workspace's next save fail, so a
// management write can be tested against a store that cannot persist. The
// path's directory is replaced by a regular file, so MkdirAll fails on every
// save, exactly as it does for a bridge whose data directory is gone.
func breakWorkspacePersistence(t *testing.T, ws *Workspace) {
	t.Helper()
	dir := filepath.Dir(ws.path)
	if err := os.Rename(dir, filepath.Join(t.TempDir(), "workspace-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPFactoryAuthorizeCapabilities(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "claude"})
	a := fx.s.factoryAuth

	// A registered client holding no grant authorizes nothing, reads
	// included, and the callback never runs.
	for _, capability := range []string{
		factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityDelegatedAnswer, factoryCapabilityApproveAndQueue,
	} {
		called := false
		if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", capability, func() (FactoryItem, error) {
			called = true
			return fx.appendItem("ungranted")
		}); !errors.Is(err, errFactoryDenied) {
			t.Fatalf("%s with no grant: err = %v, want denial", capability, err)
		}
		if called {
			t.Fatalf("%s ran its mutation with no grant", capability)
		}
	}
	if err := a.AuthorizeRead("c1", fx.projectID, "app"); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("read with no grant: err = %v, want denial", err)
	}

	// Read alone admits reads and nothing else.
	fx.setGrant("c1", MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone})
	if err := a.AuthorizeRead("c1", fx.projectID, "app"); err != nil {
		t.Fatalf("granted read denied: %v", err)
	}
	created := 0
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		created++
		return fx.appendItem("read-only")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("createDraft on a read-only grant: err = %v, want denial", err)
	}
	if created != 0 {
		t.Fatal("a read-only grant ran a create")
	}

	// Each capability admits exactly its own operation.
	fx.setGrant("c1", fullGrant())
	for _, capability := range []string{
		factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue, factoryCapabilityRead,
	} {
		item, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", capability, func() (FactoryItem, error) {
			return fx.appendItem("granted-" + capability)
		})
		if err != nil || item.ID == "" {
			t.Fatalf("%s on a full grant: item %q, err %v", capability, item.ID, err)
		}
	}

	// Approval needs a configured authority as well as the boolean: a grant
	// saying yes without one cannot approve.
	fx.forceGrant("c1", MCPFactoryProjectGrant{ApproveAndQueue: true, ApprovalAuthority: factoryAuthorityNone})
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
		return fx.appendItem("no-authority")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("approve with authority none: err = %v, want denial", err)
	}
	// An authority this bridge does not know fails closed rather than being
	// trusted.
	fx.forceGrant("c1", MCPFactoryProjectGrant{ApproveAndQueue: true, ApprovalAuthority: "owner"})
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
		return fx.appendItem("unknown-authority")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("approve with an unknown authority: err = %v, want denial", err)
	}
	fx.forceGrant("c1", MCPFactoryProjectGrant{ApproveAndQueue: true, ApprovalAuthority: factoryAuthorityStanding})
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
		return fx.appendItem("standing")
	}); err != nil {
		t.Fatalf("approve under standing authority: %v", err)
	}

	// A grant never widens the client's fleet repository allowlist.
	fx.setGrant("c1", fullGrant())
	c, _ := fx.s.fleet.ws.Client("c1")
	c.AllowedRepos = []string{"other"}
	if err := fx.s.fleet.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizeRead("c1", fx.projectID, "app"); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("read of a repo outside the allowlist: err = %v, want denial", err)
	}
	// An empty allowlist means every *registered* repo, and no other.
	c.AllowedRepos = nil
	if err := fx.s.fleet.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizeRead("c1", fx.projectID, "unregistered"); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("read of an unregistered repo: err = %v, want denial", err)
	}
	// A capability this bridge does not define is a caller error, not a
	// denial: it says nothing about the client's authority.
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", "merge", func() (FactoryItem, error) {
		return fx.appendItem("merge")
	}); !errors.Is(err, errFactoryCapabilityUnsupported) {
		t.Fatalf("unknown capability: err = %v, want unsupported", err)
	}
}

func TestMCPFactoryUnauthorizedIDDoesNotLeak(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "claude-desk"})
	fx.setGrant("c1", MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone})

	other := t.TempDir()
	if err := fx.s.fleet.ws.AddProject(other); err != nil {
		t.Fatal(err)
	}
	otherID, ok := fx.s.fleet.ws.ProjectID(other)
	if !ok {
		t.Fatal("the second project has no id")
	}
	unknownID := strings.Repeat("f", 32)
	a := fx.s.factoryAuth

	// An ID that was never minted and a registered project this client holds
	// no grant for are one answer: nothing may distinguish them.
	unknown := a.AuthorizeRead("c1", unknownID, "app")
	ungranted := a.AuthorizeRead("c1", otherID, "app")
	if unknown == nil || ungranted == nil {
		t.Fatalf("unknown %v, ungranted %v: both must be refused", unknown, ungranted)
	}
	if unknown.Error() != ungranted.Error() {
		t.Fatalf("an unauthorized project ID is distinguishable from an unknown one: %q vs %q", unknown, ungranted)
	}
	// So is a client the store never held, next to one holding no grant.
	noClient := a.AuthorizeRead("nobody", fx.projectID, "app")
	if noClient == nil || noClient.Error() != unknown.Error() {
		t.Fatalf("an unknown client answers %q, an unknown project %q", noClient, unknown)
	}
	for _, leak := range []string{otherID, other, fx.projectID, fx.root, "claude-desk", "nobody"} {
		for _, err := range []error{unknown, ungranted, noClient} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("denial %q names %q", err, leak)
			}
		}
	}

	// The management route is just as opaque, and its two failures are the
	// same response.
	unknownClient := doReq(t, fx.s, http.MethodGet, "/api/clients/nobody/factory-grants/"+fx.projectID, nil, nil)
	unknownProject := doReq(t, fx.s, http.MethodGet, "/api/clients/c1/factory-grants/"+strings.Repeat("e", 32), nil, nil)
	if unknownClient.Code != http.StatusNotFound || unknownProject.Code != http.StatusNotFound {
		t.Fatalf("unknown client %d, unknown project %d: both want 404", unknownClient.Code, unknownProject.Code)
	}
	if unknownClient.Body.String() != unknownProject.Body.String() {
		t.Fatalf("the two 404s differ: %q vs %q", unknownClient.Body, unknownProject.Body)
	}
	for _, leak := range []string{fx.projectID, fx.root, "c1", "nobody"} {
		if strings.Contains(unknownClient.Body.String(), leak) {
			t.Fatalf("the 404 body names %q: %s", leak, unknownClient.Body)
		}
	}
	if unknownClient.Body.String() != `{"error":"unknown client or project"}`+"\n" {
		t.Fatalf("the 404 body is %s", unknownClient.Body)
	}
}

func TestMCPFactoryGrantRevokeRacesMutation(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "claude"})
	fx.setGrant("c1", MCPFactoryProjectGrant{CreateDraft: true, ApprovalAuthority: factoryAuthorityNone})
	a := fx.s.factoryAuth
	version := fx.readGrant("c1").Version

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
		item, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
			close(admitted)
			<-release
			return fx.appendItem("mcp:race-1")
		})
		mutated <- outcome{item: item, err: err}
	}()
	<-admitted

	revoking := make(chan struct{})
	revoked := make(chan int, 1)
	go func() {
		close(revoking)
		rec := doReq(t, fx.s, http.MethodPut, fx.grantPath("c1"), map[string]any{
			"grant":   fx.scoped(MCPFactoryProjectGrant{ApprovalAuthority: factoryAuthorityNone}),
			"version": version,
		}, nil)
		revoked <- rec.Code
	}()
	<-revoking
	select {
	case status := <-revoked:
		t.Fatalf("the revocation returned %d while a mutation was admitted and not yet committed", status)
	default:
	}

	close(release)
	got := <-mutated
	if got.err != nil || got.item.ID == "" {
		t.Fatalf("the admitted mutation: item %q, err %v", got.item.ID, got.err)
	}
	if status := <-revoked; status != http.StatusOK {
		t.Fatalf("the revocation after the commit = %d, want 200", status)
	}

	// The revocation is in force for the next operation, and the item the
	// earlier one committed stays: revoking authority is not retroactive.
	called := false
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		called = true
		return fx.appendItem("mcp:race-2")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a mutation after the revocation: err = %v, want denial", err)
	}
	if called {
		t.Fatal("the callback ran after the grant was revoked")
	}
	items := fx.store.List()
	if len(items) != 1 || items[0].ID != got.item.ID {
		t.Fatalf("the journal holds %d items, want the one committed before the revocation", len(items))
	}
}

func TestMCPFactoryDelegationRevokeRacesAnswer(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "planner"})
	fx.setGrant("c1", MCPFactoryProjectGrant{AnswerQuestion: true, ApprovalAuthority: factoryAuthorityNone})
	a := fx.s.factoryAuth

	// Delegation is off by default: a delegated answer is refused even though
	// the capability is granted, and a user-directed answer is not.
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityDelegatedAnswer, func() (FactoryItem, error) {
		return fx.appendItem("delegated-off")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("delegated answer with delegation off: err = %v, want denial", err)
	}
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityAnswerQuestion, func() (FactoryItem, error) {
		return fx.appendItem("user-directed")
	}); err != nil {
		t.Fatalf("user-directed answer with delegation off: %v", err)
	}

	if status := fx.putSettingsStatus(fx.settingsPayload(true, "c1")); status != http.StatusOK {
		t.Fatalf("enable delegation: %d", status)
	}
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityDelegatedAnswer, func() (FactoryItem, error) {
		return fx.appendItem("delegated-on")
	}); err != nil {
		t.Fatalf("delegated answer while designated: %v", err)
	}

	// One client's designation does not delegate to another.
	fx.addClient("c2", MCPClient{Name: "other"})
	fx.setGrant("c2", MCPFactoryProjectGrant{AnswerQuestion: true, ApprovalAuthority: factoryAuthorityNone})
	if _, err := a.WithAuthorizedMutation("c2", fx.projectID, "app", factoryCapabilityDelegatedAnswer, func() (FactoryItem, error) {
		return fx.appendItem("not-designated")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("delegated answer by a client that is not designated: err = %v, want denial", err)
	}

	// An admitted answer and a revocation of the delegation cannot interleave:
	// the settings write shares the policy lock.
	revokedSettings := fx.settingsPayload(false, "")
	admitted := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		item FactoryItem
		err  error
	}
	answered := make(chan outcome, 1)
	go func() {
		item, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityDelegatedAnswer, func() (FactoryItem, error) {
			close(admitted)
			<-release
			return fx.appendItem("delegated-race")
		})
		answered <- outcome{item: item, err: err}
	}()
	<-admitted

	revoking := make(chan struct{})
	revoked := make(chan int, 1)
	go func() {
		close(revoking)
		revoked <- fx.putSettingsStatus(revokedSettings)
	}()
	<-revoking
	select {
	case status := <-revoked:
		t.Fatalf("the delegation revocation returned %d while an answer was admitted and not yet committed", status)
	default:
	}

	close(release)
	got := <-answered
	if got.err != nil || got.item.ID == "" {
		t.Fatalf("the admitted answer: item %q, err %v", got.item.ID, got.err)
	}
	if status := <-revoked; status != http.StatusOK {
		t.Fatalf("the delegation revocation after the commit = %d, want 200", status)
	}

	// Delegation is gone for the next answer; a user-directed answer, which
	// does not consult the toggle, is still allowed.
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityDelegatedAnswer, func() (FactoryItem, error) {
		return fx.appendItem("delegated-after")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a delegated answer after the revocation: err = %v, want denial", err)
	}
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityAnswerQuestion, func() (FactoryItem, error) {
		return fx.appendItem("user-directed-after")
	}); err != nil {
		t.Fatalf("a user-directed answer after the delegation revocation: %v", err)
	}
}

func TestMCPFactoryDeletedClientDeniesReplay(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "claude"})
	fx.setGrant("c1", MCPFactoryProjectGrant{CreateDraft: true, ApprovalAuthority: factoryAuthorityNone})
	a := fx.s.factoryAuth

	// The first request commits and its response is lost.
	first, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		return fx.appendItem("mcp:replay-1")
	})
	if err != nil || first.ID == "" {
		t.Fatalf("the first request: item %q, err %v", first.ID, err)
	}

	// The client is deleted between that commit and the retry.
	if rec := doReq(t, fx.s, http.MethodDelete, "/api/clients/c1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete the client: %d %s", rec.Code, rec.Body)
	}

	// The retry is refused before a replay could hand back the first result.
	called := false
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		called = true
		return fx.appendItem("mcp:replay-1")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a replayed request from a deleted client: err = %v, want denial", err)
	}
	if called {
		t.Fatal("the replay ran for a deleted client")
	}
	items := fx.store.List()
	if len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("the journal holds %d items after the replay, want the one original", len(items))
	}
	// Nothing of the client survives: the pair is unknown to management and
	// the workspace holds no such client.
	if status := fx.readGrantRec("c1", fx.projectID); status != http.StatusNotFound {
		t.Fatalf("grant GET for a deleted client = %d, want 404", status)
	}
	if _, ok := fx.s.fleet.ws.Client("c1"); ok {
		t.Fatal("the deleted client is still stored")
	}
}

func TestMCPFactoryGrantManagementConflict(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	fx.addClient("c1", MCPClient{Name: "claude"})

	// A valid pair with nothing stored reports the zero grant and the stable
	// version of it, so the editor's first write can be conditional.
	zero := fx.readGrant("c1")
	if zero.ClientID != "c1" || zero.ProjectID != fx.projectID {
		t.Fatalf("zero state = %+v", zero)
	}
	if zero.Grant != (MCPFactoryProjectGrant{ProjectID: fx.projectID}) {
		t.Fatalf("zero grant = %+v", zero.Grant)
	}
	if want := factoryGrantVersion(MCPFactoryProjectGrant{ProjectID: fx.projectID}); zero.Version != want {
		t.Fatalf("zero version = %q, want %q", zero.Version, want)
	}
	if again := fx.readGrant("c1"); again.Version != zero.Version {
		t.Fatalf("the zero version moved: %q then %q", zero.Version, again.Version)
	}
	// Saving the reported zero grant unchanged is a no-op rather than a
	// version bump: an empty authority and the explicit "none" the write path
	// stores are one refusal, so they encode to one version.
	unchanged := doReq(t, fx.s, http.MethodPut, fx.grantPath("c1"), map[string]any{
		"grant":   fx.scoped(MCPFactoryProjectGrant{ApprovalAuthority: factoryAuthorityNone}),
		"version": zero.Version,
	}, nil)
	if unchanged.Code != http.StatusOK {
		t.Fatalf("saving the reported zero grant = %d %s", unchanged.Code, unchanged.Body)
	}
	var kept FactoryGrantState
	decodeBody(t, unchanged, &kept)
	if kept.Version != zero.Version {
		t.Fatalf("saving the zero grant unchanged moved its version to %q", kept.Version)
	}
	// The response never carries the client's token hash.
	stored, ok := fx.s.fleet.ws.Client("c1")
	if !ok || stored.TokenHash == "" {
		t.Fatal("the fixture client has no token hash")
	}
	if body := doReq(t, fx.s, http.MethodGet, fx.grantPath("c1"), nil, nil).Body.String(); strings.Contains(body, stored.TokenHash) {
		t.Fatalf("the grant response carries the token hash: %s", body)
	}

	// A write naming the version it read lands and moves the version.
	first := fx.setGrant("c1", MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone})
	if !first.Grant.Read || first.Version == zero.Version {
		t.Fatalf("the first write = %+v", first)
	}

	// A second editor writing from the version it read before is refused, and
	// the refusal names the value that won.
	stale := doReq(t, fx.s, http.MethodPut, fx.grantPath("c1"), map[string]any{
		"grant":   fx.scoped(MCPFactoryProjectGrant{CreateDraft: true, ApprovalAuthority: factoryAuthorityNone}),
		"version": zero.Version,
	}, nil)
	if stale.Code != http.StatusConflict {
		t.Fatalf("a stale write = %d, want 409", stale.Code)
	}
	var current FactoryGrantState
	decodeBody(t, stale, &current)
	if current.Version != first.Version || current.Grant.CreateDraft || !current.Grant.Read {
		t.Fatalf("the conflict body = %+v, want the stored state", current)
	}
	if got := fx.readGrant("c1"); got.Version != first.Version || got.Grant.CreateDraft {
		t.Fatalf("a refused write changed the stored grant: %+v", got)
	}

	// Validation refusals change nothing either.
	second := fx.setGrant("c1", MCPFactoryProjectGrant{Read: true, CreateDraft: true, ApprovalAuthority: factoryAuthorityNone})
	bad := map[string]map[string]any{
		"mismatched project": {
			"grant":   MCPFactoryProjectGrant{ProjectID: strings.Repeat("a", 32), Read: true, ApprovalAuthority: factoryAuthorityNone},
			"version": second.Version,
		},
		"unknown authority": {
			"grant":   fx.scoped(MCPFactoryProjectGrant{Read: true, ApproveAndQueue: true, ApprovalAuthority: "owner"}),
			"version": second.Version,
		},
		"approval without authority": {
			"grant":   fx.scoped(MCPFactoryProjectGrant{ApproveAndQueue: true, ApprovalAuthority: factoryAuthorityNone}),
			"version": second.Version,
		},
		"no version": {
			"grant": fx.scoped(MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone}),
		},
	}
	for name, body := range bad {
		if rec := doReq(t, fx.s, http.MethodPut, fx.grantPath("c1"), body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if got := fx.readGrant("c1"); got.Version != second.Version {
		t.Fatalf("a refused write moved the grant: %+v", got)
	}

	// A persistence failure is reported without a path, and the in-memory
	// grant is restored rather than left half-written.
	breakWorkspacePersistence(t, fx.s.fleet.ws)
	failed := doReq(t, fx.s, http.MethodPut, fx.grantPath("c1"), map[string]any{
		"grant":   fx.scoped(MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone}),
		"version": second.Version,
	}, nil)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("a write that cannot persist = %d %s, want 500", failed.Code, failed.Body)
	}
	if strings.Contains(failed.Body.String(), fx.s.fleet.ws.path) {
		t.Fatalf("the failure names the workspace path: %s", failed.Body)
	}
	if got := fx.readGrant("c1"); !got.Grant.CreateDraft || got.Version != second.Version {
		t.Fatalf("a failed save changed the grant: %+v", got)
	}
}

func TestMCPFactoryGrantExistingFleetAutonomyDoesNotGrantAccess(t *testing.T) {
	fx := newFactoryAuthFixture(t)
	// An autonomous client with an empty allowlist is the widest existing
	// fleet policy there is: it may spawn on any registered repo without
	// confirmation.
	fx.addClient("c1", MCPClient{Name: "claude", Autonomous: true, MaxConcurrent: 4, MaxPerDay: 40})
	a := fx.s.factoryAuth

	if err := a.AuthorizeRead("c1", fx.projectID, "app"); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("autonomous read without a grant: err = %v, want denial", err)
	}
	called := false
	if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		called = true
		return fx.appendItem("autonomous")
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("autonomous createDraft without a grant: err = %v, want denial", err)
	}
	if called {
		t.Fatal("autonomy ran a factory mutation")
	}
	if zero := fx.readGrant("c1"); zero.Grant != (MCPFactoryProjectGrant{ProjectID: fx.projectID}) {
		t.Fatalf("autonomy appeared as a stored grant: %+v", zero.Grant)
	}

	// A grant adds exactly what it names, and the autonomy behind it adds
	// nothing: no draft, no approval.
	fx.setGrant("c1", MCPFactoryProjectGrant{Read: true, ApprovalAuthority: factoryAuthorityNone})
	if err := a.AuthorizeRead("c1", fx.projectID, "app"); err != nil {
		t.Fatalf("a granted read: %v", err)
	}
	for _, capability := range []string{factoryCapabilityCreateDraft, factoryCapabilityApproveAndQueue} {
		if _, err := a.WithAuthorizedMutation("c1", fx.projectID, "app", capability, func() (FactoryItem, error) {
			return fx.appendItem("autonomy-" + capability)
		}); !errors.Is(err, errFactoryDenied) {
			t.Fatalf("%s beside autonomy: err = %v, want denial", capability, err)
		}
	}
}
