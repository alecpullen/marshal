package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startWSEgress brings the egress proxy up on a workspace test fleet.
func startWSEgress(t *testing.T, e *wsSpawnEnv) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "webbridge")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	old := egressExecutable
	egressExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { egressExecutable = old })
	oldInt := egressMonitorInterval
	egressMonitorInterval = time.Hour
	t.Cleanup(func() { egressMonitorInterval = oldInt })
	e.f.stateDir = shortTempDir(t)
	e.f.netlog = NewNetLog(e.f.stateDir)
	if err := e.f.StartEgress(ctlContext(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.f.stopEgress)
}

func allowlistTemplate(t *testing.T, e *wsSpawnEnv, pool int) {
	t.Helper()
	publishDoc(t, e.f, "svc", sampleDoc("svc")) // allowlist
	e.f.templates.SetPool("svc", pool)
	if err := e.f.BuildWorkspace(ctlContext(t), "svc", 1); err != nil {
		t.Fatal(err)
	}
	e.f.pools.wait()
}

func TestPoolIsWiredToTheProxyAndHandedOffToTheAgent(t *testing.T) {
	bare := newBareRepoFixture(t)
	e := newWSSpawnEnv(t)
	e.f.git = testGitRunner(t)
	startWSEgress(t, e)
	prev := e.f.newRuntime
	e.f.newRuntime = func(a Agent) (*Child, error) {
		if _, _, err := e.f.egressPrepare(ctlContext(t), a); err != nil {
			return nil, err
		}
		return prev(a)
	}
	allowlistTemplate(t, e, 1)

	runs := poolRuns(e.imgs)
	if len(runs) != 1 {
		t.Fatalf("pool runs = %v", runs)
	}
	var args string
	e.imgs.mu.Lock()
	for _, c := range e.imgs.cmds {
		if len(c) > 5 && c[1] == "run" && c[5] == runs[0] {
			args = strings.Join(c, " ")
		}
	}
	e.imgs.mu.Unlock()
	for _, want := range []string{"--network " + egressNetwork, "HTTPS_PROXY=http://pool-svc-v1-0:"} {
		if !strings.Contains(args, want) {
			t.Fatalf("pool run lacks %q:\n%s", want, args)
		}
	}
	pseudo := "pool-svc-v1-0"
	idle := e.f.egress.snapshot().Agents[pseudo]
	if idle.Mode != EgressModeAllowlist || !allowed(idle, "github.com") || allowed(idle, "evil.com") {
		t.Fatalf("idle pool policy = %+v", idle)
	}

	id, err := e.f.Spawn(ctlContext(t), t.TempDir(), SpawnOptions{Workspace: "svc", URL: bare})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.f.ws.Agent(id)
	if a.ContainerName != "marshal-pool-svc-v1-0" {
		t.Fatalf("agent did not take the pool container: %+v", a)
	}
	snap := e.f.egress.snapshot()
	real, ok := snap.Agents[id]
	if !ok {
		t.Fatalf("adopting agent has no policy: %v", snap.Agents)
	}
	alias, ok := snap.Agents[pseudo]
	if !ok {
		t.Fatal("the pool container's credentials stopped working at hand-off")
	}
	if alias.Token != e.f.egress.tokenFor(pseudo) || real.Token != e.f.egress.tokenFor(id) || alias.Token == real.Token {
		t.Fatalf("tokens: alias %q real %q", alias.Token, real.Token)
	}
	if alias.Mode != real.Mode || !allowed(alias, "github.com") || allowed(alias, "evil.com") {
		t.Fatalf("alias policy = %+v", alias)
	}
	// A grant to the agent reaches the container's credentials too, and a
	// block reported under the pseudo ID is attributed to the agent.
	e.f.egress.grant(id, "registry.npmjs.org")
	if !allowed(e.f.egress.snapshot().Agents[pseudo], "registry.npmjs.org") {
		t.Fatal("a grant did not follow the alias")
	}
	if got := e.f.egress.realID(pseudo); got != id {
		t.Fatalf("realID = %q", got)
	}
	// Retiring the agent drops both identities.
	e.f.stopAgent(id)
	snap = e.f.egress.snapshot()
	if _, ok := snap.Agents[id]; ok {
		t.Fatal("agent policy outlived the agent")
	}
	if _, ok := snap.Agents[pseudo]; ok {
		t.Fatal("pool alias outlived the agent")
	}
}

func TestPoolIsNotFilledForProxyWorkspacesWithoutTheProxy(t *testing.T) {
	e := newWSSpawnEnv(t)
	allowlistTemplate(t, e, 1) // no StartEgress
	if len(poolRuns(e.imgs)) != 0 {
		t.Fatalf("an unrestricted pool container was started: %v", poolRuns(e.imgs))
	}
}

