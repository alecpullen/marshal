package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// factoryDefaultsFixture is the offline harness these tests share: a fake
// control agent with a canned config/get, one registered GitHub repository on
// the RFC 2606 reserved example.invalid host, a PAT read from the environment
// through an in-memory credential store, and a real factory API over a
// temp-dir journal. Nothing here reaches a forge, a git remote, a container
// runtime or a model provider: preflight constructs the forge client but never
// calls it.
type factoryDefaultsFixture struct {
	fleet *Fleet
	store *FactoryStore
	// api is the transport-free surface the routes serve, so a test can drive
	// the layer that normalises and preflights a retry replacement directly.
	api  FactoryAPI
	srv  *Server
	root string
}

// factoryDefaultsRouting is the routing profile the canned config/get result
// serves, so an inherited or hand-written routing passes preflight.
func factoryDefaultsRouting() json.RawMessage {
	return json.RawMessage(`{"profile":"chosen"}`)
}

func newFactoryDefaultsFixture(t *testing.T) *factoryDefaultsFixture {
	t.Helper()
	agent := newFakeAgent()
	fleet := ctlFleet(t, agent)
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := fleet.ws.PutRepo(Repo{
		ID:      "repo-1",
		URL:     "https://example.invalid/repo.git",
		Branch:  "main",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		CredRef: "test-pat",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_DEFAULTS_TEST_PAT", "fixture-only")
	fleet.creds = NewCredentialStore([]Credential{{ID: "test-pat", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "FACTORY_DEFAULTS_TEST_PAT"}})
	agent.results["config/get"] = map[string]any{
		"providers": map[string]any{"test": map[string]any{"type": "openai_compatible", "hasKey": true}},
		"presets":   map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}},
		"profiles": map[string]any{"chosen": map[string]any{
			"implementer": map[string]any{"preset": "test/model"},
			"reviewer":    map[string]any{"preset": "test/model"},
		}},
		"defaultProfile": "chosen",
	}
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	api := NewFactoryAPI(store, NewFactoryController(store, nil, nil), fleet)
	srv := NewServer(fleet, "secret", "", api)
	return &factoryDefaultsFixture{fleet: fleet, store: store, api: api, srv: srv, root: root}
}

func (f *factoryDefaultsFixture) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer secret"}
}

// putSettings writes a project's settings through the real settings surface,
// so the stored defaults are exactly what a client would persist.
func (f *factoryDefaultsFixture) putSettings(t *testing.T, root string, ps ProjectSettings) {
	t.Helper()
	rec := doReq(t, f.srv, http.MethodPut, "/api/projects/settings?root="+url.QueryEscape(root), ps, f.headers())
	if rec.Code != http.StatusOK {
		t.Fatalf("put project settings: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// handoff is the smallest valid request naming root: every inheritable field
// is left unset, so the project's defaults are their only source.
func (f *factoryDefaultsFixture) handoff(requestID, root string) FactoryCreate {
	return FactoryCreate{
		RequestID: requestID,
		Title:     "Add the thing",
		Plan:      "implement the thing",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              root,
			Native:                   true,
			NoRequiredCIAcknowledged: true,
		},
	}
}

func (f *factoryDefaultsFixture) create(t *testing.T, req FactoryCreate) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, f.srv, http.MethodPost, "/api/factory/items", factoryHandoffBody(t, req), f.headers())
}

// factoryHandoffBody encodes a handoff the way a client sends one: a routing
// the caller left unset is omitted, not written as the literal null. §4.3
// makes an unset json.RawMessage a nil or zero-length message, and null is an
// explicit value, so the distinction matters to resolution.
func factoryHandoffBody(t *testing.T, req FactoryCreate) map[string]any {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	config, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatalf("handoff has no config object: %s", raw)
	}
	for _, key := range []string{"implementerRouting", "reviewerRouting"} {
		if v, present := config[key]; present && v == nil {
			delete(config, key)
		}
	}
	return body
}

