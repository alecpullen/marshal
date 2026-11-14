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
	api FactoryAPI
	srv *Server
	// agent is the control agent behind the canned config/get, so a test can
	// replace that result with a model configuration of its own.
	agent *fakeAgent
	root  string
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
	return &factoryDefaultsFixture{fleet: fleet, store: store, api: api, srv: srv, agent: agent, root: root}
}

func (f *factoryDefaultsFixture) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer secret"}
}

// addProfile registers an extra routing profile in the canned model
// configuration, so a test can move a project's routing profile between two
// creates without preflight rejecting the new one.
func (f *factoryDefaultsFixture) addProfile(t *testing.T, name string) {
	t.Helper()
	config, ok := f.agent.results["config/get"].(map[string]any)
	if !ok {
		t.Fatalf("canned model configuration is %T, not an object", f.agent.results["config/get"])
	}
	profiles, ok := config["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("canned model profiles is %T, not an object", config["profiles"])
	}
	profiles[name] = map[string]any{
		"implementer": map[string]any{"preset": "test/model"},
		"reviewer":    map[string]any{"preset": "test/model"},
	}
}

// resolve runs resolution alone: no normalisation and no preflight. A
// published workspace cannot be resolved or built offline, so the workspace
// half of a precedence level is pinned here; the create path covers every
// field preflight can check.
func (f *factoryDefaultsFixture) resolve(t *testing.T, req FactoryCreate) FactoryConfig {
	t.Helper()
	concrete, ok := f.api.(*factoryAPI)
	if !ok {
		t.Fatalf("factory API is %T, not *factoryAPI", f.api)
	}
	resolved, err := concrete.factoryResolveDefaults(req)
	if err != nil {
		t.Fatalf("resolve defaults: %v", err)
	}
	return resolved.Config
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

// TestFactoryBootstrapAcceptanceContractStillHolds pins the specification's
// §4.8 list: project defaults add a source of configuration without changing
// a single rule of the bootstrap factory's acceptance contract. Every rule is
// exercised twice — against a project with no defaults and against a project
// whose defaults are a full set — so a resolution that weakened a rule could
// not hide behind either state.
func TestFactoryBootstrapAcceptanceContractStillHolds(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	withDefaults := t.TempDir()
	if err := f.fleet.ws.AddProject(withDefaults); err != nil {
		t.Fatal(err)
	}
	// A full set of defaults, minus workspaces: a workspace default conflicts
	// with every native handoff (§4.7), and an offline test cannot resolve a
	// published workspace. Everything else a project can set is set here.
	f.putSettings(t, withDefaults, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:         "main",
		HostID:               "local",
		ImplementerRouting:   factoryDefaultsRouting(),
		ReviewerRouting:      factoryDefaultsRouting(),
		ReviewerInstructions: "be terse",
		ExpectedChecks:       []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		MaxRounds:            9,
		CIPollSeconds:        30,
		CIWaitSeconds:        900,
	}})

	projects := []struct {
		name string
		root string
		// limits are what a handoff that sets none must end up with: the
		// built-in values when nothing supplies them, the project's own when
		// its defaults do.
		maxRounds     int
		ciPollSeconds int
		ciWaitSeconds int
	}{
		{"no defaults", f.root, 5, 15, 1800},
		{"full defaults", withDefaults, 9, 30, 900},
	}
	for _, project := range projects {
		project := project
		t.Run(project.name, func(t *testing.T) {
			// base is the smallest handoff a project with no defaults accepts:
			// the fields only a handoff can supply. Request ids are prefixed
			// with the project, because the journal's replay identity is
			// global to the store.
			base := func(name string) FactoryCreate {
				req := f.handoff(project.name+"/"+name, project.root)
				req.Config.TargetBranch = "main"
				req.Config.ImplementerRouting = factoryDefaultsRouting()
				req.Config.ReviewerRouting = factoryDefaultsRouting()
				return req
			}
			items := func() int { return len(f.store.List()) }
			expectRejected := func(t *testing.T, rec *httptest.ResponseRecorder, status int, wants ...string) {
				t.Helper()
				if rec.Code != status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
				}
				for _, want := range wants {
					if !strings.Contains(rec.Body.String(), want) {
						t.Fatalf("error does not name %q: %s", want, rec.Body.String())
					}
				}
			}

			// §4.8: the request id, title, plan and repository are still
			// required, and no default can supply one of them.
			for _, tc := range []struct {
				name string
				drop func(*FactoryCreate)
			}{
				{"requestId", func(r *FactoryCreate) { r.RequestID = "" }},
				{"title", func(r *FactoryCreate) { r.Title = "" }},
				{"plan", func(r *FactoryCreate) { r.Plan = "" }},
				{"repository", func(r *FactoryCreate) { r.Config.RepoID = "" }},
			} {
				before := items()
				req := base("identity-" + tc.name)
				tc.drop(&req)
				expectRejected(t, f.create(t, req), http.StatusBadRequest, "requestId, title, plan and repository required")
				if got := items(); got != before {
					t.Fatalf("a handoff missing %s created %d work items", tc.name, got-before)
				}
			}

			// §4.8: the plan and specification size limit is unchanged.
			before := items()
			tooLong := base("plan-over-limit")
			tooLong.Plan = strings.Repeat("x", factoryImportLimit+1)
			expectRejected(t, f.create(t, tooLong), http.StatusBadRequest, "1 MiB")
			combined := base("plan-and-spec-over-limit")
			combined.Plan = strings.Repeat("x", factoryImportLimit/2)
			combined.Spec = strings.Repeat("y", factoryImportLimit/2+1)
			expectRejected(t, f.create(t, combined), http.StatusBadRequest, "1 MiB")
			if got := items(); got != before {
				t.Fatalf("oversized handoffs created %d work items", got-before)
			}
			// A handoff at exactly the limit is still accepted, so the rule is
			// the unchanged "more than 1 MiB" and not a stricter one.
			atLimit := f.created(t, func() FactoryCreate {
				req := base("plan-at-limit")
				req.Plan = strings.Repeat("x", factoryImportLimit)
				return req
			}())
			if len(atLimit.Draft.Plan) != factoryImportLimit {
				t.Fatalf("stored plan = %d bytes, want the %d-byte plan it accepted", len(atLimit.Draft.Plan), factoryImportLimit)
			}

			// §4.8: the local-host requirement is unchanged, and a handoff
			// that names a remote host is still rejected even when the
			// project's default is local.
			before = items()
			remote := base("remote-host")
			remote.Config.HostID = "remote"
			expectRejected(t, f.create(t, remote), http.StatusBadRequest, "local host")
			if got := items(); got != before {
				t.Fatalf("a remote-host handoff created %d work items", got-before)
			}

			// §4.8: the built-in limits are unchanged, and a project default
			// may set them explicitly — resolution runs before normalisation,
			// so the project's values win where it sets them.
			config := f.created(t, base("limits")).Draft.Config
			if config.HostID != "local" {
				t.Fatalf("hostId = %q, want the built-in %q", config.HostID, "local")
			}
			if config.MaxRounds != project.maxRounds || config.CIPollSeconds != project.ciPollSeconds || config.CIWaitSeconds != project.ciWaitSeconds {
				t.Fatalf("limits = %d/%d/%d, want %d/%d/%d",
					config.MaxRounds, config.CIPollSeconds, config.CIWaitSeconds,
					project.maxRounds, project.ciPollSeconds, project.ciWaitSeconds)
			}
			// A handoff that sets the limits itself wins over both sources.
			explicit := base("limits-set-by-handoff")
			explicit.Config.MaxRounds = 2
			explicit.Config.CIPollSeconds = 5
			explicit.Config.CIWaitSeconds = 60
			config = f.created(t, explicit).Draft.Config
			if config.MaxRounds != 2 || config.CIPollSeconds != 5 || config.CIWaitSeconds != 60 {
				t.Fatalf("limits = %d/%d/%d, want the handoff's 2/5/60", config.MaxRounds, config.CIPollSeconds, config.CIWaitSeconds)
			}

			// §4.8: a handoff must select expected CI checks or acknowledge
			// that none are required. An explicitly empty list is still an
			// empty list for that rule, in both projects.
			unacknowledged := base("ci-unacknowledged")
			unacknowledged.Config.ExpectedChecks = []FactoryCheckKey{}
			unacknowledged.Config.NoRequiredCIAcknowledged = false
			draft := f.created(t, unacknowledged)
			expectRejected(t, doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+draft.ID+"/commands",
				FactoryCommand{RequestID: project.name + "/approve-unacknowledged", Kind: "approve", ExpectedVersion: 1}, f.headers()),
				http.StatusUnprocessableEntity, "expected CI checks or acknowledge none are required")
			// The rejected approval left the item a draft, as it does today.
			if stored, err := f.store.Get(draft.ID); err != nil || stored.State != "draft" || stored.Version != 1 {
				t.Fatalf("rejected approval changed the item: %v %+v", err, stored)
			}
			// Acknowledging that none are required satisfies the rule.
			acknowledged := base("ci-acknowledged")
			acknowledged.Config.ExpectedChecks = []FactoryCheckKey{}
			created := f.created(t, acknowledged)
			if rec := doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+created.ID+"/commands",
				FactoryCommand{RequestID: project.name + "/approve-acknowledged", Kind: "approve", ExpectedVersion: 1}, f.headers()); rec.Code != http.StatusOK {
				t.Fatalf("acknowledged approval: status %d, body %s", rec.Code, rec.Body.String())
			}
			// Selecting an expected check satisfies it without the
			// acknowledgement.
			selected := base("ci-selected")
			selected.Config.ExpectedChecks = []FactoryCheckKey{{Kind: "commit_status", Name: "gate"}}
			selected.Config.NoRequiredCIAcknowledged = false
			created = f.created(t, selected)
			if rec := doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+created.ID+"/commands",
				FactoryCommand{RequestID: project.name + "/approve-selected", Kind: "approve", ExpectedVersion: 1}, f.headers()); rec.Code != http.StatusOK {
				t.Fatalf("approval with selected checks: status %d, body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestFactoryAgentDefaultsFollowProject pins resolution's lower precedence
// level: the project's agent workspace and routing profile are what a handoff
// inherits when neither the handoff nor the project's factory defaults set
// them. A change between two creates reaches only the second one, and each
// draft stays a snapshot of the settings its own create resolved against.
func TestFactoryAgentDefaultsFollowProject(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.addProfile(t, "later")
	firstWorkspace, secondWorkspace := "first", "second"
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: firstWorkspace,
		Routing:   json.RawMessage(`{"profile":"chosen"}`),
	})

	// Resolution alone pins the workspace half of the fallback: a published
	// workspace cannot be resolved or built offline, so preflight is out of
	// reach here.
	plain := f.handoff("agent-defaults-plain-1", f.root)
	plain.Config.Native = false
	resolved := f.resolve(t, plain)
	if resolved.Workspace != firstWorkspace {
		t.Fatalf("workspace = %q, want the project agent workspace %q", resolved.Workspace, firstWorkspace)
	}
	assertFactoryRoutes(t, resolved, `{"profile":"chosen"}`)

	// A native handoff suppresses that fallback, so these creates need no
	// published workspace to reach preflight.
	nativeCreate := func(requestID string) FactoryCreate {
		req := f.handoff(requestID, f.root)
		req.Config.TargetBranch = "main"
		return req
	}
	first := f.created(t, nativeCreate("agent-defaults-create-1"))
	if first.Draft.Config.Workspace != "" {
		t.Fatalf("workspace = %q, want none for a native handoff", first.Draft.Config.Workspace)
	}
	assertFactoryRoutes(t, first.Draft.Config, `{"profile":"chosen"}`)

	// Move both project agent defaults between the two creates.
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: secondWorkspace,
		Routing:   json.RawMessage(`{"profile":"later"}`),
	})
	plain = f.handoff("agent-defaults-plain-2", f.root)
	plain.Config.Native = false
	resolved = f.resolve(t, plain)
	if resolved.Workspace != secondWorkspace {
		t.Fatalf("workspace = %q, want the project agent workspace %q", resolved.Workspace, secondWorkspace)
	}
	assertFactoryRoutes(t, resolved, `{"profile":"later"}`)

	second := f.created(t, nativeCreate("agent-defaults-create-2"))
	assertFactoryRoutes(t, second.Draft.Config, `{"profile":"later"}`)

	// The first draft keeps the profile it resolved against: both roles follow
	// the project, but only for creates made after the change.
	stored, err := f.store.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertFactoryRoutes(t, stored.Draft.Config, `{"profile":"chosen"}`)
}

