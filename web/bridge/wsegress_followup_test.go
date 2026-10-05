package bridge

import (
	"context"
	"errors"
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

// repoTemplateAgent is a local agent whose project holds a repo template.
func repoTemplateAgent(t *testing.T, trusted bool) (Agent, string, []byte) {
	t.Helper()
	home := fakeHome(t)
	root := projectWithConfig(t)
	src := wsSrc(t, sampleDoc("r"))
	writeRepoTemplate(t, root, "r", src)
	if trusted {
		writeTrustStore(t, home, `{"`+root+`":{"trusted":true}}`)
	}
	file := filepath.Join(root, ".marshal", "workspaces", "r.toml")
	return Agent{ID: "a1", SourceKind: "local", Project: root, Workspace: &AgentWorkspace{Name: "r", Source: "repo"}}, file, src
}

func TestAddToWorkspaceRepoReturnsAPatchAndWritesNothing(t *testing.T) {
	e := newWSSpawnEnv(t)
	a, file, src := repoTemplateAgent(t, true)
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

func TestAddToWorkspaceRepoRefusesUntrustedSymlinkedAndHugeTemplates(t *testing.T) {
	e := newWSSpawnEnv(t)
	req := func() *http.Request { return httptest.NewRequest(http.MethodPost, "/", nil) }

	// Untrusted project: refused before anything is parsed.
	a, _, _ := repoTemplateAgent(t, false)
	if _, err := e.f.addAgentHostToWorkspace(req(), a, "x.com"); !errors.Is(err, ErrUntrustedRepoTemplate) {
		t.Fatalf("untrusted = %v", err)
	}
	if _, err := readRepoTemplate(a.Project, a.Project, filepath.Join(".marshal", "workspaces", "r.toml")); !errors.Is(err, ErrUntrustedRepoTemplate) {
		t.Fatalf("untrusted read = %v", err)
	}

	// A symlinked template is not followed.
	a, file, _ := repoTemplateAgent(t, true)
	secret := filepath.Join(t.TempDir(), "secret.toml")
	os.WriteFile(secret, wsSrc(t, sampleDoc("leak")), 0o600)
	os.Remove(file)
	if err := os.Symlink(secret, file); err != nil {
		t.Skip("no symlinks")
	}
	if _, err := e.f.addAgentHostToWorkspace(req(), a, "x.com"); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("symlinked template = %v", err)
	}

	// So is a symlinked directory on the way.
	a, file, _ = repoTemplateAgent(t, true)
	realDir := filepath.Join(t.TempDir(), "workspaces")
	os.MkdirAll(realDir, 0o755)
	os.WriteFile(filepath.Join(realDir, "r.toml"), wsSrc(t, sampleDoc("leak")), 0o600)
	dir := filepath.Dir(file)
	os.RemoveAll(dir)
	os.Symlink(realDir, dir)
	if _, err := e.f.addAgentHostToWorkspace(req(), a, "x.com"); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("symlinked directory = %v", err)
	}

	// An oversized template is refused unread.
	a, file, _ = repoTemplateAgent(t, true)
	os.WriteFile(file, make([]byte, maxRepoTemplateBytes+1), 0o644)
	if _, err := e.f.addAgentHostToWorkspace(req(), a, "x.com"); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("huge template = %v", err)
	}
}

func TestAddToWorkspaceStudioDoesNotOverwriteAConcurrentDraftEdit(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	src, _ := e.f.templates.Read("svc", 0)
	// Another editor saves between the read and the write.
	edited := sampleDoc("svc")
	edited.Packages.Apt = []string{"git", "jq"}
	if err := e.f.templates.SaveDraft("svc", wsSrc(t, edited)); err != nil {
		t.Fatal(err)
	}
	if err := e.f.templates.SaveDraftIf("svc", src, []byte("patched")); !errors.Is(err, ErrDraftChanged) {
		t.Fatalf("SaveDraftIf = %v, want ErrDraftChanged", err)
	}
	got, _ := e.f.templates.Read("svc", 0)
	if string(got) != string(wsSrc(t, edited)) {
		t.Fatal("the concurrent edit was overwritten")
	}
	// With the draft unchanged the swap goes through.
	if err := e.f.templates.SaveDraftIf("svc", got, []byte("patched")); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptedPoolContainerKeepsItsCAGenerationAndIPPin(t *testing.T) {
	ctx := ctlContext(t)
	e := newWSSpawnEnv(t)
	startWSEgress(t, e)
	h := e.f.egress
	pseudo := "pool-svc-v1-0"
	spec := EgressSpec{Mode: EgressModeAllowlist, Inject: []EgressInjectSpec{{Host: "api.example.com", Header: "X-Key", Ref: "vault:api/key"}}}
	e.f.SetSecrets(testSecretProvider(t))
	e.f.secrets.Put(ctx, DefaultOwnerID, "api/key", []byte("k"))
	if _, err := e.f.egressWire(ctx, Agent{ID: pseudo}, "svc", spec, "", pseudo); err != nil {
		t.Fatal(err)
	}
	first := h.snapshot().Agents[pseudo].CA
	if first == "" {
		t.Fatal("pool container has no CA generation")
	}
	if _, err := e.f.rotateCA(ctx, "svc"); err != nil {
		t.Fatal(err)
	}
	e.f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) { return "svc", spec, nil }
	a := Agent{ID: "ag1", ContainerName: "marshal-pool-svc-v1-0"}
	if _, _, err := e.f.egressPrepare(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := h.snapshot().Agents["ag1"].CA; got != first {
		t.Fatalf("adopting agent got CA %q, the container was started under %q", got, first)
	}
	if got := h.snapshot().Agents[pseudo].CA; got != first {
		t.Fatalf("alias CA = %q", got)
	}
	// The IP pin follows once the agent's container is inspected.
	e.f.egressAttached(a)
	if ip := h.snapshot().Agents[pseudo].IP; ip == "" || ip != h.snapshot().Agents["ag1"].IP {
		t.Fatalf("alias IP %q, agent IP %q", ip, h.snapshot().Agents["ag1"].IP)
	}
}

func testSecretProvider(t *testing.T) SecretProvider {
	t.Helper()
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	p, err := NewLocalProvider(filepath.Join(root, "state"), key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoolAdoptionRestoresTheRegistrationInProcessMode(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	e.f.templates.SetBuild("svc", 1, "ok", "marshal-derived-svc", 1, 1)
	startWSEgress(t, e)
	e.f.egress.mode = egressModeProcess
	meta, _ := e.f.templates.Meta("svc")
	entry := poolEntry{name: "svc", version: 1, k: 0, container: "marshal-pool-svc-v1-0"}
	if !e.f.pools.wiredForProxy(entry, meta) {
		t.Fatal("a surviving pool container was refused in process mode")
	}
	if _, ok := e.f.egress.snapshot().Agents["pool-svc-v1-0"]; !ok {
		t.Fatal("the registration was not restored, so the container's token would be denied")
	}
}