func (f *factoryDefaultsFixture) created(t *testing.T, req FactoryCreate) FactoryItem {
	t.Helper()
	rec := f.create(t, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, rec, &body)
	return body.Item
}

// sameFactoryConfig compares two configurations byte for byte after a JSON
// round trip, which keeps nil-versus-empty distinctions honest: a nil check
// list renders as null and an explicitly empty one as [].
func sameFactoryConfig(t *testing.T, got, want FactoryConfig) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, w) {
		t.Fatalf("configuration = %s, want %s", g, w)
	}
}

func TestFactoryCreateResolvesProjectDefaultsIntoStoredDraft(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:         "main",
		HostID:               "local",
		ImplementerRouting:   routing,
		ReviewerRouting:      routing,
		ReviewerInstructions: "be terse",
		ExpectedChecks:       []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		MaxRounds:            9,
		CIPollSeconds:        30,
		CIWaitSeconds:        900,
	}})
	item := f.created(t, f.handoff("create-1", f.root))
	if item.State != "draft" {
		t.Fatalf("state = %q, want draft", item.State)
	}
	want := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              f.root,
		HostID:                   "local",
		TargetBranch:             "main",
		Native:                   true,
		ImplementerRouting:       routing,
		ReviewerRouting:          routing,
		ReviewerInstructions:     "be terse",
		ExpectedChecks:           []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		NoRequiredCIAcknowledged: true,
		MaxRounds:                9,
		CIPollSeconds:            30,
		CIWaitSeconds:            900,
	}
	sameFactoryConfig(t, item.Draft.Config, want)
	stored, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameFactoryConfig(t, stored.Draft.Config, want)
}

func TestFactoryCreateHandoffOverridesDefault(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:         "main",
		ImplementerRouting:   routing,
		ReviewerRouting:      routing,
		ReviewerInstructions: "be terse",
		MaxRounds:            9,
		CIPollSeconds:        30,
		CIWaitSeconds:        900,
	}})
	req := f.handoff("create-1", f.root)
	req.Config.TargetBranch = "release"
	req.Config.ReviewerInstructions = "be loud"
	req.Config.MaxRounds = 3
	item := f.created(t, req)
	want := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              f.root,
		HostID:                   "local",
		TargetBranch:             "release",
		Native:                   true,
		ImplementerRouting:       routing,
		ReviewerRouting:          routing,
		ReviewerInstructions:     "be loud",
		NoRequiredCIAcknowledged: true,
		MaxRounds:                3,
		CIPollSeconds:            30,
		CIWaitSeconds:            900,
	}
	sameFactoryConfig(t, item.Draft.Config, want)
}

func TestFactoryCreatePreservesExplicitlyEmptyChecks(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "main",
		ImplementerRouting: routing,
		ReviewerRouting:    routing,
		ExpectedChecks:     []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
	}})
	req := f.handoff("create-1", f.root)
	// An explicitly empty list means "require no checks", which is distinct
	// from inheriting the project's own list.
	req.Config.ExpectedChecks = []FactoryCheckKey{}
	item := f.created(t, req)
	stored, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Draft.Config.ExpectedChecks == nil {
		t.Fatal("explicitly empty expectedChecks became nil, so it was replaced by the default")
	}
	if len(stored.Draft.Config.ExpectedChecks) != 0 {
		t.Fatalf("expectedChecks = %+v, want an empty list", stored.Draft.Config.ExpectedChecks)
	}
}