// TestFactoryExplicitOverridesAndReset pins the precedence of each of the three
// fields the project agent defaults cover. An explicit handoff value wins, the
// project's factory default outranks the agent fallback, and removing an
// override restores the fallback for that field alone.
func TestFactoryExplicitOverridesAndReset(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.addProfile(t, "later")
	projectWorkspace := "project"
	fallback := json.RawMessage(`{"profile":"later"}`)
	f.putSettings(t, f.root, ProjectSettings{Workspace: projectWorkspace, Routing: fallback})
	explicit := json.RawMessage(`{"profile":"chosen"}`)

	// The three fields resolve independently, so setting one leaves the other
	// two on the project's fallback.
	cases := []struct {
		key           string
		name          string
		implementer   json.RawMessage
		reviewer      json.RawMessage
		workspace     string
		wantImpl      string
		wantRev       string
		wantWorkspace string
	}{
		{
			key: "none", name: "nothing explicit",
			wantImpl: `{"profile":"later"}`, wantRev: `{"profile":"later"}`, wantWorkspace: projectWorkspace,
		},
		{
			key: "implementer", name: "implementer profile only",
			implementer: explicit,
			wantImpl:    `{"profile":"chosen"}`, wantRev: `{"profile":"later"}`, wantWorkspace: projectWorkspace,
		},
		{
			key: "reviewer", name: "reviewer profile only",
			reviewer: explicit,
			wantImpl: `{"profile":"later"}`, wantRev: `{"profile":"chosen"}`, wantWorkspace: projectWorkspace,
		},
		{
			key: "workspace", name: "workspace only",
			workspace: "ws://handoff/explicit",
			wantImpl:  `{"profile":"later"}`, wantRev: `{"profile":"later"}`, wantWorkspace: "ws://handoff/explicit",
		},
		{
			key: "all", name: "all three explicit",
			implementer: explicit, reviewer: explicit, workspace: "ws://handoff/explicit",
			wantImpl: `{"profile":"chosen"}`, wantRev: `{"profile":"chosen"}`, wantWorkspace: "ws://handoff/explicit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := f.handoff("override-"+tc.key, f.root)
			req.Config.Native = false
			req.Config.ImplementerRouting = tc.implementer
			req.Config.ReviewerRouting = tc.reviewer
			req.Config.Workspace = tc.workspace
			got := f.resolve(t, req)
			if !bytes.Equal(got.ImplementerRouting, json.RawMessage(tc.wantImpl)) {
				t.Fatalf("implementer routing = %q, want %q", got.ImplementerRouting, tc.wantImpl)
			}
			if !bytes.Equal(got.ReviewerRouting, json.RawMessage(tc.wantRev)) {
				t.Fatalf("reviewer routing = %q, want %q", got.ReviewerRouting, tc.wantRev)
			}
			if got.Workspace != tc.wantWorkspace {
				t.Fatalf("workspace = %q, want %q", got.Workspace, tc.wantWorkspace)
			}
		})
	}

	// Removing one override restores the fallback for exactly that field.
	full := f.handoff("override-removed", f.root)
	full.Config.Native = false
	full.Config.Workspace = "ws://handoff/explicit"
	full.Config.ImplementerRouting, full.Config.ReviewerRouting = explicit, explicit

	noImplementer := full
	noImplementer.Config.ImplementerRouting = nil
	got := f.resolve(t, noImplementer)
	if !bytes.Equal(got.ImplementerRouting, fallback) || !bytes.Equal(got.ReviewerRouting, explicit) || got.Workspace != "ws://handoff/explicit" {
		t.Fatalf("removing the implementer route did not restore only that fallback: %+v", got)
	}
	noReviewer := full
	noReviewer.Config.ReviewerRouting = json.RawMessage{}
	got = f.resolve(t, noReviewer)
	if !bytes.Equal(got.ReviewerRouting, fallback) || !bytes.Equal(got.ImplementerRouting, explicit) || got.Workspace != "ws://handoff/explicit" {
		t.Fatalf("removing the reviewer route did not restore only that fallback: %+v", got)
	}
	noWorkspace := full
	noWorkspace.Config.Workspace = ""
	got = f.resolve(t, noWorkspace)
	if got.Workspace != projectWorkspace || !bytes.Equal(got.ImplementerRouting, explicit) || !bytes.Equal(got.ReviewerRouting, explicit) {
		t.Fatalf("removing the workspace did not restore only that fallback: %+v", got)
	}

	// A project factory default outranks the agent fallback for the field it
	// sets, and the agent fallback still fills the fields it leaves unset.
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: projectWorkspace,
		Routing:   fallback,
		FactoryDefaults: &FactoryDefaults{
			Workspace:          "ws://published/factory",
			ImplementerRouting: explicit,
		},
	})
	req := f.handoff("override-factory-first", f.root)
	req.Config.Native = false
	got = f.resolve(t, req)
	if got.Workspace != "ws://published/factory" {
		t.Fatalf("workspace = %q, want the factory default %q", got.Workspace, "ws://published/factory")
	}
	if !bytes.Equal(got.ImplementerRouting, explicit) {
		t.Fatalf("implementer routing = %q, want the factory default %q", got.ImplementerRouting, explicit)
	}
	if !bytes.Equal(got.ReviewerRouting, fallback) {
		t.Fatalf("reviewer routing = %q, want the project agent fallback %q", got.ReviewerRouting, fallback)
	}

	// The upper level alone still gives the handoff the last word, so the two
	// steps are independent of the API around them.
	merged := factoryMergeDefaults(
		FactoryConfig{Workspace: "ws://handoff/explicit", ReviewerRouting: explicit},
		&FactoryDefaults{Workspace: "ws://published/factory", ImplementerRouting: explicit},
	)
	if merged.Workspace != "ws://handoff/explicit" || !bytes.Equal(merged.ImplementerRouting, explicit) || !bytes.Equal(merged.ReviewerRouting, explicit) {
		t.Fatalf("factory defaults overrode an explicit handoff: %+v", merged)
	}
}

