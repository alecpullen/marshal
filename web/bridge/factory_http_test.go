package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// factoryMCPFixture is one factory service with an MCP client holding a grant
// on the fixture's project: the shape the MCP tools drive, minus the protocol.
// The journal is on disk under dir, so "one append" is countable.
type factoryMCPFixture struct {
	*factoryDefaultsFixture
	projectID string
	dir       string
}

func newFactoryMCPFixture(t *testing.T, grant MCPFactoryProjectGrant) *factoryMCPFixture {
	t.Helper()
	f := newFactoryDefaultsFixture(t)
	projectID, ok := f.fleet.ws.ProjectID(f.root)
	if !ok {
		t.Fatal("a registered project has no factory id")
	}
	fx := &factoryMCPFixture{factoryDefaultsFixture: f, projectID: projectID, dir: filepath.Dir(f.store.file.Name())}
	fx.addClient(t, "c1", grant)
	return fx
}

// addClient registers one more MCP consumer holding grant on this project.
func (fx *factoryMCPFixture) addClient(t *testing.T, id string, grant MCPFactoryProjectGrant) {
	t.Helper()
	grant.ProjectID = fx.projectID
	if err := fx.fleet.ws.PutClient(MCPClient{ID: id, OwnerID: DefaultOwnerID, FactoryGrants: []MCPFactoryProjectGrant{grant}}); err != nil {
		t.Fatal(err)
	}
}

// principal is the server-authenticated identity of a client on this project.
func (fx *factoryMCPFixture) principal(clientID, action string) factoryPrincipal {
	return factoryPrincipal{ClientID: clientID, ProjectID: fx.projectID, Action: action}
}

// handoff is a valid proposal: native, one target branch, no required checks,
// and the routing profile the fixture's canned model configuration serves.
func (fx *factoryMCPFixture) handoff(requestID string) FactoryCreate {
	routing := factoryDefaultsRouting()
	return FactoryCreate{
		RequestID: requestID,
		Title:     "Add the thing",
		Plan:      "implement the thing",
		Spec:      "the specification",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              fx.root,
			TargetBranch:             "main",
			Native:                   true,
			ImplementerRouting:       routing,
			ReviewerRouting:          routing,
			NoRequiredCIAcknowledged: true,
		},
	}
}

// createDraft runs the approved MCP order: resolve, normalize and preflight
// outside the policy lock, then authorize and commit inside it.
func (fx *factoryMCPFixture) createDraft(t *testing.T, clientID string, req FactoryCreate) FactoryItem {
	t.Helper()
	resolved, err := fx.api.PrepareDraft(req)
	if err != nil {
		t.Fatalf("prepare draft: %v", err)
	}
	item, err := fx.srv.factoryAuth.WithAuthorizedMutation(clientID, fx.projectID, req.Config.RepoID, factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		return fx.api.CreateDraftAuthorized(req, resolved, fx.principal(clientID, factoryCapabilityCreateDraft))
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	return item
}

// journalLines is what the journal holds: one record per accepted mutation,
// which is exactly what "the same append" means.
func (fx *factoryMCPFixture) journalLines(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fx.dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// mustJSON renders an item for byte comparisons.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFactoryMCPCreateProvenanceAtomic(t *testing.T) {
	fx := newFactoryMCPFixture(t, fullGrant())
	req := fx.handoff("create-1")
	resolved, err := fx.api.PrepareDraft(req)
	if err != nil {
		t.Fatal(err)
	}
	item, err := fx.srv.factoryAuth.WithAuthorizedMutation("c1", fx.projectID, "repo-1", factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		return fx.api.CreateDraftAuthorized(req, resolved, fx.principal("c1", factoryCapabilityCreateDraft))
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.State != "draft" || item.Version != 1 {
		t.Fatalf("created %+v", item)
	}
	if len(item.History) != 1 {
		t.Fatalf("create journalled %d entries, want exactly its own attribution", len(item.History))
	}
	event := item.History[0]
	if event.Kind != "create" || event.Provenance == nil {
		t.Fatalf("create entry %+v", event)
	}
	want := FactoryProvenance{ClientID: "c1", ProjectID: fx.projectID, Action: factoryCapabilityCreateDraft, RequestID: "create-1"}
	if *event.Provenance != want {
		t.Fatalf("provenance = %+v, want %+v", *event.Provenance, want)
	}
	// The journal's request identity is namespaced; the client's own ID exists
	// only in provenance.
	if item.Draft.RequestID != factoryScopedRequestID("c1", "create-1") {
		t.Fatalf("draft request identity %q is not namespaced", item.Draft.RequestID)
	}
	if lines := fx.journalLines(t); lines != 1 {
		t.Fatalf("create wrote %d journal records, want 1", lines)
	}
	// One append is the whole provenance: a restart replays it byte for byte,
	// with no second audit record to reconcile.
	if err := fx.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(fx.dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, got), mustJSON(t, item)) {
		t.Fatal("replayed item lost its create provenance")
	}
	if got.History[0].Provenance == nil || got.History[0].Provenance.ClientID != "c1" {
		t.Fatalf("replayed provenance %+v", got.History[0].Provenance)
	}
}

func TestFactoryMCPScopedRequestReplay(t *testing.T) {
	fx := newFactoryMCPFixture(t, fullGrant())
	fx.addClient(t, "c2", fullGrant())
	first := fx.createDraft(t, "c1", fx.handoff("same"))
	second := fx.createDraft(t, "c2", fx.handoff("same"))
	if first.ID == second.ID {
		t.Fatal("two clients naming one request id share a work item")
	}
	if first.Draft.RequestID == second.Draft.RequestID {
		t.Fatal("the namespaced request identities collided")
	}
	if lines := fx.journalLines(t); lines != 2 {
		t.Fatalf("two creates wrote %d journal records", lines)
	}
	// A lost response retried after the commit returns what the journal holds.
	replayed := fx.createDraft(t, "c1", fx.handoff("same"))
	if replayed.ID != first.ID || replayed.Version != first.Version {
		t.Fatalf("replay returned %s v%d, want %s v%d", replayed.ID, replayed.Version, first.ID, first.Version)
	}
	if lines := fx.journalLines(t); lines != 2 {
		t.Fatalf("the retry wrote a second record: %d", lines)
	}
	for _, c := range []struct {
		clientID string
		item     FactoryItem
	}{{"c1", first}, {"c2", second}} {
		if len(c.item.History) != 1 || c.item.History[0].Provenance == nil {
			t.Fatalf("%s create entries %+v", c.clientID, c.item.History)
		}
		p := c.item.History[0].Provenance
		if p.ClientID != c.clientID || p.RequestID != "same" {
			t.Fatalf("%s attributed as %+v", c.clientID, p)
		}
	}
	// Commands are namespaced the same way: one request id from two clients is
	// two approvals, and each replays its own.
	approved := map[string]FactoryItem{}
	for _, c := range []struct {
		clientID string
		item     FactoryItem
	}{{"c1", first}, {"c2", second}} {
		item, err := fx.srv.factoryAuth.WithAuthorizedMutation(c.clientID, fx.projectID, "repo-1", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
			return fx.api.CommandAuthorized(context.Background(), c.item.ID, FactoryCommand{RequestID: "approve-same", Kind: "approve", ExpectedVersion: c.item.Version}, fx.principal(c.clientID, factoryCapabilityApproveAndQueue))
		})
		if err != nil {
			t.Fatalf("%s approve: %v", c.clientID, err)
		}
		if item.State != "queued" {
			t.Fatalf("%s state %q after approve", c.clientID, item.State)
		}
		approved[c.clientID] = item
	}
	if lines := fx.journalLines(t); lines != 4 {
		t.Fatalf("%d journal records after two approvals", lines)
	}
	replay, err := fx.srv.factoryAuth.WithAuthorizedMutation("c1", fx.projectID, "repo-1", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
		return fx.api.CommandAuthorized(context.Background(), first.ID, FactoryCommand{RequestID: "approve-same", Kind: "approve", ExpectedVersion: first.Version}, fx.principal("c1", factoryCapabilityApproveAndQueue))
	})
	if err != nil {
		t.Fatalf("approve replay: %v", err)
	}
	if replay.Version != approved["c1"].Version {
		t.Fatalf("approve replay returned v%d, want v%d", replay.Version, approved["c1"].Version)
	}
	if lines := fx.journalLines(t); lines != 4 {
		t.Fatalf("the approve retry wrote a second record: %d", lines)
	}
}

