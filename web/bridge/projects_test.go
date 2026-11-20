package bridge

import (
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

// projectEnv is a fleet with one registered project and a server.
func projectEnv(t *testing.T) (*wsSpawnEnv, *Server, string) {
	t.Helper()
	e := newWSSpawnEnv(t)
	root := t.TempDir()
	if err := e.f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	return e, NewServer(e.f, ""), root
}

func putSettings(t *testing.T, s *Server, root string, body any) int {
	t.Helper()
	return doReq(t, s, http.MethodPut, "/api/projects/settings?root="+root, body, nil).Code
}

func TestProjectSettingsRoundTripAndValidation(t *testing.T) {
	e, s, root := projectEnv(t)
	e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID})

	yes := true
	good := ProjectSettings{
		Workspace: "svc@2", Mode: "plan", Isolated: &yes, ShipTarget: "patch",
		Routing: json.RawMessage(`{"profile":"fast"}`),
		Intake:  ProjectIntake{RepoID: "r1", Labels: []string{"marshal", "other"}},
	}
	if code := putSettings(t, s, root, good); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	rec := doReq(t, s, http.MethodGet, "/api/projects/settings?root="+root, nil, nil)
	var got ProjectSettings
	decodeBody(t, rec, &got)
	if got.Workspace != "svc@2" || got.Mode != "ask" || got.ShipTarget != "patch" || got.Isolated == nil || !*got.Isolated || len(got.Intake.Labels) != 2 {
		t.Fatalf("settings = %+v", got)
	}

	bad := map[string]ProjectSettings{
		"workspace":  {Workspace: "Bad Name"},
		"mode":       {Mode: "yolo"},
		"shipTarget": {ShipTarget: "ship"},
		"repo":       {Intake: ProjectIntake{RepoID: "nope"}},
		"labels":     {Intake: ProjectIntake{Labels: []string{"x"}}},
		"routing":    {Routing: json.RawMessage(`[1]`)},
		// Planner delegation names one client: enabling it with nobody
		// designated is the state the Studio refuses to save, and a
		// designation must name a client this bridge has.
		"delegation without a client": {PlannerDelegationEnabled: true},
		"delegation to a stranger":    {DesignatedMCPClientID: "nope"},
	}
	for name, ps := range bad {
		if code := putSettings(t, s, root, ps); code != http.StatusBadRequest {
			t.Errorf("%s: put = %d, want 400", name, code)
		}
	}
	// A refused request leaves the stored settings alone.
	if st := e.f.ws.ProjectSettingsFor(root); st.Workspace != "svc@2" {
		t.Fatalf("settings changed by a refused request: %+v", st)
	}
	// Delegation to a registered client is a savable state, and it round-trips.
	if err := e.f.ws.PutClient(MCPClient{ID: "c1", Name: "claude", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	delegated := good
	delegated.PlannerDelegationEnabled = true
	delegated.DesignatedMCPClientID = "c1"
	if code := putSettings(t, s, root, delegated); code != http.StatusOK {
		t.Fatalf("delegation to a registered client = %d", code)
	}
	if st := e.f.ws.ProjectSettingsFor(root); !st.PlannerDelegationEnabled || st.DesignatedMCPClientID != "c1" {
		t.Fatalf("delegation stored as %+v", st)
	}
	// A client that goes away takes its designation with it, so a project is
	// never left unable to save its own settings.
	if err := e.f.ws.DeleteClient("c1"); err != nil {
		t.Fatal(err)
	}
	if st := e.f.ws.ProjectSettingsFor(root); st.PlannerDelegationEnabled || st.DesignatedMCPClientID != "" {
		t.Fatalf("a deleted client kept its designation: %+v", st)
	}
	if code := putSettings(t, s, "/not/registered", good); code != http.StatusBadRequest {
		t.Fatalf("unregistered root = %d", code)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/projects/settings?root=/not/registered", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("get unregistered = %d", rec.Code)
	}
	tail, _ := e.f.audit.Tail(10)
	found := false
	for _, ev := range tail {
		found = found || ev.Event == AuditProjectSettings
	}
	if !found {
		t.Fatal("no project_settings audit entry")
	}
}

// TestProjectInteractionDefaultsRejectInvalidTuple checks that project defaults
// accept only a coherent starting tuple: an unknown mode is reported rather
// than promoted, Ask cannot carry Auto, a retired spelling is stored canonical,
// and an explicit Auto false stays a real value.
func TestProjectInteractionDefaultsRejectInvalidTuple(t *testing.T) {
	e, _, root := projectEnv(t)

	for name, ps := range map[string]ProjectSettings{
		"unknown mode":  {Mode: "yolo"},
		"ask with auto": {Mode: "ask", Auto: boolPtr(true)},
	} {
		if err := e.f.PutProjectSettings(root, ps); !errors.Is(err, errInvalidProjectSettings) {
			t.Errorf("%s: err = %v, want errInvalidProjectSettings", name, err)
		}
	}

	// A retired spelling is accepted and stored as its canonical pair.
	if err := e.f.PutProjectSettings(root, ProjectSettings{Mode: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Mode != "act" || got.Auto != nil {
		t.Fatalf("copilot stored as %+v, want act with no auto", got)
	}

	// An explicit false is a real value, not an absent one.
	if err := e.f.PutProjectSettings(root, ProjectSettings{Mode: "act", Auto: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Auto == nil || *got.Auto {
		t.Fatalf("auto = %v, want an explicit false", got.Auto)
	}
}

func TestProjectSettingsMirrorTheIntakeLabelOntoTheWatcher(t *testing.T) {
	e, s, root := projectEnv(t)
	e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID})

	putSettings(t, s, root, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"marshal", "second"}}})
	r, _ := e.f.ws.Repo("r1")
	if !r.Watch || r.WatchLabel != "marshal" {
		t.Fatalf("repo = %+v, want the first label watched", r)
	}
	putSettings(t, s, root, ProjectSettings{Intake: ProjectIntake{RepoID: "r1"}})
	r, _ = e.f.ws.Repo("r1")
	if r.Watch || r.WatchLabel != "" {
		t.Fatalf("repo = %+v, want the watcher off", r)
	}
	// Unrelated changes leave a hand-set watcher alone.
	e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID, Watch: true, WatchLabel: "manual"})
	putSettings(t, s, root, ProjectSettings{Intake: ProjectIntake{RepoID: "r1"}, Mode: "plan"})
	r, _ = e.f.ws.Repo("r1")
	if !r.Watch || r.WatchLabel != "manual" {
		t.Fatalf("repo = %+v", r)
	}
}