func TestFactoryDefaultsResolveBeforeNormalisation(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "main",
		ImplementerRouting: routing,
		ReviewerRouting:    routing,
		MaxRounds:          9,
	}})
	item := f.created(t, f.handoff("create-1", f.root))
	if got := item.Draft.Config.MaxRounds; got != 9 {
		t.Fatalf("maxRounds = %d, want the project default 9, not the built-in 5", got)
	}
	// Normalisation still fills the limits the defaults left unset, so the
	// default was merged before it ran rather than after.
	if got := item.Draft.Config.CIPollSeconds; got != 15 {
		t.Fatalf("ciPollSeconds = %d, want the built-in 15", got)
	}
	if got := item.Draft.Config.CIWaitSeconds; got != 1800 {
		t.Fatalf("ciWaitSeconds = %d, want the built-in 1800", got)
	}
}

func TestFactoryDefaultsResolveFromNamedProjectRootNotIntake(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	intakeRoot, namedRoot := f.root, t.TempDir()
	if err := f.fleet.ws.AddProject(namedRoot); err != nil {
		t.Fatal(err)
	}
	// The intake mapping names the first project, so a resolution that
	// consulted it instead of the named root would pick the wrong defaults.
	f.putSettings(t, intakeRoot, ProjectSettings{
		Intake: ProjectIntake{RepoID: "repo-1"},
		FactoryDefaults: &FactoryDefaults{
			TargetBranch:         "intake-branch",
			ImplementerRouting:   factoryDefaultsRouting(),
			ReviewerRouting:      factoryDefaultsRouting(),
			ReviewerInstructions: "intake instructions",
			MaxRounds:            9,
		},
	})
	f.putSettings(t, namedRoot, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:         "named-branch",
		ImplementerRouting:   factoryDefaultsRouting(),
		ReviewerRouting:      factoryDefaultsRouting(),
		ReviewerInstructions: "named instructions",
		MaxRounds:            3,
	}})
	named := f.created(t, f.handoff("create-named", namedRoot))
	if named.Draft.Config.TargetBranch != "named-branch" || named.Draft.Config.MaxRounds != 3 || named.Draft.Config.ReviewerInstructions != "named instructions" {
		t.Fatalf("named root defaults not applied: %+v", named.Draft.Config)
	}
	if named.Draft.Config.ProjectRoot != namedRoot || named.ProjectRoot != namedRoot {
		t.Fatalf("project root = %q, want the named root %q", named.Draft.Config.ProjectRoot, namedRoot)
	}
	intake := f.created(t, f.handoff("create-intake", intakeRoot))
	if intake.Draft.Config.TargetBranch != "intake-branch" || intake.Draft.Config.MaxRounds != 9 || intake.Draft.Config.ReviewerInstructions != "intake instructions" {
		t.Fatalf("intake root defaults not applied: %+v", intake.Draft.Config)
	}
}

func TestFactoryDefaultsRootlessHandoffIsStillRejected(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.putSettings(t, f.root, ProjectSettings{
		Intake: ProjectIntake{RepoID: "repo-1"},
		FactoryDefaults: &FactoryDefaults{
			TargetBranch:       "main",
			ImplementerRouting: factoryDefaultsRouting(),
			ReviewerRouting:    factoryDefaultsRouting(),
		},
	})
	req := f.handoff("create-rootless", "")
	rec := f.create(t, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rootless handoff: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "project root") {
		t.Fatalf("rootless handoff error did not name the missing root: %s", rec.Body.String())
	}
	// The defensive intake fallback only chooses which defaults are read; it
	// never supplies a root, so no work item exists.
	if items := f.store.List(); len(items) != 0 {
		t.Fatalf("rootless handoff created %d work items", len(items))
	}
}