func TestPoolAdoptionRechecksTheProxyRequirement(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc")) // allowlist
	e.f.templates.SetBuild("svc", 1, "ok", "marshal-derived-svc", 1, 1)
	e.f.templates.SetPool("svc", 2)
	e.imgs.out["ps"] = "marshal-pool-svc-v1-0\n"

	// Proxy down: a pooled container for an allowlist workspace is not
	// adopted, it is killed.
	e.f.pools.adopt()
	e.f.pools.wait()
	if n := e.f.pools.idleCount("svc", 1); n != 0 {
		t.Fatalf("idle = %d, want 0", n)
	}
	if e.imgs.count("kill", "marshal-pool-svc-v1-0") != 1 {
		t.Fatalf("unwired container not killed: %v", e.imgs.cmds)
	}

	// Proxy up: a container that is not on the egress network (started
	// before wiring existed) is killed too.
	startWSEgress(t, e)
	e.f.pools.adopt()
	e.f.pools.wait()
	if e.imgs.count("kill", "marshal-pool-svc-v1-0") != 2 {
		t.Fatalf("container off the egress network was adopted: %v", e.imgs.cmds)
	}
}

func TestReattachedAgentKeepsItsCAGeneration(t *testing.T) {
	ctx := ctlContext(t)
	f, _ := testCAFleet(t)
	f.stateDir = shortTempDir(t)
	f.netlog = NewNetLog(f.stateDir)
	f.runner = func(string, ...string) ([]byte, error) { return nil, nil }
	f.buildVersion = "v1"
	exe := filepath.Join(t.TempDir(), "webbridge")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	old := egressExecutable
	egressExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { egressExecutable = old })
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.stopEgress)
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Inject: []EgressInjectSpec{{Host: "api.example.com", Header: "X-Key", Ref: "vault:api/key"}}}, nil
	}
	f.secrets.Put(ctx, DefaultOwnerID, "api/key", []byte("k"))
	ag := Agent{ID: "ag1"}
	if _, _, err := f.egressPrepare(ctx, ag); err != nil {
		t.Fatal(err)
	}
	first := f.egress.snapshot().Agents["ag1"].CA
	if first == "" {
		t.Fatal("no CA generation recorded")
	}
	if _, err := f.rotateCA(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	// Bridge restart: a new egress host reloads the persisted generations,
	// then the reattached agent is prepared again.
	f.stopEgress()
	f.egress = nil
	f.cas, f.retiredCAs = nil, nil
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.egressPrepare(ctx, ag); err != nil {
		t.Fatal(err)
	}
	if got := f.egress.snapshot().Agents["ag1"].CA; got != first {
		t.Fatalf("reattached agent moved to CA %q, started under %q", got, first)
	}
	if _, err := f.leafFor(ctx, "dev", first, "api.example.com"); err != nil {
		t.Fatalf("old generation does not sign after the restart: %v", err)
	}
}

func TestAddToWorkspaceStudioChangesTheDraft(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	a := Agent{ID: "a1", Workspace: &AgentWorkspace{Name: "svc", Version: 1, Source: "studio"}}
	res, err := e.f.addAgentHostToWorkspace(httptest.NewRequest(http.MethodPost, "/", nil), a, "registry.npmjs.org")
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["ok"] != true || m["workspace"] != "svc" || m["patch"] != nil {
		t.Fatalf("result = %v", m)
	}
	draft, _ := e.f.templates.Read("svc", 0)
	if !strings.Contains(string(draft), "registry.npmjs.org") || !strings.Contains(string(draft), "github.com") {
		t.Fatalf("draft = %s", draft)
	}
	// Adding the same host again changes nothing.
	if _, err := e.f.addAgentHostToWorkspace(httptest.NewRequest(http.MethodPost, "/", nil), a, "registry.npmjs.org"); err != nil {
		t.Fatal(err)
	}
	again, _ := e.f.templates.Read("svc", 0)
	if string(again) != string(draft) {
		t.Fatal("a repeated add rewrote the draft")
	}
	if _, err := e.f.addAgentHostToWorkspace(httptest.NewRequest(http.MethodPost, "/", nil), Agent{ID: "a2"}, "x.com"); err == nil {
		t.Fatal("an agent without a workspace accepted add-to-workspace")
	}
}

func TestAddToWorkspaceRepoReturnsAPatchAndWritesNothing(t *testing.T) {
	e := newWSSpawnEnv(t)
	root := t.TempDir()
	dir := filepath.Join(root, ".marshal", "workspaces")
	os.MkdirAll(dir, 0o755)
	src := wsSrc(t, sampleDoc("r"))
	file := filepath.Join(dir, "r.toml")
	os.WriteFile(file, src, 0o644)
	a := Agent{ID: "a1", SourceKind: "local", Project: root, Workspace: &AgentWorkspace{Name: "r", Source: "repo"}}
	res, err := e.f.addAgentHostToWorkspace(httptest.NewRequest(http.MethodPost, "/", nil), a, "registry.npmjs.org")
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	patch, ok := m["patch"].(string)
	if !ok || !strings.Contains(patch, "--- a/.marshal/workspaces/r.toml") || !strings.Contains(patch, "registry.npmjs.org") || !strings.Contains(patch, "\n+") {
		t.Fatalf("patch = %q (%v)", patch, m)
	}
	if after, _ := os.ReadFile(file); string(after) != string(src) {
		t.Fatal("the bridge wrote to the repo")
	}
	if _, has := m["source"]; has {
		t.Fatal("a repo result carries a source")
	}
}