func TestProjectHealth(t *testing.T) {
	e, s, root := projectEnv(t)
	f := e.f
	fakeHome(t)
	url := "https://example.com/r1.git"
	f.ws.PutRepo(Repo{ID: "r1", URL: url, OwnerID: DefaultOwnerID})
	publishDoc(t, f, "svc", sampleDoc("svc"))
	putSettings(t, s, root, ProjectSettings{Workspace: "svc", Intake: ProjectIntake{RepoID: "r1"}})

	health := func() ProjectHealth {
		var h ProjectHealth
		rec := doReq(t, s, http.MethodGet, "/api/projects/health?root="+root, nil, nil)
		if rec.Code != 200 {
			t.Fatalf("health = %d %s", rec.Code, rec.Body)
		}
		decodeBody(t, rec, &h)
		return h
	}

	h := health()
	if h.GateRunnable != "unknown" || h.Trust != "na" || len(h.MirrorFresh) != 1 || h.MirrorFresh[0].Present {
		t.Fatalf("health = %+v", h)
	}
	if h.WorkspaceResolves == nil || !h.WorkspaceResolves.Resolves || h.WorkspaceResolves.Built {
		t.Fatalf("workspace health = %+v", h.WorkspaceResolves)
	}

	// A built workspace, a mirror fetched an hour ago, a runnable gate.
	f.templates.SetBuild("svc", 1, "ok", "tag", 1, 1)
	dir := mirrorDir(f.stateDir, url)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	hour := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "HEAD"), hour, hour)
	f.ws.PutAgent(Agent{ID: "a1", Project: root, SourceKind: "local"})
	f.storeGate("a1", &gateResult{OK: false, FailedCommand: "go test"})

	h = health()
	if h.GateRunnable != "yes" || !h.WorkspaceResolves.Built {
		t.Fatalf("health = %+v", h)
	}
	if m := h.MirrorFresh[0]; !m.Present || m.AgeSeconds < 3500 || m.AgeSeconds > 3700 {
		t.Fatalf("mirror = %+v", m)
	}

	// A skipped verify means the gate has nothing to run.
	f.storeGate("a1", &gateResult{Skipped: true})
	if h = health(); h.GateRunnable != "no" {
		t.Fatalf("gateRunnable = %q", h.GateRunnable)
	}

	// A workspace that no longer resolves is reported, not an error.
	f.templates.Delete("svc", nil)
	if h = health(); h.WorkspaceResolves == nil || h.WorkspaceResolves.Resolves || h.WorkspaceResolves.Error == "" {
		t.Fatalf("workspace health = %+v", h.WorkspaceResolves)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/projects/health?root=/nope", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("health of an unregistered root = %d", rec.Code)
	}
}