func TestFactoryMCPDraftReplaceVersionAndIdentity(t *testing.T) {
	fx := newFactoryMCPFixture(t, fullGrant())
	draft := fx.createDraft(t, "c1", fx.handoff("create-1"))
	candidate := fx.handoff("edit-1")
	candidate.Title = "A better title"
	candidate.Plan = "a better plan"
	candidate.Spec = "a better spec"
	candidate.Config.MaxRounds = 7
	prepared, err := fx.api.PrepareDraft(candidate)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(c FactoryCreate, expectedVersion uint64) (FactoryItem, error) {
		return fx.srv.factoryAuth.WithAuthorizedMutation("c1", fx.projectID, "repo-1", factoryCapabilityEditDraft, func() (FactoryItem, error) {
			return fx.api.ReplaceDraftAuthorized(draft.ID, c, expectedVersion, fx.principal("c1", factoryCapabilityEditDraft))
		})
	}
	// A version the work item never had is refused: the replacement is
	// conditional on what the client reviewed.
	stale := prepared
	stale.RequestID = "edit-stale"
	if _, err := replace(stale, draft.Version+1); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a future version replaced the draft: %v", err)
	}
	// Another repository or another project is another work item, not an edit.
	for _, move := range []struct {
		name  string
		apply func(*FactoryCreate)
	}{
		{"repository", func(c *FactoryCreate) { c.Config.RepoID = "other" }},
		{"project", func(c *FactoryCreate) { c.Config.ProjectRoot = t.TempDir() }},
	} {
		moved := prepared
		move.apply(&moved)
		if _, err := replace(moved, draft.Version); !errors.Is(err, ErrFactoryConflict) {
			t.Fatalf("a replacement moved the %s: %v", move.name, err)
		}
		still, err := fx.store.Get(draft.ID)
		if err != nil || !bytes.Equal(mustJSON(t, still), mustJSON(t, draft)) {
			t.Fatalf("a refused replacement touched the draft (%s): %v", move.name, err)
		}
	}
	lines := fx.journalLines(t)
	replaced, err := replace(prepared, draft.Version)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Version != draft.Version+1 {
		t.Fatalf("version %d after replace, want %d", replaced.Version, draft.Version+1)
	}
	// Only the content moves; the identity is the same work item.
	if replaced.ID != draft.ID || replaced.RepoID != draft.RepoID || replaced.ProjectRoot != draft.ProjectRoot ||
		replaced.HostID != draft.HostID || replaced.Branch != draft.Branch || !replaced.CreatedAt.Equal(draft.CreatedAt) {
		t.Fatalf("replace moved the identity: %+v", replaced)
	}
	if replaced.Draft.RequestID != draft.Draft.RequestID {
		t.Fatalf("replace rewrote the draft's create identity to %q", replaced.Draft.RequestID)
	}
	if replaced.Title != "A better title" || replaced.Draft.Title != "A better title" ||
		replaced.Draft.Plan != "a better plan" || replaced.Draft.Spec != "a better spec" ||
		replaced.Draft.Config.MaxRounds != 7 || replaced.Draft.Config.CIPollSeconds != 15 || replaced.Draft.Config.RepoID != "repo-1" {
		t.Fatalf("replace stored %+v", replaced.Draft)
	}
	if len(replaced.Approvals) != 0 || len(replaced.Rounds) != 0 || len(replaced.Effects) != 0 || len(replaced.Grants) != 0 {
		t.Fatalf("replace invented history: %+v", replaced)
	}
	if len(replaced.History) != 2 || replaced.History[0].Kind != "create" {
		t.Fatalf("replace lost the earlier entries: %+v", replaced.History)
	}
	attribution := replaced.History[1]
	if attribution.Kind != "replace" || attribution.Provenance == nil {
		t.Fatalf("replacement entry %+v", attribution)
	}
	if p := attribution.Provenance; p.ClientID != "c1" || p.Action != factoryCapabilityEditDraft || p.RequestID != "edit-1" || p.ProjectID != fx.projectID {
		t.Fatalf("replacement provenance %+v", attribution.Provenance)
	}
	if got := fx.journalLines(t); got != lines+1 {
		t.Fatalf("the replacement wrote %d records, want 1", got-lines)
	}
	// A lost response retried after the commit returns the journaled item.
	again, err := replace(prepared, draft.Version)
	if err != nil {
		t.Fatalf("replacement replay: %v", err)
	}
	if again.Version != replaced.Version {
		t.Fatalf("replay returned v%d, want v%d", again.Version, replaced.Version)
	}
	if got := fx.journalLines(t); got != lines+1 {
		t.Fatalf("the replacement retry wrote another record: %d", got-lines)
	}
	// The version the replacement spent is spent.
	next := fx.handoff("edit-2")
	if _, err := replace(next, draft.Version); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a stale version replaced the draft again: %v", err)
	}
}