// TestFactoryNativeSuppressesAgentWorkspace pins the one direction the
// suppression runs in: an explicit native handoff wins over the project's agent
// workspace without becoming a conflict, and the backend never infers native
// from an empty project workspace.
func TestFactoryNativeSuppressesAgentWorkspace(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	projectWorkspace := "example"
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: projectWorkspace,
		Routing:   json.RawMessage(`{"profile":"chosen"}`),
	})

	// A native handoff suppresses the fallback rather than conflicting with
	// it: the conflict rule covers only a workspace a project default states.
	native := f.handoff("native-suppresses", f.root)
	resolved := f.resolve(t, native)
	if !resolved.Native || resolved.Workspace != "" {
		t.Fatalf("native handoff resolved to native=%v workspace=%q, want true and empty", resolved.Native, resolved.Workspace)
	}
	create := native
	create.RequestID = "native-suppresses-create"
	create.Config.TargetBranch = "main"
	created := f.created(t, create)
	if created.Draft.Config.Workspace != "" {
		t.Fatalf("workspace = %q, want none", created.Draft.Config.Workspace)
	}
	assertFactoryRoutes(t, created.Draft.Config, `{"profile":"chosen"}`)

	// Without native the same project supplies the workspace.
	plain := native
	plain.Config.Native = false
	if got := f.resolve(t, plain).Workspace; got != projectWorkspace {
		t.Fatalf("workspace = %q, want the project agent workspace %q", got, projectWorkspace)
	}

	// The suppression is one-directional. A project with no workspace leaves a
	// non-native handoff non-native, so preflight still rejects it instead of
	// the backend choosing native on the operator's behalf.
	empty := t.TempDir()
	if err := f.fleet.ws.AddProject(empty); err != nil {
		t.Fatal(err)
	}
	req := f.handoff("native-not-inferred", empty)
	req.Config.Native = false
	req.Config.TargetBranch = "main"
	resolved = f.resolve(t, req)
	if resolved.Native || resolved.Workspace != "" {
		t.Fatalf("empty project resolved to native=%v workspace=%q, want neither", resolved.Native, resolved.Workspace)
	}
	rec := f.create(t, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-native handoff without a workspace: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "select native or a published workspace") {
		t.Fatalf("error does not name the missing choice: %s", rec.Body.String())
	}
	if items := f.store.List(); len(items) != 1 {
		t.Fatalf("work items = %d, want only the earlier native create", len(items))
	}
}