func TestSpawnPicksUpTheProjectDefaults(t *testing.T) {
	e, s, root := projectEnv(t)
	e.builtTemplate(t, "svc", sampleDoc("svc"))
	yes := true
	putSettings(t, s, root, ProjectSettings{Workspace: "svc", Mode: "plan", Isolated: &yes, Routing: json.RawMessage(`{"profile":"fast"}`)})

	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]string{"project": root}, nil)
	var out struct {
		AgentID string `json:"agentId"`
	}
	decodeBody(t, rec, &out)
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn = %d %s", rec.Code, rec.Body)
	}
	a, _ := e.f.ws.Agent(out.AgentID)
	if a.Workspace == nil || a.Workspace.Name != "svc" {
		t.Fatalf("workspace default not applied: %+v", a.Workspace)
	}
	calls := e.agents[out.AgentID].calls("session/new")
	if len(calls) != 1 || !strings.Contains(calls[0], `"isolation"`) || !strings.Contains(calls[0], `"routing":{"profile":"fast"}`) {
		t.Fatalf("session/new = %v", calls)
	}
	// The project default travels as the session's starting policy; there is
	// no second implicit set_mode patch.
	if !strings.Contains(calls[0], `"policy":{"mode":"ask"}`) {
		t.Fatalf("session/new policy = %v, want the project's ask default", calls[0])
	}
	if got := e.agents[out.AgentID].calls("session/set_mode"); len(got) != 0 {
		t.Fatalf("session/set_mode = %v, want no implicit patch", got)
	}

	// Explicit request values win over the defaults.
	rec = doReq(t, s, http.MethodPost, "/api/agents", map[string]any{"project": root, "mode": "auto", "isolated": false, "workspace": "svc@1"}, nil)
	decodeBody(t, rec, &out)
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn = %d %s", rec.Code, rec.Body)
	}
	calls = e.agents[out.AgentID].calls("session/new")
	if strings.Contains(calls[0], `"isolation"`) {
		t.Fatalf("an explicit isolated:false was overridden: %v", calls)
	}
	// The retired "auto" spelling is canonicalized to Act + Auto at the one
	// policy the spawn sends.
	if !strings.Contains(calls[0], `"policy":{"auto":true,"mode":"act"}`) {
		t.Fatalf("session/new policy = %v, want act + auto", calls[0])
	}
	if got := e.agents[out.AgentID].calls("session/set_mode"); len(got) != 0 {
		t.Fatalf("session/set_mode = %v, want no implicit patch", got)
	}
}

func TestShipTargetOverridesTheDerivedDestinationOnlyWhenValid(t *testing.T) {
	e, s, root := projectEnv(t)
	f := e.f
	f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID})

	local := Agent{ID: "l", Project: root, SourceKind: "local"}
	gitAgent := Agent{ID: "g", Project: "/work/g", SourceKind: "git", SourceRef: "r1"}
	readOnly := Agent{ID: "ro", Project: "/work/ro", SourceKind: "git", SourceRef: "https://x/y.git", ReadOnly: true}

	put := func(target string) {
		t.Helper()
		if code := putSettings(t, s, root, ProjectSettings{ShipTarget: target, Intake: ProjectIntake{RepoID: "r1"}}); code != 200 {
			t.Fatalf("put %q = %d", target, code)
		}
	}
	for _, c := range []struct {
		target string
		want   [3]string // local, git, readOnly
	}{
		{"", [3]string{"merge", "push", "patch"}},
		{"merge", [3]string{"merge", "push", "patch"}},
		{"push", [3]string{"merge", "push", "patch"}},
		{"patch", [3]string{"merge", "patch", "patch"}},
	} {
		put(c.target)
		got := [3]string{f.shipDestination(local), f.shipDestination(gitAgent), f.shipDestination(readOnly)}
		if got != c.want {
			t.Errorf("shipTarget %q: destinations = %v, want %v", c.target, got, c.want)
		}
	}
	if code := putSettings(t, s, root, ProjectSettings{ShipTarget: "teleport"}); code != http.StatusBadRequest {
		t.Fatalf("an invalid ship target = %d, want 400", code)
	}
}

func TestExitHonoursAValidShipTarget(t *testing.T) {
	e, s, root := projectEnv(t)
	e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID})
	e.f.ws.PutAgent(Agent{ID: "g", Project: "/work/g", SourceKind: "git", SourceRef: "r1"})
	putSettings(t, s, root, ProjectSettings{ShipTarget: "patch", Intake: ProjectIntake{RepoID: "r1"}})
	res, err := e.f.Exit(ctlContext(t), "g", ExitOptions{})
	if err != nil || res.Destination != "patch" {
		t.Fatalf("Exit = %+v, %v; want a patch destination without pushing", res, err)
	}
}