func TestFactoryMCPApprovedDraftImmutable(t *testing.T) {
	fx := newFactoryMCPFixture(t, fullGrant())
	draft := fx.createDraft(t, "c1", fx.handoff("create-1"))
	approved, err := fx.srv.factoryAuth.WithAuthorizedMutation("c1", fx.projectID, "repo-1", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
		p := fx.principal("c1", factoryCapabilityApproveAndQueue)
		p.Authority = factoryAuthorityUserDirected
		return fx.api.CommandAuthorized(context.Background(), draft.ID, FactoryCommand{RequestID: "approve-1", Kind: "approve", ExpectedVersion: draft.Version}, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != "queued" || len(approved.Approvals) != 1 {
		t.Fatalf("approval did not queue: %+v", approved)
	}
	candidate := fx.handoff("edit-1")
	candidate.Title = "different"
	candidate.Plan = "a different plan"
	candidate.Config.MaxRounds = 2
	lines := fx.journalLines(t)
	_, err = fx.srv.factoryAuth.WithAuthorizedMutation("c1", fx.projectID, "repo-1", factoryCapabilityEditDraft, func() (FactoryItem, error) {
		return fx.api.ReplaceDraftAuthorized(approved.ID, candidate, approved.Version, fx.principal("c1", factoryCapabilityEditDraft))
	})
	if !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an approved draft was replaced: %v", err)
	}
	after, err := fx.store.Get(approved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, after), mustJSON(t, approved)) {
		t.Fatal("a refused replacement touched the approved work item")
	}
	if got := fx.journalLines(t); got != lines {
		t.Fatalf("a refused replacement wrote %d journal records", got-lines)
	}
	if after.Approvals[0].PlanHash != FactoryPlanHash(draft.Draft.Plan, draft.Draft.Spec) {
		t.Fatalf("approval hash %q", after.Approvals[0].PlanHash)
	}
}

func TestFactoryHTTP(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fleet := NewFleet(NewWorkspace(t.TempDir()), "", nil, t.TempDir(), Limits{}, "", nil, "")
	defer fleet.Close()
	srv := NewServer(fleet)
	srv.factoryRoutes(NewFactoryAPI(store, nil, fleet))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := testRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if got := request("POST", "/api/factory/items", `{"title":"x"}`, "secret"); got.Code != http.StatusBadRequest {
		t.Fatalf("identity %d: %s", got.Code, got.Body.String())
	}
	if got := request("POST", "/api/factory/items", `{"requestId":"x","title":"x","plan":"p","config":{"repoId":"missing"}}`, "secret"); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repo %d: %s", got.Code, got.Body.String())
	}
	if got := request("POST", "/api/factory/items", strings.Repeat("x", factoryRequestLimit+1), "secret"); got.Code != http.StatusBadRequest {
		t.Fatalf("body limit %d", got.Code)
	}
	var list struct {
		Items []FactoryItem `json:"items"`
	}
	got := request("GET", "/api/factory/items", "", "secret")
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &list) != nil || len(list.Items) != 0 {
		t.Fatalf("list %d: %s", got.Code, got.Body.String())
	}
}