func TestFactoryCreateStoredDraftIsSnapshotted(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "main",
		ImplementerRouting: routing,
		ReviewerRouting:    routing,
		MaxRounds:          9,
	}})
	item := f.created(t, f.handoff("create-1", f.root))
	if item.Draft.Config.MaxRounds != 9 || item.Draft.Config.TargetBranch != "main" {
		t.Fatalf("create did not resolve the defaults: %+v", item.Draft.Config)
	}
	// Change the project's defaults through the real settings surface.
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "trunk",
		ImplementerRouting: routing,
		ReviewerRouting:    routing,
		MaxRounds:          2,
	}})
	stored, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Draft.Config.MaxRounds != 9 || stored.Draft.Config.TargetBranch != "main" {
		t.Fatalf("stored draft changed with the project defaults: %+v", stored.Draft.Config)
	}
	approved := doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+item.ID+"/commands",
		FactoryCommand{RequestID: "approve-1", Kind: "approve", ExpectedVersion: 1}, f.headers())
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", approved.Code, approved.Body.String())
	}
	var approvalBody struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, approved, &approvalBody)
	if len(approvalBody.Item.Approvals) != 1 {
		t.Fatalf("approvals = %+v", approvalBody.Item.Approvals)
	}
	snapshot := approvalBody.Item.Approvals[0].Config
	if snapshot.MaxRounds != 9 || snapshot.TargetBranch != "main" {
		t.Fatalf("approval configuration changed with the project defaults: %+v", snapshot)
	}
	// The next handoff picks up the new defaults, so the change took effect.
	next := f.created(t, f.handoff("create-2", f.root))
	if next.Draft.Config.MaxRounds != 2 || next.Draft.Config.TargetBranch != "trunk" {
		t.Fatalf("next handoff did not pick up the new defaults: %+v", next.Draft.Config)
	}
}

func TestFactoryCreateReplayReturnsTheOriginalItem(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "main",
		ImplementerRouting: routing,
		ReviewerRouting:    routing,
		MaxRounds:          9,
	}})
	req := f.handoff("create-replay", f.root)
	original := f.created(t, req)
	// Rewrite the defaults to a set that is invalid on its own, the way an
	// older binary or a hand edit can leave them in the workspace file.
	if err := f.fleet.ws.PutProjectSettings(f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{HostID: "remote"}}); err != nil {
		t.Fatal(err)
	}
	replay := f.create(t, req)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: status %d, body %s", replay.Code, replay.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, replay, &body)
	if body.Item.ID != original.ID || body.Item.Version != original.Version {
		t.Fatalf("replay returned %s@%d, want the original %s@%d", body.Item.ID, body.Item.Version, original.ID, original.Version)
	}
	// A fresh request is resolved again and does fail, which proves the
	// rewrite really made the defaults unusable.
	fresh := req
	fresh.RequestID = "create-fresh"
	if rec := f.create(t, fresh); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fresh request against invalid defaults: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestFactoryDefaultsConflictNamesBothSources(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		Workspace:          "ws://published/example",
		ImplementerRouting: factoryDefaultsRouting(),
		ReviewerRouting:    factoryDefaultsRouting(),
	}})
	// The handoff asks for a native run, which the inherited workspace cannot
	// satisfy. The defaults are not at fault on their own, so this is a
	// conflict between two sources and must name both of them.
	rec := f.create(t, f.handoff("create-conflict", f.root))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("default workspace with a native handoff: status %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"conflict", "workspace", "native", "mutually exclusive", f.root, "handoff", `"code":"invalid_input"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("conflict error does not name %q: %s", want, body)
		}
	}
	if items := f.store.List(); len(items) != 0 {
		t.Fatalf("conflicting handoff created %d work items", len(items))
	}
	// A handoff that sets both native and workspace itself is a
	// handoff-internal conflict: the project's defaults are not involved, so
	// the unchanged normalisation reports it rather than resolution.
	clean := t.TempDir()
	if err := f.fleet.ws.AddProject(clean); err != nil {
		t.Fatal(err)
	}
	internal := f.handoff("create-native-and-workspace", clean)
	internal.Config.Workspace = "ws://published/example"
	rec = f.create(t, internal)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handoff-internal conflict: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); strings.Contains(got, "project defaults") || !strings.Contains(got, "mutually exclusive") {
		t.Fatalf("handoff-internal conflict reported as a defaults conflict: %s", got)
	}
}

