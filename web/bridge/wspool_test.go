package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pooledTemplate publishes and builds "svc" with the given pool size.
func pooledTemplate(t *testing.T, e *wsSpawnEnv, pool int) {
	t.Helper()
	doc := sampleDoc("svc")
	doc.Network = WSNetwork{} // an open network needs no proxy wiring
	publishDoc(t, e.f, "svc", doc)
	if err := e.f.templates.SetPool("svc", pool); err != nil {
		t.Fatal(err)
	}
	if err := e.f.BuildWorkspace(ctlContext(t), "svc", 1); err != nil {
		t.Fatal(err)
	}
	e.f.pools.wait()
}

// poolRuns lists the pool containers the runtime was asked to start.
func poolRuns(imgs *fakeImages) []string {
	imgs.mu.Lock()
	defer imgs.mu.Unlock()
	var out []string
	for _, c := range imgs.cmds {
		if len(c) > 5 && c[1] == "run" && strings.HasPrefix(c[5], poolContainerPrefix) {
			out = append(out, c[5])
		}
	}
	return out
}

func TestPoolFillStartsTheConfiguredNumber(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 2)
	got := poolRuns(e.imgs)
	if len(got) != 2 || got[0] != "marshal-pool-svc-v1-0" || got[1] != "marshal-pool-svc-v1-1" {
		t.Fatalf("pool containers = %v", got)
	}
	if n := e.f.pools.idleCount("svc", 1); n != 2 {
		t.Fatalf("idle = %d", n)
	}
	for _, sub := range []string{"pool/svc-0/work", "pool/svc-0/sock", "pool/svc-1/work"} {
		if _, err := os.Stat(filepath.Join(e.f.stateDir, sub)); err != nil {
			t.Errorf("%s: %v", sub, err)
		}
	}
	// The run args use the pool subpaths, the derived image and run the agent.
	e.imgs.mu.Lock()
	var args string
	for _, c := range e.imgs.cmds {
		if len(c) > 5 && c[5] == "marshal-pool-svc-v1-0" {
			args = strings.Join(c, " ")
		}
	}
	e.imgs.mu.Unlock()
	for _, want := range []string{"volume-subpath=pool/svc-0/work", "volume-subpath=pool/svc-0/sock", "acp --listen", "marshal-derived-"} {
		if !strings.Contains(args, want) {
			t.Errorf("pool run lacks %q:\n%s", want, args)
		}
	}
	// Filling again is a no-op once full.
	if err := e.f.pools.fill("svc", 1); err != nil {
		t.Fatal(err)
	}
	if len(poolRuns(e.imgs)) != 2 {
		t.Fatalf("a full pool started more containers: %v", poolRuns(e.imgs))
	}
}

func TestPoolIsUnusedWithoutASize(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 0)
	if len(poolRuns(e.imgs)) != 0 {
		t.Fatalf("containers = %v", poolRuns(e.imgs))
	}
}

func TestPoolGitSpawnTakesOneAndRefills(t *testing.T) {
	bare := newBareRepoFixture(t)
	e := newWSSpawnEnv(t)
	e.f.git = testGitRunner(t)
	pooledTemplate(t, e, 2)

	id, err := e.f.Spawn(ctlContext(t), t.TempDir(), SpawnOptions{Workspace: "svc", URL: bare})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.f.ws.Agent(id)
	if a.ContainerName != "marshal-pool-svc-v1-0" || a.WorkSubpath != "pool/svc-0/work" || a.SocketSubpath != "pool/svc-0/sock" {
		t.Fatalf("agent did not take a pool container: %+v", a)
	}
	if cfg := e.cfgs[id]; cfg.Name != a.ContainerName || cfg.WorkSubpath != a.WorkSubpath {
		t.Fatalf("container config = %q %q", cfg.Name, cfg.WorkSubpath)
	}
	if _, err := os.Stat(filepath.Join(e.f.stateDir, "pool/svc-0/work/README.md")); err != nil {
		t.Fatalf("checkout did not land in the pool's work dir: %v", err)
	}
	if a.Project != filepath.Join(e.f.stateDir, "pool/svc-0/work") {
		t.Fatalf("project = %q", a.Project)
	}
	e.f.pools.wait()
	// The refill restores the configured size without reusing the slot
	// the agent now holds.
	if n := e.f.pools.idleCount("svc", 1); n != 2 {
		t.Fatalf("idle after the refill = %d, want 2", n)
	}
	runs := poolRuns(e.imgs)
	if len(runs) != 3 || runs[2] != "marshal-pool-svc-v1-2" {
		t.Fatalf("pool runs = %v", runs)
	}
	med := e.f.pools.startMedians("svc")
	if med["warmMs"] < 0 {
		t.Fatalf("medians = %v", med)
	}
	if len(e.f.pools.starts["svc"]) != 1 || !e.f.pools.starts["svc"][0].warm {
		t.Fatalf("start samples = %+v", e.f.pools.starts["svc"])
	}

	// Retiring the agent removes its pool directory.
	e.f.stopAgent(id)
	if _, err := os.Stat(filepath.Join(e.f.stateDir, "pool/svc-0")); !os.IsNotExist(err) {
		t.Fatalf("pool dir survived its agent: %v", err)
	}
}