// TestProjectSettingsRoundTripFactoryDefaults pins the project's factory
// defaults to the existing project-settings surface: a block written through
// PUT comes back unchanged from GET, and because the field is optional the
// workspace file keeps its version.
func TestProjectSettingsRoundTripFactoryDefaults(t *testing.T) {
	e, s, root := projectEnv(t)

	want := &FactoryDefaults{
		TargetBranch:         "main",
		Workspace:            "ws://published/example",
		HostID:               "local",
		ImplementerRouting:   json.RawMessage(`{"profile":"frontier"}`),
		ReviewerRouting:      json.RawMessage(`{"profile":"frontier"}`),
		ReviewerInstructions: "be terse",
		ExpectedChecks:       []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		MaxRounds:            5,
		CIPollSeconds:        15,
		CIWaitSeconds:        1800,
	}
	if code := putSettings(t, s, root, ProjectSettings{FactoryDefaults: want}); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}

	rec := doReq(t, s, http.MethodGet, "/api/projects/settings?root="+root, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	var got ProjectSettings
	decodeBody(t, rec, &got)
	if got.FactoryDefaults == nil {
		t.Fatal("factoryDefaults did not survive the round trip")
	}
	gd := got.FactoryDefaults
	if gd.TargetBranch != want.TargetBranch || gd.Workspace != want.Workspace || gd.HostID != want.HostID {
		t.Fatalf("defaults = %+v", gd)
	}
	if string(gd.ImplementerRouting) != string(want.ImplementerRouting) || string(gd.ReviewerRouting) != string(want.ReviewerRouting) {
		t.Fatalf("routings = %s / %s", gd.ImplementerRouting, gd.ReviewerRouting)
	}
	if gd.ReviewerInstructions != want.ReviewerInstructions {
		t.Fatalf("reviewerInstructions = %q", gd.ReviewerInstructions)
	}
	if len(gd.ExpectedChecks) != 1 || gd.ExpectedChecks[0] != want.ExpectedChecks[0] {
		t.Fatalf("expectedChecks = %+v", gd.ExpectedChecks)
	}
	if gd.MaxRounds != 5 || gd.CIPollSeconds != 15 || gd.CIWaitSeconds != 1800 {
		t.Fatalf("limits = %d/%d/%d", gd.MaxRounds, gd.CIPollSeconds, gd.CIWaitSeconds)
	}

	// The optional field leaves the on-disk format alone.
	raw, err := os.ReadFile(e.f.ws.path)
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	var stored struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode workspace file: %v", err)
	}
	if stored.Version != workspaceVersion {
		t.Fatalf("workspace version = %d, want %d", stored.Version, workspaceVersion)
	}
}

// TestFactoryDefaultsRejectInvalidDefaultsAtWriteTime checks the write-time
// validation the settings surface owns: every invalid default is a 400 naming
// the offending field, the accepted boundary cases (a zero limit, "local", and
// nil or explicitly empty checks) are stored, and a rejected write leaves the
// stored record untouched.
func TestFactoryDefaultsRejectInvalidDefaultsAtWriteTime(t *testing.T) {
	e, s, root := projectEnv(t)

	// A stored record that a refused write must not disturb.
	good := ProjectSettings{
		Workspace:       "svc@2",
		FactoryDefaults: &FactoryDefaults{ReviewerInstructions: "keep", MaxRounds: 5},
	}
	if code := putSettings(t, s, root, good); code != http.StatusOK {
		t.Fatalf("put good = %d", code)
	}

	bad := []struct {
		name  string
		d     *FactoryDefaults
		field string
	}{
		{"negative maxRounds", &FactoryDefaults{MaxRounds: -1}, "maxRounds"},
		{"negative ciPollSeconds", &FactoryDefaults{CIPollSeconds: -1}, "ciPollSeconds"},
		{"negative ciWaitSeconds", &FactoryDefaults{CIWaitSeconds: -1}, "ciWaitSeconds"},
		{"non-local hostId", &FactoryDefaults{HostID: "remote"}, "hostId"},
		{"check without a name", &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "check_run", AppID: 7}}}, "expectedChecks"},
		{"unknown check kind", &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "workflow", Name: "build"}}}, "expectedChecks"},
		{"check_run without an app id", &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "check_run", Name: "build"}}}, "expectedChecks"},
		{"commit_status with an app id", &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "commit_status", Name: "ci", AppID: 7}}}, "expectedChecks"},
	}
	for _, c := range bad {
		// The rest of the record would overwrite the stored one, so storing
		// anything at all proves the write was refused before it landed.
		body := ProjectSettings{Workspace: "overwritten", FactoryDefaults: c.d}
		rec := doReq(t, s, http.MethodPut, "/api/projects/settings?root="+root, body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: put = %d, want 400", c.name, rec.Code)
			continue
		}
		var out struct {
			Error string `json:"error"`
		}
		decodeBody(t, rec, &out)
		if !strings.Contains(out.Error, c.field) {
			t.Errorf("%s: error %q does not name %q", c.name, out.Error, c.field)
		}
	}
	if st := e.f.ws.ProjectSettingsFor(root); st.Workspace != "svc@2" || st.FactoryDefaults == nil || st.FactoryDefaults.ReviewerInstructions != "keep" || st.FactoryDefaults.MaxRounds != 5 {
		t.Fatalf("settings changed by a refused request: %+v", st)
	}

	// The boundary cases are real values, not invalid ones.
	accepted := []struct {
		name string
		body json.RawMessage
	}{
		{"zero limits", json.RawMessage(`{"factoryDefaults":{"maxRounds":0,"ciPollSeconds":0,"ciWaitSeconds":0}}`)},
		{"local host", json.RawMessage(`{"factoryDefaults":{"hostId":"local"}}`)},
		{"nil checks", json.RawMessage(`{"factoryDefaults":{"reviewerInstructions":"no checks"}}`)},
		{"explicitly empty checks", json.RawMessage(`{"factoryDefaults":{"expectedChecks":[]}}`)},
	}
	for _, c := range accepted {
		if code := putSettings(t, s, root, c.body); code != http.StatusOK {
			t.Errorf("%s: put = %d, want 200", c.name, code)
		}
	}
	// An explicitly empty check list is a choice, so it is stored non-nil.
	if st := e.f.ws.ProjectSettingsFor(root); st.FactoryDefaults == nil || st.FactoryDefaults.ExpectedChecks == nil || len(st.FactoryDefaults.ExpectedChecks) != 0 {
		t.Fatalf("explicitly empty expectedChecks = %+v", st.FactoryDefaults)
	}
}