func TestFactoryDefaultsRejectInvalidDefaultsAtResolveTime(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	// Written straight into the workspace file, the way an older binary or a
	// hand edit can leave a default the write-time validator would reject.
	if err := f.fleet.ws.PutProjectSettings(f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		MaxRounds:          -1,
		ImplementerRouting: factoryDefaultsRouting(),
		ReviewerRouting:    factoryDefaultsRouting(),
	}}); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, rec *httptest.ResponseRecorder, label string) {
		t.Helper()
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, body %s", label, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{"project factory defaults", f.root, "maxRounds", `"code":"unsupported"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: error does not name %q: %s", label, want, body)
			}
		}
	}
	check(t, f.create(t, f.handoff("create-invalid", f.root)), "invalid defaults")
	// A handoff that sets the offending field does not rescue the defaults:
	// they are validated on their own, never as a silent fallback.
	override := f.handoff("create-override", f.root)
	override.Config.MaxRounds = 3
	check(t, f.create(t, override), "handoff overrides the offending field")
	if items := f.store.List(); len(items) != 0 {
		t.Fatalf("invalid defaults created %d work items", len(items))
	}
}

func TestFactoryRetryReplacementIsNotReResolved(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	routing := factoryDefaultsRouting()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:         "main",
		ImplementerRouting:   routing,
		ReviewerRouting:      routing,
		ReviewerInstructions: "be terse",
		MaxRounds:            9,
		CIPollSeconds:        30,
		CIWaitSeconds:        900,
	}})
	item := f.created(t, f.handoff("create-1", f.root))
	if item.Draft.Config.ReviewerInstructions != "be terse" || item.Draft.Config.MaxRounds != 9 {
		t.Fatalf("create did not resolve the defaults: %+v", item.Draft.Config)
	}
	// The retry path needs a decision, a positive round grant and a reason;
	// the decision is set through the store's public mutation path, so no
	// effect, forge or loop is involved.
	item, err := f.store.Update(item.ID, "park-for-question", item.Version, json.RawMessage(`{"decision":"question"}`), func(dst *FactoryItem) error {
		dst.State = "needs_input"
		dst.Decision = &FactoryDecision{ID: "q", Kind: "question", Question: "continue?", Reason: "implementer needs input"}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	replacement := FactoryCreate{
		RequestID: "replacement",
		Title:     "Add the thing",
		Plan:      "implement the thing a different way",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              f.root,
			TargetBranch:             "release",
			Native:                   true,
			ImplementerRouting:       routing,
			ReviewerRouting:          routing,
			NoRequiredCIAcknowledged: true,
		},
	}
	got, err := f.api.Command(context.Background(), item.ID, FactoryCommand{
		RequestID:        "retry-1",
		Kind:             "retry",
		DecisionID:       "q",
		Reason:           "restart with a replacement",
		ExpectedVersion:  item.Version,
		AdditionalRounds: 1,
		Replacement:      &replacement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want the replacement's single approval", got.Approvals)
	}
	snapshot := got.Approvals[0].Config
	// The replacement is taken as written: a field the project default sets
	// and the replacement omits stays empty instead of being inherited.
	if snapshot.ReviewerInstructions != "" {
		t.Fatalf("reviewer instructions = %q, want the replacement's empty value", snapshot.ReviewerInstructions)
	}
	// Normalisation still fills the built-in limits, which is what separates
	// an un-resolved replacement from a resolved one: the project's 9 and 30
	// would have won had resolution run.
	if snapshot.MaxRounds != 5 || snapshot.CIPollSeconds != 15 || snapshot.CIWaitSeconds != 1800 {
		t.Fatalf("limits = %d/%d/%d, want the built-in 5/15/1800", snapshot.MaxRounds, snapshot.CIPollSeconds, snapshot.CIWaitSeconds)
	}
	if snapshot.TargetBranch != "release" || snapshot.ProjectRoot != f.root {
		t.Fatalf("replacement configuration was not taken as written: %+v", snapshot)
	}
}