func TestPoolLocalSpawnDoesNotTake(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 1)
	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.f.ws.Agent(id)
	if a.ContainerName != "" {
		t.Fatalf("a local spawn took a pool container: %+v", a)
	}
	if n := e.f.pools.idleCount("svc", 1); n != 1 {
		t.Fatalf("idle = %d", n)
	}
	if samples := e.f.pools.starts["svc"]; len(samples) != 1 || samples[0].warm {
		t.Fatalf("start samples = %+v", samples)
	}
}

func TestPoolSizeZeroKillsIdleContainers(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 2)
	e.f.templates.SetPool("svc", 0)
	e.f.pools.resize("svc")
	if n := e.f.pools.idleCount("svc", 1); n != 0 {
		t.Fatalf("idle = %d", n)
	}
	if e.imgs.count("kill", "marshal-pool-svc-v1-0")+e.imgs.count("kill", "marshal-pool-svc-v1-1") != 2 {
		t.Fatalf("commands = %v", e.imgs.cmds)
	}
	if _, err := os.Stat(filepath.Join(e.f.stateDir, "pool/svc-0")); !os.IsNotExist(err) {
		t.Fatalf("pool dir survived: %v", err)
	}
}

func TestPoolShrinkKeepsTheRest(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 3)
	e.f.templates.SetPool("svc", 1)
	e.f.pools.resize("svc")
	if n := e.f.pools.idleCount("svc", 1); n != 1 {
		t.Fatalf("idle = %d", n)
	}
}

func TestPoolDeleteDropsIdleContainers(t *testing.T) {
	e := newWSSpawnEnv(t)
	pooledTemplate(t, e, 1)
	s := NewServer(e.f, "")
	if rec := doReq(t, s, "DELETE", "/api/workspaces/svc", nil, nil); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if e.imgs.count("kill", "marshal-pool-svc-v1-0") != 1 {
		t.Fatalf("commands = %v", e.imgs.cmds)
	}
}

func TestPoolAdoptsRunningContainersOnStartup(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	e.f.templates.SetBuild("svc", 1, "ok", "marshal-derived-svc", 1, 1)
	e.f.templates.SetPool("svc", 2)
	// One container is held by an agent, one is idle, one is surplus, one
	// belongs to a template that no longer exists.
	e.f.ws.PutAgent(Agent{ID: "a1", Project: "/p", ContainerName: "marshal-pool-svc-v1-0", WorkSubpath: "pool/svc-0/work"})
	e.imgs.out["ps"] = "marshal-pool-svc-v1-0\nmarshal-pool-svc-v1-1\nmarshal-pool-svc-v1-2\nmarshal-pool-gone-v1-0\nmarshal-agent-xyz\n"

	e.f.pools.adopt()
	e.f.pools.wait()
	if n := e.f.pools.idleCount("svc", 1); n < 1 {
		t.Fatalf("idle after adopt = %d", n)
	}
	if e.imgs.count("kill", "marshal-pool-gone-v1-0") != 1 {
		t.Fatalf("an orphaned pool container was not killed: %v", e.imgs.cmds)
	}
	if e.imgs.count("kill", "marshal-pool-svc-v1-0") != 0 {
		t.Fatal("a container held by an agent was killed")
	}
	if e.imgs.count("kill", "marshal-agent-xyz") != 0 {
		t.Fatal("a non-pool container was touched")
	}
}

func TestPoolContainersAreListedForReattach(t *testing.T) {
	var gotArgs []string
	tr := newContainerTransport(ContainerConfig{Runtime: "docker", Name: "marshal-pool-svc-v1-0"})
	tr.run = func(name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("marshal-agent-a\nmarshal-pool-svc-v1-0\nfoo-marshal-pool-x\n"), nil
	}
	names, err := tr.listAgentContainers()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[1] != "marshal-pool-svc-v1-0" {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "name="+poolContainerPrefix) {
		t.Fatalf("ps args = %v", gotArgs)
	}
}

func TestPoolSkipsWorkspacesThatNeedTheProxy(t *testing.T) {
	cases := map[string]func(*WSDoc){
		"allowlist": func(d *WSDoc) { d.Network = WSNetwork{Mode: "allowlist", Egress: []string{"github.com"}} },
		"off":       func(d *WSDoc) { d.Network = WSNetwork{Mode: "off"} },
		"inject": func(d *WSDoc) {
			d.Network = WSNetwork{Mode: "open"}
			d.Inject = map[string]WSInject{"api.example.com": {Ref: "vault:k", Header: "Authorization"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newWSSpawnEnv(t)
			doc := sampleDoc("svc")
			mutate(&doc)
			publishDoc(t, e.f, "svc", doc)
			if err := e.f.templates.SetPool("svc", 2); err != nil {
				t.Fatal(err)
			}
			if err := e.f.BuildWorkspace(ctlContext(t), "svc", 1); err != nil {
				t.Fatal(err)
			}
			e.f.pools.wait()
			if got := poolRuns(e.imgs); len(got) != 0 {
				t.Fatalf("pool containers started without proxy wiring: %v", got)
			}
		})
	}
}