// projectSettingsTag returns the ETag a settings GET carries and the settings
// it describes.
func projectSettingsTag(t *testing.T, s *Server, root string) (string, ProjectSettings) {
	t.Helper()
	rec := doReq(t, s, http.MethodGet, "/api/projects/settings?root="+root, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings = %d %s", rec.Code, rec.Body)
	}
	var got ProjectSettings
	decodeBody(t, rec, &got)
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("a settings GET carries no ETag")
	}
	return tag, got
}

// TestProjectSettingsETagRoundTrip checks the version token is what the
// contract says: a quoted SHA-256 of the typed settings, returned by GET and by
// every successful PUT, naming the value that was written rather than a later
// read.
func TestProjectSettingsETagRoundTrip(t *testing.T) {
	e, s, root := projectEnv(t)
	path := "/api/projects/settings?root=" + root

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2", Mode: "ask"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	tag := rec.Header().Get("ETag")
	if len(tag) != 66 || tag[0] != '"' || tag[65] != '"' {
		t.Fatalf("ETag = %q, want one quoted tag", tag)
	}
	for _, c := range tag[1:65] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("ETag %q is not lowercase hex", tag)
		}
	}
	want, err := projectSettingsETag(e.f.ws.ProjectSettingsFor(root))
	if err != nil {
		t.Fatal(err)
	}
	if tag != want {
		t.Fatalf("ETag = %q, want %q for the stored value", tag, want)
	}
	var stored ProjectSettings
	decodeBody(t, rec, &stored)
	if stored.Workspace != "svc@2" || stored.Mode != "ask" {
		t.Fatalf("response = %+v", stored)
	}
	// GET agrees with the write: same tag, same body.
	if gotTag, got := projectSettingsTag(t, s, root); gotTag != tag || got.Workspace != stored.Workspace || got.Mode != stored.Mode {
		t.Fatalf("GET = %q %+v, want %q %+v", gotTag, got, tag, stored)
	}

	// A conditional PUT naming the tag it read applies, and answers with the
	// tag of the value it stored.
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2", Mode: "act"}, map[string]string{"If-Match": tag})
	if rec.Code != http.StatusOK {
		t.Fatalf("conditional put = %d %s", rec.Code, rec.Body)
	}
	next := rec.Header().Get("ETag")
	if next == tag {
		t.Fatalf("the ETag did not follow the value: %q", next)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Mode != "act" {
		t.Fatalf("stored = %+v, want the conditional write", got)
	}
	want, err = projectSettingsETag(e.f.ws.ProjectSettingsFor(root))
	if err != nil {
		t.Fatal(err)
	}
	if next != want {
		t.Fatalf("conditional ETag = %q, want %q", next, want)
	}
	if gotTag, _ := projectSettingsTag(t, s, root); gotTag != next {
		t.Fatalf("GET after the conditional write = %q, want %q", gotTag, next)
	}

	// Rewriting the same value keeps the tag: it names the content.
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2", Mode: "act"}, map[string]string{"If-Match": next})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != next {
		t.Fatalf("idempotent conditional put = %d %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// TestProjectSettingsConditionalConflict checks that a write naming a version
// the record has moved past is a 412 that changes nothing, and that a malformed
// If-Match is a caller mistake (400), never a conflict.
func TestProjectSettingsConditionalConflict(t *testing.T) {
	e, s, root := projectEnv(t)
	if err := e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/settings?root=" + root
	audits := func() int {
		t.Helper()
		tail, err := e.f.audit.Tail(200)
		if err != nil {
			t.Fatalf("audit tail: %v", err)
		}
		n := 0
		for _, ev := range tail {
			if ev.Event == AuditProjectSettings {
				n++
			}
		}
		return n
	}

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"marshal"}}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	stale := rec.Header().Get("ETag")

	// Another writer moves the record on, so the tag above is stale.
	if code := putSettings(t, s, root, ProjectSettings{Mode: "ask", Intake: ProjectIntake{RepoID: "r1", Labels: []string{"other"}}}); code != http.StatusOK {
		t.Fatalf("intervening write = %d", code)
	}
	before := audits()

	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"third"}}}, map[string]string{"If-Match": stale})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale conditional put = %d %s, want 412", rec.Code, rec.Body)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Mode != "ask" || len(got.Intake.Labels) != 1 || got.Intake.Labels[0] != "other" {
		t.Fatalf("a refused write changed the record: %+v", got)
	}
	if r, _ := e.f.ws.Repo("r1"); r.WatchLabel != "other" {
		t.Fatalf("repo = %+v, want the watcher on the stored label", r)
	}
	if after := audits(); after != before {
		t.Fatalf("a refused write was audited: %d -> %d", before, after)
	}

	// Every malformed If-Match is the caller's mistake: 400, not 412.
	current, _ := projectSettingsTag(t, s, root)
	for name, header := range map[string]string{
		"wildcard": "*",
		"unquoted": strings.Trim(current, `"`),
		"list":     current + `, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`,
		"short":    `"deadbeef"`,
		"weak":     "W/" + current,
	} {
		rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2"}, map[string]string{"If-Match": header})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: put = %d, want 400", name, rec.Code)
		}
	}
	// Two header lines are two tags, which the bridge refuses to choose
	// between.
	req := testJSONRequest(http.MethodPut, path, `{"workspace":"svc@2"}`)
	req.Header.Add("If-Match", current)
	req.Header.Add("If-Match", current)
	twin := httptest.NewRecorder()
	s.ServeHTTP(twin, req)
	if twin.Code != http.StatusBadRequest {
		t.Fatalf("two If-Match headers = %d, want 400", twin.Code)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Workspace != "" {
		t.Fatalf("a malformed If-Match wrote: %+v", got)
	}

	// A save that fails restores the exact prior value, including for a
	// project that had no record at all.
	t.Run("persistence failure restores the prior value", func(t *testing.T) {
		dir := t.TempDir()
		state := filepath.Join(dir, "state")
		w := NewWorkspace(filepath.Join(state, "fleet.json"))
		prior := ProjectSettings{Workspace: "svc@2"}
		if err := w.PutProjectSettings("/p", prior); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tag, err := projectSettingsETag(prior)
		if err != nil {
			t.Fatal(err)
		}
		// A regular file where the state directory was: every later save
		// fails, whatever the test user's privileges.
		if err := os.RemoveAll(state); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}

		ok, prev, err := w.PutProjectSettingsIfMatch("/p", ProjectSettings{Workspace: "svc@3"}, tag)
		if err == nil {
			t.Fatal("a failed save reported success")
		}
		if ok {
			t.Fatal("ok = true on a failed save")
		}
		if prev.Workspace != "svc@2" {
			t.Fatalf("prev = %+v, want the value it replaced", prev)
		}
		if got := w.ProjectSettingsFor("/p"); got.Workspace != "svc@2" {
			t.Fatalf("after a failed save settings = %+v, want the prior value", got)
		}

		// A project with no stored record gains none.
		empty, err := projectSettingsETag(ProjectSettings{})
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := w.PutProjectSettingsIfMatch("/fresh", ProjectSettings{Workspace: "svc@3"}, empty); err == nil || ok {
			t.Fatalf("a failed first save = ok %v, err %v", ok, err)
		}
		w.mu.Lock()
		_, had := w.projectSettings["/fresh"]
		w.mu.Unlock()
		if had {
			t.Fatal("a failed first save left a record behind")
		}

		// An empty expectation names no version, so it is refused outright.
		if ok, _, err := w.PutProjectSettingsIfMatch("/p", prior, ""); err == nil || ok {
			t.Fatalf("empty If-Match = ok %v, err %v, want a refusal", ok, err)
		}
	})
}