// TestFactoryReplayAfterAgentDefaultsChange pins the durability side of the
// lower level: a replay returns the original draft even though the settings
// resolution reads have changed, a new request id is resolved against them, and
// a retry replacement stays literal.
func TestFactoryReplayAfterAgentDefaultsChange(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.addProfile(t, "later")
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: "first",
		Routing:   json.RawMessage(`{"profile":"chosen"}`),
	})
	req := f.handoff("replay-agent-defaults", f.root)
	req.Config.TargetBranch = "main"
	original := f.created(t, req)
	assertFactoryRoutes(t, original.Draft.Config, `{"profile":"chosen"}`)

	// Move both project agent defaults between the request and its replay.
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: "second",
		Routing:   json.RawMessage(`{"profile":"later"}`),
	})

	// The same request id and payload returns the original draft: replay
	// identity is the request the operator sent, never the settings resolution
	// reads.
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
	assertFactoryRoutes(t, body.Item.Draft.Config, `{"profile":"chosen"}`)
	stored, err := f.store.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertFactoryRoutes(t, stored.Draft.Config, `{"profile":"chosen"}`)

	// A request the journal has not seen is resolved against the new settings,
	// which proves the change took effect.
	fresh := f.handoff("replay-agent-defaults-fresh", f.root)
	fresh.Config.TargetBranch = "main"
	assertFactoryRoutes(t, f.created(t, fresh).Draft.Config, `{"profile":"later"}`)

	// The retry path stays unchanged: a replacement is an authored
	// configuration and remains literal, so the project's new profile is not
	// applied to a route the replacement states.
	parked, err := f.store.Update(original.ID, "park-for-question", original.Version, json.RawMessage(`{"decision":"question"}`), func(dst *FactoryItem) error {
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
			TargetBranch:             "main",
			Native:                   true,
			ImplementerRouting:       json.RawMessage(`{"profile":"chosen"}`),
			ReviewerRouting:          json.RawMessage(`{"profile":"chosen"}`),
			NoRequiredCIAcknowledged: true,
		},
	}
	got, err := f.api.Command(context.Background(), parked.ID, FactoryCommand{
		RequestID:        "retry-agent-defaults",
		Kind:             "retry",
		DecisionID:       "q",
		Reason:           "restart with a replacement",
		ExpectedVersion:  parked.Version,
		AdditionalRounds: 1,
		Replacement:      &replacement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want the replacement's single approval", got.Approvals)
	}
	assertFactoryRoutes(t, got.Approvals[0].Config, `{"profile":"chosen"}`)
}

