package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	if stored.Version != 11 {
		t.Fatalf("workspace version = %d, want 11", stored.Version)
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