// TestProjectSettingsStaleBodyIsConflictNotInvalid checks that a conditional
// write evaluates its precondition before it validates the body. A writer whose
// view of the record is stale is told its view is stale, not that its body is
// invalid: it is describing a project someone else has since changed, and
// "fix your body and retry" is the wrong instruction. The 412 names the value
// that won, so a client can act on it without a second round trip.
func TestProjectSettingsStaleBodyIsConflictNotInvalid(t *testing.T) {
	e, s, root := projectEnv(t)
	path := "/api/projects/settings?root=" + root

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{ShipTarget: "merge"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	stale := rec.Header().Get("ETag")

	// Another writer moves the record on, so stale no longer names it.
	if code := putSettings(t, s, root, ProjectSettings{ShipTarget: "push"}); code != http.StatusOK {
		t.Fatalf("intervening write = %d", code)
	}
	current, stored := projectSettingsTag(t, s, root)

	// The stale tag wins over the invalid body: 412, not 400.
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{ShipTarget: "bogus"}, map[string]string{"If-Match": stale})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale tag with an invalid body = %d %s, want 412", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != current {
		t.Fatalf("412 ETag = %q, want the stored %q", got, current)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.ShipTarget != stored.ShipTarget {
		t.Fatalf("a refused write changed the record: %+v", got)
	}

	// The same invalid body against the current tag is still the caller's
	// mistake, so the ordering does not weaken validation.
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{ShipTarget: "bogus"}, map[string]string{"If-Match": current})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("current tag with an invalid body = %d %s, want 400", rec.Code, rec.Body)
	}

	// A project that is not registered is a bad request target, not a
	// conflict, whatever tag it names.
	unregistered := path[:strings.LastIndex(path, "=")+1] + root + "-nope"
	rec = doReq(t, s, http.MethodPut, unregistered, ProjectSettings{ShipTarget: "merge"}, map[string]string{"If-Match": stale})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered root = %d %s, want 400", rec.Code, rec.Body)
	}
}

