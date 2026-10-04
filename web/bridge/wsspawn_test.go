package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wsSpawnFleet is a fleet with a fake parse agent, a fake image runtime,
// and a fake agent per spawn whose container config is captured.
type wsSpawnEnv struct {
	f      *Fleet
	imgs   *fakeImages
	agents map[string]*fakeAgent
	cfgs   map[string]ContainerConfig
	// setupsAtStart is how many setup commands had run when each agent's
	// runtime was created.
	setupsAtStart map[string]int
}

func newWSSpawnEnv(t *testing.T) *wsSpawnEnv {
	t.Helper()
	f, _, imgs := wsFleet(t)
	env := &wsSpawnEnv{f: f, imgs: imgs, agents: map[string]*fakeAgent{}, cfgs: map[string]ContainerConfig{}, setupsAtStart: map[string]int{}}
	f.newRuntime = func(a Agent) (*Child, error) {
		cfg, err := f.containerConfigFor(a, "/usr/bin/docker", "docker")
		if err != nil {
			return nil, err
		}
		env.cfgs[a.ID] = cfg
		env.setupsAtStart[a.ID] = imgs.count("run", "--rm", "--name")
		fa := newFakeAgent()
		env.agents[a.ID] = fa
		return &Child{Transport: fa}, nil
	}
	return env
}