func TestFactoryHTTPCreateApprovalAndReplay(t *testing.T) {
	agent := newFakeAgent()
	fleet := ctlFleet(t, agent)
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := fleet.ws.PutRepo(Repo{ID: "repo", URL: "https://example.invalid/repo.git", Branch: "main", OwnerID: DefaultOwnerID, Forge: "github", CredRef: "test-pat"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_PAT", "fixture-only")
	fleet.creds = NewCredentialStore([]Credential{{ID: "test-pat", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "FACTORY_TEST_PAT"}})
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"test": map[string]any{"type": "openai_compatible", "hasKey": true}}, "presets": map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}, "defaultProfile": "chosen"}
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	controller := NewFactoryController(store, nil, nil)
	srv := NewServer(fleet, "", NewFactoryAPI(store, controller, fleet))
	req := FactoryCreate{RequestID: "create-1", Title: "Task", Plan: "build", Config: FactoryConfig{RepoID: "repo", ProjectRoot: root, TargetBranch: "main", Native: true, ImplementerRouting: json.RawMessage(`{"profile":"chosen"}`), ReviewerRouting: json.RawMessage(`{"profile":"chosen"}`)}}
	post := func(v any, path string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(v)
		r := testJSONRequest("POST", path, string(raw))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	created := post(req, "/api/factory/items")
	if created.Code != 201 {
		t.Fatalf("create %d: %s", created.Code, created.Body.String())
	}
	var result struct {
		Item FactoryItem `json:"item"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Item.State != "draft" || result.Item.Draft.Config.ImplementerAuto {
		t.Fatalf("preview started or auto selected: %+v", result.Item)
	}
	if replay := post(req, "/api/factory/items"); replay.Code != 200 {
		t.Fatalf("replay %d: %s", replay.Code, replay.Body.String())
	}
	changed := req
	changed.Title = "different"
	if conflict := post(changed, "/api/factory/items"); conflict.Code != 409 {
		t.Fatalf("request identity conflict %d: %s", conflict.Code, conflict.Body.String())
	}
	stale := post(FactoryCommand{RequestID: "stale", Kind: "stop", ExpectedVersion: 2}, "/api/factory/items/"+result.Item.ID+"/commands")
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), `"currentVersion":1`) {
		t.Fatalf("version conflict %d: %s", stale.Code, stale.Body.String())
	}
	if bad := post(FactoryCommand{RequestID: "approve-1", Kind: "approve", ExpectedVersion: 1}, "/api/factory/items/"+result.Item.ID+"/commands"); bad.Code != 422 {
		t.Fatalf("CI acknowledgement %d: %s", bad.Code, bad.Body.String())
	}
	if latest, err := store.Get(result.Item.ID); err != nil || latest.Version != 1 {
		t.Fatalf("invalid approval changed journal: %v %+v", err, latest)
	}
	approved := req
	approved.RequestID = "create-2"
	approved.Config.NoRequiredCIAcknowledged = true
	approved.Config.ImplementerAuto = true
	second := post(approved, "/api/factory/items")
	if second.Code != 201 || json.Unmarshal(second.Body.Bytes(), &result) != nil {
		t.Fatalf("second create %d: %s", second.Code, second.Body.String())
	}
	command := FactoryCommand{RequestID: "approve-2", Kind: "approve", ExpectedVersion: 1}
	commandBody, _ := json.Marshal(command)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	acceptedRequest := testJSONRequest("POST", "/api/factory/items/"+result.Item.ID+"/commands", string(commandBody)).WithContext(requestCtx)
	acceptedRequest.Header.Set("Authorization", "Bearer secret")
	accepted := httptest.NewRecorder()
	srv.ServeHTTP(accepted, acceptedRequest)
	cancelRequest()
	if accepted.Code != 200 || json.Unmarshal(accepted.Body.Bytes(), &result) != nil || result.Item.State != "queued" || !result.Item.Approvals[0].Config.ImplementerAuto {
		t.Fatalf("approve %d: %s", accepted.Code, accepted.Body.String())
	}
	if replay := post(command, "/api/factory/items/"+result.Item.ID+"/commands"); replay.Code != 200 {
		t.Fatalf("command replay %d: %s", replay.Code, replay.Body.String())
	}
	invalidModel := req
	invalidModel.RequestID = "bad-model"
	invalidModel.Config.ImplementerRouting = json.RawMessage(`{"profile":"missing"}`)
	if bad := post(invalidModel, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("model %d: %s", bad.Code, bad.Body.String())
	}
	invalidEnvironment := req
	invalidEnvironment.RequestID = "bad-environment"
	invalidEnvironment.Config.Native = false
	invalidEnvironment.Config.Workspace = "unbuilt"
	if bad := post(invalidEnvironment, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("environment %d: %s", bad.Code, bad.Body.String())
	}
	if _, err := fleet.templates.Create("pending", []byte("fixture"), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := fleet.templates.Publish("pending", "test"); err != nil {
		t.Fatal(err)
	}
	agent.results["workspace/parse"] = map[string]any{"doc": map[string]any{"workspace": map[string]any{"name": "pending"}}}
	invalidEnvironment.RequestID = "pending-image"
	invalidEnvironment.Config.Workspace = "pending"
	if bad := post(invalidEnvironment, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("pending build %d: %s", bad.Code, bad.Body.String())
	}
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"test": map[string]any{"type": "anthropic", "hasKey": false}}, "presets": map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}}
	missingKey := req
	missingKey.RequestID = "missing-key"
	if bad := post(missingKey, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("missing provider key %d: %s", bad.Code, bad.Body.String())
	}
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"oauth": map[string]any{"type": "openai_codex", "auth": "oauth", "apiKeyEnv": "FACTORY_STALE_OPTIONAL_KEY", "hasKey": false}}, "presets": map[string]any{"test/model": map[string]any{"provider": "oauth", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}}
	oauth := req
	oauth.RequestID = "oauth-with-stale-env"
	if accepted := post(oauth, "/api/factory/items"); accepted.Code != 201 {
		t.Fatalf("OAuth route with optional stale key %d: %s", accepted.Code, accepted.Body.String())
	}
	if strings.Contains(created.Body.String(), "fixture-only") || strings.Contains(accepted.Body.String(), "fixture-only") {
		t.Fatal("credential value leaked in response")
	}
	// The accepted approval belongs to the process controller. Closing the
	// request above must not cancel its later dispatch.
	effects := &httpContinuationEffects{started: make(chan context.Context, 1), release: make(chan struct{})}
	processController := NewFactoryController(store, effects, nil)
	if err := processController.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	processCtx, cancelProcess := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- processController.Run(processCtx) }()
	select {
	case effectCtx := <-effects.started:
		select {
		case <-effectCtx.Done():
			t.Fatal("request cancellation stopped accepted work")
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("accepted work was not dispatched")
	}
	close(effects.release)
	if err := processController.Quiesce(); err != nil {
		t.Fatal(err)
	}
	cancelProcess()
	<-done
}

type httpContinuationEffects struct {
	started chan context.Context
	release chan struct{}
}

func (f *httpContinuationEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return FactoryObservation{BaseSHA: "base", RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}, nil
}
func (f *httpContinuationEffects) Execute(ctx context.Context, _ FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	select {
	case f.started <- ctx:
	default:
	}
	<-f.release
	return FactoryOutcome{EffectID: e.ID, Confirmed: true}, nil
}

func TestFactoryMCPApproveProvenanceAndQueue(t *testing.T) {
	standing := fullGrant()
	standing.ApprovalAuthority = factoryAuthorityStanding
	fx := newFactoryMCPFixture(t, standing)
	draft := fx.createDraft(t, "c1", fx.handoff("create-1"))
	approve := func(requestID string, expectedVersion uint64) (FactoryItem, error) {
		return fx.srv.factoryAuth.WithAuthorizedMutationAuthority("c1", fx.projectID, "repo-1", factoryCapabilityApproveAndQueue, func(authority string) (FactoryItem, error) {
			p := fx.principal("c1", factoryCapabilityApproveAndQueue)
			// The authority is the one the stored grant carries under the
			// policy lock, not one a caller supplied.
			p.Authority = authority
			return fx.api.CommandAuthorized(context.Background(), draft.ID, FactoryCommand{RequestID: requestID, Kind: "approve", ExpectedVersion: expectedVersion}, p)
		})
	}
	queued, err := approve("approve-1", draft.Version)
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != "queued" || queued.ApprovedAt.IsZero() {
		t.Fatalf("approval did not queue: %+v", queued)
	}
	if len(queued.Approvals) != 1 {
		t.Fatalf("approvals %+v", queued.Approvals)
	}
	approval := queued.Approvals[0]
	if approval.Revision != 1 || approval.Plan != draft.Draft.Plan || approval.Spec != draft.Draft.Spec ||
		approval.PlanHash != FactoryPlanHash(draft.Draft.Plan, draft.Draft.Spec) {
		t.Fatalf("the approval is not the stored draft: %+v", approval)
	}
	sameFactoryConfig(t, approval.Config, draft.Draft.Config)
	if len(queued.Grants) != 1 || queued.Grants[0].ID != factoryScopedRequestID("c1", "approve-1") {
		t.Fatalf("grants %+v", queued.Grants)
	}
	// One attribution entry sits beside the approval, in the same append, and
	// names the authority that admitted it.
	if len(queued.History) != 2 || queued.History[1].Kind != "approve" {
		t.Fatalf("approval entries %+v", queued.History)
	}
	p := queued.History[1].Provenance
	if p == nil || p.ClientID != "c1" || p.ProjectID != fx.projectID ||
		p.Action != factoryCapabilityApproveAndQueue || p.RequestID != "approve-1" || p.Authority != factoryAuthorityStanding {
		t.Fatalf("approval provenance %+v", p)
	}
	if lines := fx.journalLines(t); lines != 2 {
		t.Fatalf("%d journal records after create and approve, want 2", lines)
	}
	// A lost response retried after the commit returns the same approval
	// instead of queueing a second one.
	replay, err := approve("approve-1", draft.Version)
	if err != nil {
		t.Fatalf("approval replay: %v", err)
	}
	if replay.Version != queued.Version || len(replay.Approvals) != 1 {
		t.Fatalf("replay returned v%d with %d approvals", replay.Version, len(replay.Approvals))
	}
	if lines := fx.journalLines(t); lines != 2 {
		t.Fatalf("the approval retry wrote a second record: %d", lines)
	}
}

func TestFactoryMCPAnswerDecisionRace(t *testing.T) {
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	item, err := store.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	item, err = store.Update(item.ID, "question", item.Version, json.RawMessage(`{"question":true}`), func(i *FactoryItem) error {
		i.State = "needs_input"
		i.Grants = []FactoryGrant{{ID: "initial", AdditionalRounds: 5}}
		i.Effects = []FactoryEffect{{ID: "impl", Kind: "implement", Status: "completed", Outcome: &FactoryOutcome{
			EffectID: "impl", Confirmed: true, Report: &FactoryReport{Outcome: "needs_input", Question: "which file?"},
		}}}
		i.Decision = &FactoryDecision{ID: "q", Kind: "question", EffectID: "impl", Question: "which file?"}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	controller := NewFactoryController(store, nil, nil)
	answers := []struct {
		clientID string
		action   string
		answer   string
	}{
		{"c1", factoryCapabilityAnswerQuestion, "use A"},
		{"c2", factoryCapabilityDelegatedAnswer, "use B"},
	}
	// Both clients answer the same decision against the same version at the
	// same moment: exactly one answer may be journaled.
	start := make(chan struct{})
	results := make(chan error, len(answers))
	var wg sync.WaitGroup
	for _, a := range answers {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p := factoryPrincipal{ClientID: a.clientID, ProjectID: "project-1", Action: a.action}
			_, err := controller.CommandAuthorized(context.Background(), item.ID,
				FactoryCommand{RequestID: "answer-" + a.clientID, Kind: "answer", DecisionID: "q", Answer: a.answer, ExpectedVersion: item.Version}, p)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrFactoryConflict):
			conflicted++
		default:
			t.Fatalf("answer race: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("%d answers succeeded and %d conflicted, want one each", succeeded, conflicted)
	}
	after, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var answered []FactoryEvent
	for _, e := range after.History {
		if e.Kind == "answer" {
			answered = append(answered, e)
		}
	}
	if len(answered) != 1 {
		t.Fatalf("%d answer entries journaled: %+v", len(answered), after.History)
	}
	winner := answered[0]
	if winner.Provenance == nil || winner.Provenance.DecisionID != "q" {
		t.Fatalf("the answer carries %+v", winner.Provenance)
	}
	// The entry belongs to whoever actually applied it, not to both.
	if (winner.Message == "use A") != (winner.Provenance.ClientID == "c1") {
		t.Fatalf("answer %q attributed to %+v", winner.Message, winner.Provenance)
	}
	if after.Decision != nil || after.State != "repairing" {
		t.Fatalf("the decision was not consumed once: %+v state %q", after.Decision, after.State)
	}
}

func TestFactoryMCPJournalFailureNoSuccess(t *testing.T) {
	now := time.Now().UTC()
	failing := func([]byte) (int, error) { return 0, os.ErrPermission }
	for _, op := range []string{"create", "replace"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenFactoryStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			journal := func() int {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				return strings.Count(string(raw), "\n")
			}
			owner := factoryPrincipal{ClientID: "c1", ProjectID: "project-1", Action: factoryCapabilityCreateDraft}
			editor := owner
			editor.Action = factoryCapabilityEditDraft
			req := factoryReq()
			req.RequestID = factoryScopedRequestID(owner.ClientID, "create")
			draft, err := s.CreateAuthorized(req, req, owner, now)
			if err != nil {
				t.Fatal(err)
			}
			before := journal()
			s.write = failing
			if op == "create" {
				refused := factoryReq()
				refused.RequestID = factoryScopedRequestID(owner.ClientID, "create-refused")
				if _, err := s.CreateAuthorized(refused, refused, owner, now); !errors.Is(err, ErrFactoryUnavailable) {
					t.Fatalf("failed create: %v", err)
				}
				if len(s.List()) != 1 {
					t.Fatalf("a failed create published an item: %+v", s.List())
				}
			} else {
				candidate := draft.Draft
				candidate.RequestID = "edit-1"
				candidate.Plan = "a plan that must not be published"
				if _, err := s.ReplaceDraft(draft.ID, factoryScopedRequestID(editor.ClientID, "edit-1"), draft.Version, candidate, editor, now); !errors.Is(err, ErrFactoryUnavailable) {
					t.Fatalf("failed replacement: %v", err)
				}
				after, err := s.Get(draft.ID)
				if err != nil || !bytes.Equal(mustJSON(t, after), mustJSON(t, draft)) {
					t.Fatal("a failed replacement was published")
				}
			}
			if got := journal(); got != before {
				t.Fatalf("a failed %s wrote %d journal records", op, got-before)
			}
			// The store stays unavailable, so a retry cannot slip a success
			// entry past the failure either.
			again := factoryReq()
			again.RequestID = factoryScopedRequestID(owner.ClientID, "after-failure")
			if _, err := s.CreateAuthorized(again, again, owner, now); !errors.Is(err, ErrFactoryUnavailable) {
				t.Fatalf("retry after a failed append: %v", err)
			}
			if got := journal(); got != before {
				t.Fatalf("the retry wrote a success entry: %d", got-before)
			}
		})
	}
}

func TestFactoryMCPLegacyJournalReplay(t *testing.T) {
	// A record written before provenance existed: an approval, an event, and
	// no attribution anywhere.
	dir := t.TempDir()
	now := time.Now().UTC()
	req := factoryReq()
	config := req.Config
	config.TargetBranch = "main"
	item := FactoryItem{
		ID: "legacy-item", Title: req.Title, State: "checking", Version: 1, RepoID: config.RepoID,
		Branch: "marshal/factory/legacy-item", BaseSHA: "base", Draft: req,
		Approvals: []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: config, ApprovedAt: now}},
		History:   []FactoryEvent{{At: now, Kind: "question", Message: "which file?", EffectID: "effect-1"}},
		CreatedAt: now, UpdatedAt: now,
	}
	payload, err := json.Marshal(factoryPayload{Format: 1, Sequence: 1, RequestID: "legacy", ItemID: item.ID, CommandHash: "legacy", Item: item})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(factoryRecord{Payload: payload, Checksum: factoryDigest(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatalf("legacy journal did not open: %v", err)
	}
	defer s.Close()
	got, err := s.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mustJSON(t, got), []byte("provenance")) {
		t.Fatal("replaying a legacy record added a provenance field")
	}
	if got.Approvals[0].PlanHash != FactoryPlanHash(item.Draft.Plan, item.Draft.Spec) || len(got.History) != 1 ||
		got.History[0].Message != "which file?" || got.History[0].EffectID != "effect-1" {
		t.Fatalf("legacy record lost its contents: %+v", got)
	}
	// A Studio mutation still writes the journal bytes it always did: no
	// attribution appears anywhere in the file.
	fresh := t.TempDir()
	studio, err := OpenFactoryStore(fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer studio.Close()
	created, err := studio.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := NewFactoryController(studio, nil, nil).Command(context.Background(), created.ID,
		FactoryCommand{RequestID: "approve", Kind: "approve", ExpectedVersion: created.Version})
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != "queued" || len(approved.History) != 0 {
		t.Fatalf("a Studio approval journalled %+v", approved.History)
	}
	if approved.Approvals[0].PlanHash != FactoryPlanHash(created.Draft.Plan, created.Draft.Spec) {
		t.Fatalf("Studio approval hash %q", approved.Approvals[0].PlanHash)
	}
	raw, err := os.ReadFile(filepath.Join(fresh, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("provenance")) {
		t.Fatal("a Studio mutation journalled provenance")
	}
}

func TestFactoryStartupQuiescePersistsUncertainty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "factory")
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := factoryReq()
	item, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item, err = store.Update(item.ID, "effect", item.Version, json.RawMessage(`{"effect":true}`), func(i *FactoryItem) error {
		i.State = "implementing"
		i.Effects = []FactoryEffect{{ID: "effect-1", Kind: "implement", Status: "dispatched"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controller := NewFactoryController(store, nil, nil)
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if controller.isReady() {
		t.Fatal("dispatch admitted after shutdown")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Effects) != 1 || recovered.Effects[0].Status != "uncertain" || recovered.Decision == nil || recovered.Decision.Kind != "recovery" {
		t.Fatalf("shutdown evidence lost: %+v", recovered)
	}
	if _, err = store.Create(FactoryCreate{RequestID: "effect"}, time.Now()); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("journal request identity lost: %v", err)
	}
}

func TestFactoryStartupQuiesceConcurrentFinish(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "factory")
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	effects := &blockedLoopEffects{started: make(chan struct{}, 2), release: make(chan struct{}), cancelled: make(chan struct{}, 1)}
	controller := NewFactoryController(store, effects, nil)
	for _, id := range []string{"first", "second"} {
		item := blockedFactoryItem(t, store, id)
		if err := controller.dispatch(context.Background(), item, item.Effects[len(item.Effects)-1]); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 2; n++ {
		select {
		case <-effects.started:
		case <-time.After(time.Second):
			t.Fatal("effect did not start")
		}
	}
	close(effects.release)
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	controller.active.Wait()
	store, err = OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range store.List() {
		status := item.Effects[len(item.Effects)-1].Status
		if status == "dispatched" {
			t.Fatalf("unsettled effect after shutdown: %s", item.ID)
		}
	}
}

func TestFactoryStartupQuiesceBeforeExecution(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := blockedFactoryItem(t, store, "boundary")
	effects := &dispatchBoundaryEffects{executed: make(chan struct{}, 1)}
	controller := NewFactoryController(store, effects, nil)
	paused := make(chan struct{})
	release := make(chan struct{})
	controller.beforeExecute = func() { close(paused); <-release }
	done := make(chan error, 1)
	go func() { done <- controller.dispatch(context.Background(), item, item.Effects[len(item.Effects)-1]) }()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not reach admission boundary")
	}
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-effects.executed:
		t.Fatal("effect executed after shutdown marked it uncertain")
	default:
	}
	got, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Effects[len(got.Effects)-1].Status != "uncertain" {
		t.Fatalf("effect status %q", got.Effects[len(got.Effects)-1].Status)
	}
}

func TestFactoryStartupQuiesceAfterAdmissionBeforeAdapter(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := blockedFactoryItem(t, store, "admitted-boundary")
	effects := &dispatchBoundaryEffects{executed: make(chan struct{}, 1)}
	controller := NewFactoryController(store, effects, nil)
	admitted := make(chan context.Context, 1)
	release := make(chan struct{})
	controller.afterAdmission = func(ctx context.Context) { admitted <- ctx; <-release }
	if err := controller.dispatch(context.Background(), item, item.Effects[len(item.Effects)-1]); err != nil {
		t.Fatal(err)
	}
	var executionCtx context.Context
	select {
	case executionCtx = <-admitted:
	case <-time.After(time.Second):
		t.Fatal("effect not admitted")
	}
	quiesced := make(chan error, 1)
	go func() { quiesced <- controller.Quiesce() }()
	select {
	case <-executionCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel admitted effect")
	}
	before, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Effects[len(before.Effects)-1].Status != "dispatched" {
		t.Fatalf("uncertainty persisted before admitted execution joined: %s", before.Effects[len(before.Effects)-1].Status)
	}
	close(release)
	if err := <-quiesced; err != nil {
		t.Fatal(err)
	}
	select {
	case <-effects.executed:
		t.Fatal("adapter ran after cancellation")
	default:
	}
	after, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Effects[len(after.Effects)-1].Status != "uncertain" {
		t.Fatalf("shutdown status %s", after.Effects[len(after.Effects)-1].Status)
	}
}

type dispatchBoundaryEffects struct{ executed chan struct{} }

func (f *dispatchBoundaryEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return FactoryObservation{}, nil
}
func (f *dispatchBoundaryEffects) Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error) {
	f.executed <- struct{}{}
	return FactoryOutcome{}, nil
}

// factoryCompletionHTTP is the harness the Studio completion tests share: a
// server whose factory routes are installed over a temp-dir journal, a
// controller whose forge is a double, and one registered project the work
// items belong to. Nothing reaches the network — the forge double answers
// every read and every merge — and the credential is a fake the journal never
// carries, so a response that quotes one is a leak.
type factoryCompletionHTTP struct {
	fleet      *Fleet
	store      *FactoryStore
	controller *FactoryController
	srv        *Server
	root       string
	projectID  string
	// creates names each create's journal request identity, which is what
	// keeps two work items in one journal two items.
	creates int
}

// newFactoryCompletionHTTP builds the harness over a forge double. base is the
// forge the merge capability is looked up on and factory is the read
// capability, exactly as the controller's own opener consults them.
func newFactoryCompletionHTTP(t *testing.T, base Forge, factory FactoryForge) *factoryCompletionHTTP {
	t.Helper()
	fleet := NewFleet(NewWorkspace(filepath.Join(t.TempDir(), "fleet.json")), "", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(fleet.Close)
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	projectID, ok := fleet.ws.ProjectID(root)
	if !ok {
		t.Fatal("a registered project has no factory id")
	}
	if err := fleet.ws.PutRepo(Repo{
		ID: "repo", URL: "https://example.invalid/repo.git", Branch: "main",
		OwnerID: DefaultOwnerID, Forge: "github", CredRef: "completion-pat",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_COMPLETION_TEST_PAT", "fixture-only")
	fleet.creds = NewCredentialStore([]Credential{{ID: "completion-pat", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "FACTORY_COMPLETION_TEST_PAT"}})
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	controller := batchController(store, base, factory, nil)
	srv := NewServer(fleet)
	srv.factoryRoutes(NewFactoryAPI(store, controller, fleet))
	return &factoryCompletionHTTP{fleet: fleet, store: store, controller: controller, srv: srv, root: root, projectID: projectID}
}

// item is one approved work item in root whose completion links the pull
// requests in the order given: the state a Studio surface reviews before it
// acts on it.
func (f *factoryCompletionHTTP) item(t *testing.T, root string, numbers ...int) FactoryItem {
	t.Helper()
	now := time.Now().UTC()
	f.creates++
	req := factoryReq()
	req.RequestID = fmt.Sprintf("create-%d", f.creates)
	req.Config.TargetBranch = "main"
	req.Config.ProjectRoot = root
	created, err := f.store.Create(req, now)
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	saved, err := f.store.Update(created.ID, "approve-"+created.ID, created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.State = "checking"
		dst.BaseSHA = "base"
		completion := factoryCompletionOf(*dst)
		for _, n := range numbers {
			completion.Links = append(completion.Links, FactoryPRLink{RepoID: "repo", PR: reviewedPR(n), Origin: "user", ObservedAt: now, Fresh: true})
		}
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatalf("approve item: %v", err)
	}
	return saved
}

// post sends one Studio command through the route a Studio surface uses.
func (f *factoryCompletionHTTP) post(t *testing.T, id string, cmd FactoryCommand) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	req := testJSONRequest(http.MethodPost, "/api/factory/items/"+id+"/commands", string(raw))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

// command posts one Studio command, requires it to be accepted and returns the
// item the bridge answered with. No accepted response may quote the
// credential the fixture registered.
func (f *factoryCompletionHTTP) command(t *testing.T, id string, cmd FactoryCommand) FactoryItem {
	t.Helper()
	rec := f.post(t, id, cmd)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", cmd.Kind, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fixture-only") {
		t.Fatalf("%s leaked the credential: %s", cmd.Kind, rec.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, rec, &body)
	return body.Item
}

// journalLines is what the journal holds: one record per accepted mutation.
func (f *factoryCompletionHTTP) journalLines(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.store.file.Name()), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// advanceBatches runs one pass of the batch driver the daemon ticks.
func (f *factoryCompletionHTTP) advance(t *testing.T) {
	t.Helper()
	if err := f.controller.advanceCompletionBatches(context.Background()); err != nil {
		t.Fatalf("advance merge batches: %v", err)
	}
}

func TestFactoryHTTPCompletionStudioReviewsAndCompletes(t *testing.T) {
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	f := newFactoryCompletionHTTP(t, forge, forge)
	item := f.item(t, f.root)

	// A link carries the observation the bridge just made. The body may say
	// which pull request to link, never what its head is.
	linked := f.command(t, item.ID, FactoryCommand{
		RequestID: "link-12", Kind: "link_pr", ExpectedVersion: item.Version,
		Completion: &FactoryCompletionCommand{Link: "12"},
	})
	if linked.Completion == nil || len(linked.Completion.Links) != 1 {
		t.Fatalf("link_pr journalled %+v", linked.Completion)
	}
	if link := linked.Completion.Links[0]; !link.Fresh || link.PR.NodeID != "node-12" || link.Origin != "user" || link.RepoID != "repo" {
		t.Fatalf("link_pr journalled %+v", link)
	}
	second := f.command(t, item.ID, FactoryCommand{
		RequestID: "link-13", Kind: "link_pr", ExpectedVersion: linked.Version,
		Completion: &FactoryCompletionCommand{Link: "13"},
	})
	if second.Completion == nil || len(second.Completion.Links) != 2 || second.Completion.Links[1].PR.Number != 13 {
		t.Fatalf("second link %+v", second.Completion)
	}

	refreshed := f.command(t, item.ID, FactoryCommand{
		RequestID: "refresh-1", Kind: "refresh_pr_evidence", ExpectedVersion: second.Version,
	})
	if refreshed.Completion == nil || len(refreshed.Completion.Links) != 2 {
		t.Fatalf("refresh lost a link: %+v", refreshed.Completion)
	}
	for _, link := range refreshed.Completion.Links {
		if !link.Fresh || link.PR.State != "open" {
			t.Fatalf("a refreshed link is not the observed evidence: %+v", link)
		}
	}

	// An assessment is evidence about the outcome, not a gate on it: with no
	// adapter there is nothing to ask, and that state is recorded.
	assessed := f.command(t, item.ID, FactoryCommand{
		RequestID: "assess-1", Kind: "assess_completion", ExpectedVersion: refreshed.Version,
	})
	if assessed.Completion == nil || assessed.Completion.Assessment == nil || assessed.Completion.Assessment.Status != "unavailable" {
		t.Fatalf("assessment %+v", assessed.Completion)
	}

	completed := f.command(t, item.ID, FactoryCommand{
		RequestID: "complete-1", Kind: "complete", ExpectedVersion: assessed.Version,
	})
	if completed.Completion == nil || completed.Completion.Status != "complete" || len(completed.Completion.Records) != 1 {
		t.Fatalf("complete journalled %+v", completed.Completion)
	}
	record := completed.Completion.Records[0]
	if record.Goal != item.Title || record.Criteria != item.Draft.Spec || record.Actor != factoryStudioRevisionActor {
		t.Fatalf("completion record %+v", record)
	}
	if len(record.Links) != 2 || record.Links[0].PR.NodeID != "node-12" || record.Links[1].PR.NodeID != "node-13" {
		t.Fatalf("the record did not snapshot the reviewed links: %+v", record.Links)
	}
	if record.Assessment == nil || record.Assessment.Status != "unavailable" {
		t.Fatalf("the record did not snapshot the assessment the user saw: %+v", record.Assessment)
	}

	// Reopening retires the decision without rewriting it, so the surface can
	// still show what was called done.
	reopened := f.command(t, item.ID, FactoryCommand{
		RequestID: "reopen-1", Kind: "reopen", ExpectedVersion: completed.Version,
	})
	if reopened.Completion == nil || reopened.Completion.Status != "open" || len(reopened.Completion.Records) != 1 {
		t.Fatalf("reopen rewrote the record: %+v", reopened.Completion)
	}
	again := f.command(t, item.ID, FactoryCommand{
		RequestID: "complete-2", Kind: "complete", ExpectedVersion: reopened.Version,
	})
	if again.Completion == nil || again.Completion.Status != "complete" || len(again.Completion.Records) != 2 {
		t.Fatalf("a second completion %+v", again.Completion)
	}
}

func TestFactoryHTTPCompletionStaleVersion(t *testing.T) {
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	f := newFactoryCompletionHTTP(t, forge, forge)
	item := f.item(t, f.root, 12)
	rec := f.post(t, item.ID, FactoryCommand{RequestID: "complete-stale", Kind: "complete", ExpectedVersion: item.Version + 1})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale version: status %d, body %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Code           string `json:"code"`
		CurrentVersion uint64 `json:"currentVersion"`
	}
	decodeBody(t, rec, &conflict)
	if conflict.Code != "conflict" || conflict.CurrentVersion != item.Version {
		t.Fatalf("conflict body %s", rec.Body.String())
	}
	if after := currentItem(t, f.store, item.ID); after.Version != item.Version {
		t.Fatalf("a refused command moved the item to v%d", after.Version)
	}
}

func TestFactoryHTTPCompletionReplay(t *testing.T) {
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	f := newFactoryCompletionHTTP(t, forge, forge)
	item := f.item(t, f.root, 12)
	cmd := FactoryCommand{RequestID: "complete-replay", Kind: "complete", ExpectedVersion: item.Version}
	first := f.command(t, item.ID, cmd)
	lines := f.journalLines(t)
	replay := f.command(t, item.ID, cmd)
	if replay.Version != first.Version || replay.Completion == nil || len(replay.Completion.Records) != 1 {
		t.Fatalf("replay returned v%d with %+v", replay.Version, replay.Completion)
	}
	if got := f.journalLines(t); got != lines {
		t.Fatalf("the retry wrote %d journal records", got-lines)
	}
}

func TestFactoryHTTPCompletionMCPGrantsCannotInvoke(t *testing.T) {
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	f := newFactoryCompletionHTTP(t, forge, forge)
	grant := fullGrant()
	grant.ProjectID = f.projectID
	if err := f.fleet.ws.PutClient(MCPClient{ID: "c1", OwnerID: DefaultOwnerID, FactoryGrants: []MCPFactoryProjectGrant{grant}}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := f.fleet.ws.AddProject(other); err != nil {
		t.Fatal(err)
	}
	owned := f.item(t, f.root, 12)
	foreign := f.item(t, other, 12)
	kinds := []struct {
		kind       string
		completion *FactoryCompletionCommand
	}{
		{"complete", nil},
		{"reopen", nil},
		{"link_pr", &FactoryCompletionCommand{Link: "12"}},
		{"refresh_pr_evidence", nil},
		{"assess_completion", nil},
		{"merge_pr", mergeCommand("merge_pr", "squash", "", 12)},
		{"merge_and_complete", mergeCommand("merge_and_complete", "squash", "", 12)},
	}
	before := f.journalLines(t)
	// The grant is real: the authorizer admits the callback, so every refusal
	// below comes from the action itself and not from a missing capability.
	// Neither this client's own project nor another project's work item
	// accepts a completion action from it.
	for _, target := range []FactoryItem{owned, foreign} {
		for _, k := range kinds {
			item, kind, completion := target, k.kind, k.completion
			requestID := "mcp-" + kind + "-" + item.ID
			_, err := f.srv.factoryAuth.WithAuthorizedMutation("c1", f.projectID, "repo", factoryCapabilityApproveAndQueue, func() (FactoryItem, error) {
				return f.controller.CommandAuthorized(context.Background(), item.ID, FactoryCommand{
					RequestID: requestID, Kind: kind, ExpectedVersion: item.Version, Completion: completion,
				}, factoryPrincipal{ClientID: "c1", ProjectID: f.projectID, Action: factoryCapabilityApproveAndQueue})
			})
			if !errors.Is(err, ErrFactoryConflict) {
				t.Fatalf("%s on %s: err = %v, want a conflict", kind, item.ID, err)
			}
			// An assessment has no MCP case at all, so it answers with the
			// bare conflict; every other kind says why it refused.
			if kind != "assess_completion" && !strings.Contains(err.Error(), "not an action an MCP client may take") {
				t.Fatalf("%s was refused for the wrong reason: %v", kind, err)
			}
		}
	}
	if got := f.journalLines(t); got != before {
		t.Fatalf("a refused MCP action wrote %d journal records", got-before)
	}
}

func TestFactoryHTTPCompletionMergeWithoutMergeCapability(t *testing.T) {
	inner := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	readOnly := &readOnlyBatchForge{inner: inner}
	f := newFactoryCompletionHTTP(t, readOnly, readOnly)
	item := f.item(t, f.root, 12, 13)
	accepted := f.command(t, item.ID, FactoryCommand{
		RequestID: "merge-unsupported", Kind: "merge_and_complete", ExpectedVersion: item.Version,
		Completion: mergeCommand("merge_and_complete", "squash", "", 12, 13),
	})
	if accepted.Completion == nil || accepted.Completion.Batch == nil {
		t.Fatalf("the reviewed batch was not journalled: %+v", accepted.Completion)
	}
	batch := accepted.Completion.Batch
	if batch.State != "active" || !batch.CompleteAfter || len(batch.Steps) != 2 {
		t.Fatalf("accepted batch %+v", batch)
	}
	if batch.Steps[0].State != "unattempted" || batch.Steps[0].Strategy != "squash" {
		t.Fatalf("first step %+v", batch.Steps[0])
	}

	// One pass of the driver: the forge has no merge capability, so the first
	// reviewed merge fails and every later entry stays where it was.
	f.advance(t)
	after := currentItem(t, f.store, item.ID)
	batch = after.Completion.Batch
	if batch == nil || batch.State != "stopped" {
		t.Fatalf("batch after the unsupported merge %+v", batch)
	}
	if step := batch.Steps[0]; step.State != "failed" || !strings.Contains(step.Result, ErrFactoryMergeUnsupported.Error()) {
		t.Fatalf("first step after the unsupported merge %+v", step)
	}
	if batch.Steps[1].State != "unattempted" {
		t.Fatalf("a second merge followed a failed one: %+v", batch.Steps[1])
	}
	if len(inner.merges) != 0 {
		t.Fatalf("a forge without the capability recorded %d merges", len(inner.merges))
	}
	if after.Completion.Status == "complete" || len(after.Completion.Records) != 0 {
		t.Fatalf("an unmerged batch completed the item: %+v", after.Completion)
	}
}

func TestFactoryHTTPCompletionMergeAmbiguousOutcome(t *testing.T) {
	forge := &batchForge{
		prs:      map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)},
		mergeErr: errors.New("502 Bad Gateway"),
	}
	f := newFactoryCompletionHTTP(t, forge, forge)
	item := f.item(t, f.root, 12, 13)
	f.command(t, item.ID, FactoryCommand{
		RequestID: "merge-ambiguous", Kind: "merge_and_complete", ExpectedVersion: item.Version,
		Completion: mergeCommand("merge_and_complete", "squash", "", 12, 13),
	})

	f.advance(t)
	after := currentItem(t, f.store, item.ID)
	batch := after.Completion.Batch
	if batch == nil || batch.State != "stopped" {
		t.Fatalf("batch after an ambiguous merge %+v", batch)
	}
	step := batch.Steps[0]
	if step.State != "uncertain" || !strings.Contains(step.Result, "unproven") {
		t.Fatalf("an ambiguous merge was settled as %+v", step)
	}
	if len(forge.merges) != 1 {
		t.Fatalf("%d merges were sent for one reviewed step", len(forge.merges))
	}
	if batch.Steps[1].State != "unattempted" {
		t.Fatalf("a second merge followed an unproven one: %+v", batch.Steps[1])
	}
	// The batch was authorized to complete the item, and it only lands when
	// every reviewed merge confirmed.
	if after.Completion.Status == "complete" || len(after.Completion.Records) != 0 {
		t.Fatalf("an unproven merge completed the item: %+v", after.Completion)
	}
}