// TestProjectSettingsConcurrentConditionalWrites checks the compare and the
// write are one step: two writers naming the same version cannot both win.
func TestProjectSettingsConcurrentConditionalWrites(t *testing.T) {
	e, s, root := projectEnv(t)
	path := "/api/projects/settings?root=" + root

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	tag := rec.Header().Get("ETag")
	audits := func() int {
		t.Helper()
		tail, err := e.f.audit.Tail(200)
		if err != nil {
			t.Fatalf("audit tail: %v", err)
		}
		n := 0
		for _, ev := range tail {
			if ev.Event == AuditProjectSettings {
				n++
			}
		}
		return n
	}
	before := audits()

	// Two writers, each holding the same tag and a different value.
	const writers = 2
	bodies := make([][]byte, writers)
	for i := range bodies {
		data, err := json.Marshal(ProjectSettings{Workspace: fmt.Sprintf("svc@%d", i+2)})
		if err != nil {
			t.Fatal(err)
		}
		bodies[i] = data
	}
	codes := make(chan int, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := testJSONRequest(http.MethodPut, path, string(bodies[i]))
			req.Header.Set("If-Match", tag)
			rr := httptest.NewRecorder()
			<-start
			s.ServeHTTP(rr, req)
			codes <- rr.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(codes)

	won, lost := 0, 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			won++
		case http.StatusPreconditionFailed:
			lost++
		default:
			t.Errorf("writer = %d, want 200 or 412", code)
		}
	}
	if won != 1 || lost != writers-1 {
		t.Fatalf("%d writers won and %d lost, want exactly one winner", won, lost)
	}
	// The winner's value is the one stored, and only the winner mirrored or
	// audited.
	if got := e.f.ws.ProjectSettingsFor(root); got.Workspace != "svc@2" && got.Workspace != "svc@3" {
		t.Fatalf("stored = %+v, want a writer's value", got)
	}
	if after := audits(); after != before+1 {
		t.Fatalf("audit records = %d, want %d: only the winner audits", after, before+1)
	}
}

// TestProjectSettingsConditionalFailureNoIntakeSideEffect checks that a refused
// conditional write leaves the watcher alone: the intake mirror runs only after
// the store accepted the write.
func TestProjectSettingsConditionalFailureNoIntakeSideEffect(t *testing.T) {
	e, s, root := projectEnv(t)
	if err := e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/settings?root=" + root

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"marshal"}}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	if r, _ := e.f.ws.Repo("r1"); !r.Watch || r.WatchLabel != "marshal" {
		t.Fatalf("repo = %+v, want the label watched", r)
	}
	stale := rec.Header().Get("ETag")

	// The record moves on, then a write naming the version it read before
	// that tries to move the watcher somewhere else.
	if code := putSettings(t, s, root, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"second"}}}); code != http.StatusOK {
		t.Fatalf("intervening write = %d", code)
	}
	before, _ := e.f.audit.Tail(200)
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"third"}}}, map[string]string{"If-Match": stale})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale conditional put = %d %s, want 412", rec.Code, rec.Body)
	}
	if r, _ := e.f.ws.Repo("r1"); !r.Watch || r.WatchLabel != "second" {
		t.Fatalf("repo = %+v, want the watcher untouched by the refused write", r)
	}
	if got := e.f.ws.ProjectSettingsFor(root); len(got.Intake.Labels) != 1 || got.Intake.Labels[0] != "second" {
		t.Fatalf("stored = %+v, want the intervening write", got)
	}
	after, _ := e.f.audit.Tail(200)
	count := func(evs []AuditEvent) int {
		n := 0
		for _, ev := range evs {
			if ev.Event == AuditProjectSettings {
				n++
			}
		}
		return n
	}
	if count(after) != count(before) {
		t.Fatalf("a refused write was audited: %d -> %d", count(before), count(after))
	}
}