// builtTemplate publishes doc as name and marks it built.
func (e *wsSpawnEnv) builtTemplate(t *testing.T, name string, doc WSDoc) {
	t.Helper()
	publishDoc(t, e.f, name, doc)
	if err := e.f.templates.SetBuild(name, 1, "ok", "marshal-derived-"+name, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func (e *wsSpawnEnv) spawn(t *testing.T, opts SpawnOptions) (string, error) {
	t.Helper()
	root := t.TempDir()
	return e.f.Spawn(ctlContext(t), root, opts)
}

func TestSpawnWorkspaceRunsInTheBuiltImage(t *testing.T) {
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Mounts = []WSMount{{Volume: "gocache", Target: "/go/pkg", Readonly: false}}
	doc.Files = map[string]WSFile{"conf/app.toml": {Target: "/etc/app.toml", Readonly: true}}
	doc.Resources = WSResources{CPU: 3, Memory: "6g", Timeout: "2h"}
	e.builtTemplate(t, "svc", doc)

	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.cfgs[id]
	args := strings.Join(newContainerTransport(cfg).buildRunArgs(), " ")
	for _, want := range []string{
		"marshal-derived-svc acp",
		"--mount type=volume,source=gocache,target=/go/pkg",
		"--mount type=volume,source=marshal-state,target=/etc/app.toml,volume-subpath=workspaces/svc/files/conf/app.toml,readonly",
		"--cpus 3", "--memory 6144m",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("run args lack %q:\n%s", want, args)
		}
	}
	a, _ := e.f.ws.Agent(id)
	if a.Workspace == nil || a.Workspace.Name != "svc" || a.Workspace.Version != 1 || a.Workspace.Source != "studio" {
		t.Fatalf("agent workspace = %+v", a.Workspace)
	}
	if snap := e.f.Snapshot(); len(snap) != 1 || snap[0].Workspace == nil || snap[0].Workspace.Name != "svc" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// The derive step must not run again over an already-derived image.
	if n := e.imgs.count("build"); n != 0 {
		t.Fatalf("spawn issued %d builds", n)
	}
}

func TestSpawnWorkspaceUsesTheProjectDefault(t *testing.T) {
	e := newWSSpawnEnv(t)
	e.builtTemplate(t, "svc", sampleDoc("svc"))
	root := t.TempDir()
	if err := e.f.ws.PutProjectSettings(root, ProjectSettings{Workspace: "svc"}); err != nil {
		t.Fatal(err)
	}
	id, err := e.f.Spawn(ctlContext(t), root, SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := e.f.ws.Agent(id); a.Workspace == nil || a.Workspace.Name != "svc" {
		t.Fatalf("agent workspace = %+v", a.Workspace)
	}
}

func TestSpawnWithoutAWorkspaceIsUnchanged(t *testing.T) {
	e := newWSSpawnEnv(t)
	id, err := e.spawn(t, SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := e.f.ws.Agent(id); a.Workspace != nil {
		t.Fatalf("workspace = %+v", a.Workspace)
	}
	if len(e.cfgs[id].ExtraMounts) != 0 || len(e.cfgs[id].ExtraEnv) != 0 {
		t.Fatalf("extras leaked into a plain spawn: %+v", e.cfgs[id])
	}
}

func TestSpawnWorkspaceNotBuilt(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc")) // pending, never built
	_, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if !errors.Is(err, ErrWorkspaceNotBuilt) {
		t.Fatalf("err = %v", err)
	}
	if len(e.f.ws.Agents()) != 0 {
		t.Fatal("a refused spawn left an agent behind")
	}
	rec := httpCode(t, e.f, err)
	if rec != 409 {
		t.Fatalf("status = %d", rec)
	}
}

func TestSpawnWorkspaceSetupRunsBeforeTheAgentAndFailureAborts(t *testing.T) {
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Setup = WSSetup{Run: "make deps"}
	e.builtTemplate(t, "svc", doc)

	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if e.setupsAtStart[id] != 1 {
		t.Fatalf("setup commands before the agent started = %d, want 1", e.setupsAtStart[id])
	}
	var setup []string
	for _, c := range e.imgs.cmds {
		if len(c) > 4 && strings.HasPrefix(c[4], setupContainerPrefix) {
			setup = c
		}
	}
	joined := strings.Join(setup, " ")
	if !strings.Contains(joined, "--entrypoint sh marshal-derived-svc -c make deps") || strings.Contains(joined, " -d ") {
		t.Fatalf("setup command = %q", joined)
	}

	// A failing setup aborts the spawn and reports the output.
	e2 := newWSSpawnEnv(t)
	e2.builtTemplate(t, "svc", doc)
	e2.imgs.out["run"] = "npm ERR! missing script\n"
	failing := e2.f.runner
	e2.f.runner = func(name string, args ...string) ([]byte, error) {
		out, err := failing(name, args...)
		if len(args) > 0 && args[0] == "run" {
			return out, errors.New("exit status 2")
		}
		return out, err
	}
	_, err = e2.spawn(t, SpawnOptions{Workspace: "svc"})
	var sf ErrSetupFailed
	if !errors.As(err, &sf) || !strings.Contains(sf.Output, "missing script") {
		t.Fatalf("err = %v", err)
	}
	if len(e2.agents) != 0 || len(e2.f.ws.Agents()) != 0 {
		t.Fatal("a failed setup still started an agent")
	}
	if code := httpCode(t, e2.f, err); code != 502 {
		t.Fatalf("status = %d", code)
	}
}

func TestSpawnWorkspacePassesPolicyToSessionNew(t *testing.T) {
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Policy = WSPolicy{Mode: "acceptEdits", Allow: []string{"go test *"}}
	e.builtTemplate(t, "svc", doc)
	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	calls := e.agents[id].calls("session/new")
	if len(calls) != 1 {
		t.Fatalf("session/new calls = %v", calls)
	}
	var p struct {
		Policy struct {
			Mode  string   `json:"mode"`
			Allow []string `json:"allow"`
		} `json:"policy"`
	}
	_ = json.Unmarshal([]byte(calls[0]), &p)
	if p.Policy.Mode != "acceptEdits" || len(p.Policy.Allow) != 1 || p.Policy.Allow[0] != "go test *" {
		t.Fatalf("policy = %+v in %s", p.Policy, calls[0])
	}
}

func TestSpawnWorkspaceRefusesBadMountTargets(t *testing.T) {
	for name, m := range map[string]WSMount{
		"work":     {Volume: "v", Target: "/work"},
		"inside":   {Volume: "v", Target: "/run/marshal/x"},
		"marshal":  {Volume: "v", Target: "/marshal/config"},
		"sock":     {Volume: "v", Target: "/var/run/docker.sock"},
		"relative": {Volume: "v", Target: "data"},
		"inject":   {Volume: "v", Target: "/x,source=/etc"},
		"volume":   {Volume: "v,bind", Target: "/x"},
		"neither":  {Target: "/x"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newWSSpawnEnv(t)
			doc := sampleDoc("svc")
			doc.Mounts = []WSMount{m}
			e.builtTemplate(t, "svc", doc)
			if _, err := e.spawn(t, SpawnOptions{Workspace: "svc"}); !errors.Is(err, ErrWorkspaceMountTarget) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// A target used twice is refused too.
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Mounts = []WSMount{{Volume: "a", Target: "/data"}, {Volume: "b", Target: "/data"}}
	e.builtTemplate(t, "svc", doc)
	if _, err := e.spawn(t, SpawnOptions{Workspace: "svc"}); !errors.Is(err, ErrWorkspaceMountTarget) {
		t.Fatalf("duplicate target err = %v", err)
	}
	// And a file source may not climb out of the file store.
	e = newWSSpawnEnv(t)
	doc = sampleDoc("svc")
	doc.Files = map[string]WSFile{"../../secrets": {Target: "/x"}}
	e.builtTemplate(t, "svc", doc)
	if _, err := e.spawn(t, SpawnOptions{Workspace: "svc"}); !errors.Is(err, ErrWorkspaceMountTarget) {
		t.Fatalf("file escape err = %v", err)
	}
}

func TestSpawnWorkspaceMountsARepoMirrorReadOnly(t *testing.T) {
	bare := newBareRepoFixture(t)
	e := newWSSpawnEnv(t)
	e.f.git = testGitRunner(t)
	if err := e.f.ws.PutRepo(Repo{ID: "lib", URL: bare, OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	doc := sampleDoc("svc")
	doc.Mounts = []WSMount{{Repo: "lib", Target: "/libs/lib"}}
	e.builtTemplate(t, "svc", doc)
	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(newContainerTransport(e.cfgs[id]).buildRunArgs(), " ")
	want := "target=/libs/lib,volume-subpath=repos/" + filepath.Base(mirrorDir(e.f.stateDir, bare)) + ",readonly"
	if !strings.Contains(args, want) {
		t.Fatalf("args lack %q:\n%s", want, args)
	}
}

func TestSpawnWorkspaceTimeoutPausesTheAgent(t *testing.T) {
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Resources.Timeout = "90m"
	e.builtTemplate(t, "svc", doc)
	var fire func()
	var armed time.Duration
	e.f.afterFunc = func(d time.Duration, fn func()) func() { armed, fire = d, fn; return func() {} }

	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if fire != nil {
		t.Fatal("the deadline armed before any prompt")
	}
	rt, err := e.f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.reg.Prompt(context.Background(), e.f.sessionIDFor(rt), "go"); err != nil {
		t.Fatal(err)
	}
	if armed != 90*time.Minute || fire == nil {
		t.Fatalf("armed = %v fire=%v", armed, fire != nil)
	}
	fire()
	if _, err := e.f.runtimeForAgent(id); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("agent still running after its deadline: %v", err)
	}
	if got := e.agents[id].calls("session/cancel"); len(got) != 1 {
		t.Fatalf("session/cancel calls = %v", got)
	}
	tail, _ := e.f.audit.Tail(10)
	found := false
	for _, ev := range tail {
		found = found || (ev.Event == AuditAgentTimeout && ev.AgentID == id)
	}
	if !found {
		t.Fatalf("no agent_timeout audit entry in %+v", tail)
	}
}

func TestPruneKeepsAMirrorATemplateMounts(t *testing.T) {
	e := newWSSpawnEnv(t)
	f := e.f
	libURL, otherURL := "https://example.com/lib.git", "https://example.com/other.git"
	if err := f.ws.PutRepo(Repo{ID: "lib", URL: libURL, OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{libURL, otherURL} {
		dir := mirrorDir(f.stateDir, u)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	doc := sampleDoc("svc")
	doc.Mounts = []WSMount{{Repo: "lib", Target: "/libs/lib"}}
	publishDoc(t, f, "svc", doc)

	if _, err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mirrorDir(f.stateDir, libURL)); err != nil {
		t.Fatalf("a mounted mirror was pruned: %v", err)
	}
	if _, err := os.Stat(mirrorDir(f.stateDir, otherURL)); !os.IsNotExist(err) {
		t.Fatalf("an unreferenced mirror survived: %v", err)
	}
}

// httpCode runs err through writeErr and returns the status.
func httpCode(t *testing.T, _ *Fleet, err error) int {
	t.Helper()
	rec := httptest.NewRecorder()
	writeErr(rec, err)
	return rec.Code
}