// TestFactoryDefaultsEndToEnd drives the defaults chain through the served API
// alone: it writes a project's defaults with the same endpoint an operator
// uses, creates a handoff that omits every inheritable field, changes the
// defaults, creates a second handoff, and reads both drafts back as the
// snapshots their own create resolved. The three fields resolution contributes
// are each exercised the same way: an explicit role override on a handoff, the
// native choice suppressing the project's agent workspace, and an explicitly
// empty check list approved with the acknowledgement that stands in for
// required checks.
func TestFactoryDefaultsEndToEnd(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	f.addProfile(t, "later")

	// The first set of project defaults: a routing profile both roles follow,
	// an agent workspace the native choice suppresses, and a target branch the
	// handoff never states.
	f.putSettings(t, f.root, ProjectSettings{
		Workspace:       "project-one",
		Routing:         json.RawMessage(`{"profile":"chosen"}`),
		FactoryDefaults: &FactoryDefaults{TargetBranch: "main"},
	})
	// The workspace really is in effect, so the empty workspace on the draft
	// below is the native choice suppressing it rather than nothing being set.
	served := doReq(t, f.srv, http.MethodGet, "/api/projects/settings?root="+url.QueryEscape(f.root), nil, f.headers())
	if served.Code != http.StatusOK {
		t.Fatalf("get project settings: status %d, body %s", served.Code, served.Body.String())
	}
	var serving ProjectSettings
	decodeBody(t, served, &serving)
	if serving.Workspace != "project-one" {
		t.Fatalf("project workspace = %q, want %q", serving.Workspace, "project-one")
	}

	firstWant := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              f.root,
		HostID:                   "local",
		TargetBranch:             "main",
		Native:                   true,
		ImplementerRouting:       json.RawMessage(`{"profile":"chosen"}`),
		ReviewerRouting:          json.RawMessage(`{"profile":"chosen"}`),
		NoRequiredCIAcknowledged: true,
		MaxRounds:                5,
		CIPollSeconds:            15,
		CIWaitSeconds:            1800,
	}
	first := f.created(t, f.handoff("end-to-end-1", f.root))
	if first.State != "draft" {
		t.Fatalf("state = %q, want draft", first.State)
	}
	if first.Draft.Config.Workspace != "" {
		t.Fatalf("workspace = %q, want none for a native handoff", first.Draft.Config.Workspace)
	}
	sameFactoryConfig(t, first.Draft.Config, firstWant)

	// Move the project's defaults between the two creates: a second profile,
	// a second agent workspace, a different target branch and an inherited
	// check list the first set did not have.
	f.putSettings(t, f.root, ProjectSettings{
		Workspace: "project-two",
		Routing:   json.RawMessage(`{"profile":"later"}`),
		FactoryDefaults: &FactoryDefaults{
			TargetBranch:   "trunk",
			ExpectedChecks: []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		},
	})
	secondWant := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              f.root,
		HostID:                   "local",
		TargetBranch:             "trunk",
		Native:                   true,
		ImplementerRouting:       json.RawMessage(`{"profile":"later"}`),
		ReviewerRouting:          json.RawMessage(`{"profile":"later"}`),
		ExpectedChecks:           []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		NoRequiredCIAcknowledged: true,
		MaxRounds:                5,
		CIPollSeconds:            15,
		CIWaitSeconds:            1800,
	}
	second := f.created(t, f.handoff("end-to-end-2", f.root))
	if second.Draft.Config.Workspace != "" {
		t.Fatalf("workspace = %q, want none for a native handoff", second.Draft.Config.Workspace)
	}
	sameFactoryConfig(t, second.Draft.Config, secondWant)

	// The two snapshots differ because the defaults changed in between, and
	// the first draft keeps the set its own create resolved against.
	firstJSON, err := json.Marshal(first.Draft.Config)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second.Draft.Config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("both drafts resolved to the same snapshot after the defaults changed: %s", firstJSON)
	}
	reread := doReq(t, f.srv, http.MethodGet, "/api/factory/items/"+first.ID, nil, f.headers())
	if reread.Code != http.StatusOK {
		t.Fatalf("get draft: status %d, body %s", reread.Code, reread.Body.String())
	}
	var rereadBody struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, reread, &rereadBody)
	sameFactoryConfig(t, rereadBody.Item.Draft.Config, firstWant)

	// A handoff that states one role's route outranks both project levels for
	// that role alone: the reviewer still follows the project's profile.
	override := f.handoff("end-to-end-3", f.root)
	override.Config.ImplementerRouting = json.RawMessage(`{"profile":"chosen"}`)
	overrideWant := secondWant
	overrideWant.ImplementerRouting = json.RawMessage(`{"profile":"chosen"}`)
	overridden := f.created(t, override)
	sameFactoryConfig(t, overridden.Draft.Config, overrideWant)

	// An explicitly empty check list is a choice, not an omission: it replaces
	// the inherited list, and the acknowledgement is what lets the draft be
	// approved. Both are exercised through the served endpoints.
	empty := f.handoff("end-to-end-4", f.root)
	empty.Config.ExpectedChecks = []FactoryCheckKey{}
	empty.Config.NoRequiredCIAcknowledged = true
	emptyWant := secondWant
	emptyWant.ExpectedChecks = []FactoryCheckKey{}
	emptyDraft := f.created(t, empty)
	sameFactoryConfig(t, emptyDraft.Draft.Config, emptyWant)

	approved := doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+emptyDraft.ID+"/commands",
		FactoryCommand{RequestID: "end-to-end-approve", Kind: "approve", ExpectedVersion: 1}, f.headers())
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", approved.Code, approved.Body.String())
	}
	var approvalBody struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, approved, &approvalBody)
	if len(approvalBody.Item.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want one", approvalBody.Item.Approvals)
	}
	snapshot := approvalBody.Item.Approvals[0].Config
	if snapshot.ExpectedChecks == nil {
		t.Fatal("explicitly empty expectedChecks became nil in the approval snapshot")
	}
	if len(snapshot.ExpectedChecks) != 0 {
		t.Fatalf("expectedChecks = %+v, want an empty list", snapshot.ExpectedChecks)
	}
	if !snapshot.NoRequiredCIAcknowledged {
		t.Fatal("approval snapshot dropped the no-required-CI acknowledgement")
	}
	sameFactoryConfig(t, snapshot, emptyWant)
}