// TestProjectSettingsLegacyPut checks a PUT without If-Match keeps the old
// unconditional behaviour, and still hands back a tag the next write can use.
func TestProjectSettingsLegacyPut(t *testing.T) {
	e, s, root := projectEnv(t)
	if err := e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/settings?root=" + root

	rec := doReq(t, s, http.MethodPut, path, ProjectSettings{Intake: ProjectIntake{RepoID: "r1", Labels: []string{"marshal"}}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy put = %d %s", rec.Code, rec.Body)
	}
	var first ProjectSettings
	decodeBody(t, rec, &first)
	if len(first.Intake.Labels) != 1 || first.Intake.Labels[0] != "marshal" {
		t.Fatalf("response = %+v", first)
	}
	if r, _ := e.f.ws.Repo("r1"); !r.Watch || r.WatchLabel != "marshal" {
		t.Fatalf("repo = %+v, want the legacy route to mirror the intake", r)
	}
	found := false
	tail, _ := e.f.audit.Tail(50)
	for _, ev := range tail {
		found = found || ev.Event == AuditProjectSettings
	}
	if !found {
		t.Fatal("the legacy route did not audit the write")
	}

	// No If-Match is still unconditional: it overwrites a record it never read.
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2", Mode: "ask", Intake: ProjectIntake{RepoID: "r1", Labels: []string{"marshal"}}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second legacy put = %d %s", rec.Code, rec.Body)
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.Workspace != "svc@2" || got.Mode != "ask" {
		t.Fatalf("stored = %+v, want the unconditional write", got)
	}

	// A client that skipped the ETag is still answered with one, so its next
	// write can be conditional.
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("a legacy PUT carries no ETag")
	}
	rec = doReq(t, s, http.MethodPut, path, ProjectSettings{Workspace: "svc@2", Mode: "act"}, map[string]string{"If-Match": tag})
	if rec.Code != http.StatusOK {
		t.Fatalf("conditional put after a legacy one = %d %s", rec.Code, rec.Body)
	}
}

// TestProjectsListNamesTheProjectRepo checks the mapping the Studio's new-work
// dialog reads: `GET /api/projects` names the registered repo a project's work
// comes from, because `GET /api/repos/{id}/issues` is keyed by a repo id and a
// dialog starts from a project. A project that names none reports none — the
// key is omitted, not empty — rather than an id a client could not use.
func TestProjectsListNamesTheProjectRepo(t *testing.T) {
	e, s, root := projectEnv(t)
	if err := e.f.ws.PutRepo(Repo{ID: "owner/name", URL: "https://github.com/owner/name.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}

	// entry is one project's wire object from GET /api/projects.
	entry := func(root string) map[string]any {
		t.Helper()
		var list []map[string]any
		decodeBody(t, doReq(t, s, http.MethodGet, "/api/projects", nil, nil), &list)
		for _, item := range list {
			if item["root"] == root {
				return item
			}
		}
		t.Fatalf("GET /api/projects has no entry for %s: %v", root, list)
		return nil
	}

	// A project whose settings name no repo reports no repoId at all: that is
	// what the app reads as "no registered repo for this project".
	if got, ok := entry(root)["repoId"]; ok {
		t.Fatalf("a project with no intake reported repoId %v", got)
	}

	// The repo its intake names is the repo its issues come from.
	if code := putSettings(t, s, root, ProjectSettings{Intake: ProjectIntake{RepoID: "owner/name"}}); code != http.StatusOK {
		t.Fatalf("put settings = %d", code)
	}
	if got := entry(root)["repoId"]; got != "owner/name" {
		t.Fatalf("repoId = %v, want owner/name", got)
	}

	// A repo removed after the settings named it leaves the project naming
	// none: reporting the stale id would send the dialog to a route that
	// answers 404.
	if err := e.f.ws.RemoveRepo("owner/name"); err != nil {
		t.Fatal(err)
	}
	if got, ok := entry(root)["repoId"]; ok {
		t.Fatalf("a removed repo was still reported as %v", got)
	}
}
