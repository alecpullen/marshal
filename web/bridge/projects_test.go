package bridge

import (
	"encoding/json"
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
	if got.Workspace != "svc@2" || got.Mode != "plan" || got.ShipTarget != "patch" || got.Isolated == nil || !*got.Isolated || len(got.Intake.Labels) != 2 {
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
	if got := e.agents[out.AgentID].calls("session/set_mode"); len(got) != 1 || !strings.Contains(got[0], `"plan"`) {
		t.Fatalf("session/set_mode = %v", got)
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
	if got := e.agents[out.AgentID].calls("session/set_mode"); len(got) != 1 || !strings.Contains(got[0], `"auto"`) {
		t.Fatalf("session/set_mode = %v", got)
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
