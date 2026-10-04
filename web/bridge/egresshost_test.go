package bridge

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRuntime records container-runtime commands and answers inspects.
type fakeRuntime struct {
	mu      sync.Mutex
	calls   [][]string
	network bool // marshal-agents exists
	image   bool
	running bool // sidecar running
	agentIP string
}

func (r *fakeRuntime) run(_ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	j := strings.Join(args, " ")
	switch {
	case j == "network inspect "+egressNetwork:
		if r.network {
			return nil, nil
		}
		return []byte("no such network"), errors.New("exit 1")
	case strings.HasPrefix(j, "network create"):
		r.network = true
		return nil, nil
	case strings.HasPrefix(j, "image inspect"):
		if r.image {
			return nil, nil
		}
		return nil, errors.New("exit 1")
	case strings.HasPrefix(j, "build "):
		r.image = true
		return nil, nil
	case strings.HasPrefix(j, "inspect --format {{.State.Running}} {{.Config.Image}}"):
		if r.running {
			return []byte("true " + egressImageTag("v1") + "\n"), nil
		}
		return nil, errors.New("no such container")
	case strings.HasPrefix(j, "inspect --format {{.State.Running}}"):
		if r.running {
			return []byte("true\n"), nil
		}
		return []byte("false\n"), nil
	case strings.Contains(j, "NetworkSettings.Networks"):
		if r.agentIP == "" {
			return nil, errors.New("not attached")
		}
		return []byte(r.agentIP + "\n"), nil
	case strings.HasPrefix(j, "run -d --rm --name "+egressContainer):
		r.running = true
		return nil, nil
	}
	return nil, nil
}

func (r *fakeRuntime) commands(prefix string) [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, c := range r.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			out = append(out, c)
		}
	}
	return out
}

func testEgressFleet(t *testing.T, secrets bool) (*Fleet, *fakeRuntime) {
	t.Helper()
	rt := &fakeRuntime{}
	f := testFleetWithRunner(t, rt.run)
	f.buildVersion = "v1"
	exe := filepath.Join(t.TempDir(), "webbridge")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	old := egressExecutable
	egressExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { egressExecutable = old })
	oldInt := egressMonitorInterval
	egressMonitorInterval = time.Hour
	t.Cleanup(func() { egressMonitorInterval = oldInt })
	f.stateDir = shortTempDir(t)
	if secrets {
		key := filepath.Join(t.TempDir(), "key")
		if err := GenerateKeyFile(key); err != nil {
			t.Fatal(err)
		}
		p, err := NewLocalProvider(filepath.Join(t.TempDir(), "state"), key)
		if err != nil {
			t.Fatal(err)
		}
		f.SetSecrets(p)
	}
	f.netlog = NewNetLog(f.stateDir)
	return f, rt
}

// shortTempDir keeps unix socket paths under the OS limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "eg")
	if err != nil {
		d = t.TempDir()
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestEgressContainerTopology(t *testing.T) {
	f, rt := testEgressFleet(t, false)
	if err := f.StartEgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.egressProcessMode() {
		t.Fatal("container runtime present but process mode reported")
	}
	if n := len(rt.commands("network create")); n != 1 {
		t.Fatalf("network created %d times", n)
	}
	if c := rt.commands("network create"); strings.Join(c[0], " ") != "network create --internal marshal-agents" {
		t.Fatalf("network create = %v", c[0])
	}
	builds := rt.commands("build ")
	if len(builds) != 1 || builds[0][2] != "marshal-egress:v1" {
		t.Fatalf("builds = %v", builds)
	}
	runs := rt.commands("run -d")
	if len(runs) != 1 {
		t.Fatalf("sidecar runs = %v", runs)
	}
	joined := strings.Join(runs[0], " ")
	for _, want := range []string{
		"--name marshal-egress", "--network marshal-agents",
		"target=/egress,volume-subpath=egress", "marshal-egress:v1 egress --listen :3128 --control unix:///egress/control.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sidecar args missing %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"--privileged", "--network host", "docker.sock"} {
		if strings.Contains(joined, bad) {
			t.Errorf("sidecar args contain %q", bad)
		}
	}
	if len(rt.commands("network connect bridge marshal-egress")) != 1 {
		t.Fatalf("sidecar not attached to the outside network: %v", rt.calls)
	}
	// A second start finds everything in place and changes nothing.
	f2, _ := testEgressFleet(t, false)
	f2.runner = rt.run
	f2.stateDir = f.stateDir
	f2.buildVersion = "v1"
	before := len(rt.calls)
	h := newEgressHost(f2)
	if err := f2.ensureSidecar(context.Background(), h, "docker"); err != nil {
		t.Fatal(err)
	}
	for _, c := range rt.calls[before:] {
		if c[0] == "run" || c[0] == "build" || (c[0] == "network" && c[1] == "create") {
			t.Errorf("re-ensure repeated work: %v", c)
		}
	}
}

func TestEgressSidecarRestartsStaleImage(t *testing.T) {
	f, rt := testEgressFleet(t, false)
	rt.network, rt.image, rt.running = true, true, true
	f.buildVersion = "v2" // the running sidecar is v1
	h := newEgressHost(f)
	if err := f.ensureSidecar(context.Background(), h, "docker"); err != nil {
		t.Fatal(err)
	}
	if len(rt.commands("rm -f marshal-egress")) != 1 || len(rt.commands("run -d")) != 1 {
		t.Fatalf("stale sidecar not replaced: %v", rt.calls)
	}
}

func TestEgressAgentWiringAndPolicy(t *testing.T) {
	ctx := context.Background()
	f, rt := testEgressFleet(t, false)
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	a := Agent{ID: "ag1", Project: "/p", SourceKind: "git", Profile: DefaultRuntimeProfile()}
	w, ok, err := f.egressPrepare(ctx, a)
	if err != nil || !ok {
		t.Fatalf("prepare: %v %v", ok, err)
	}
	cfg := ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: containerNameFor(a.ID),
		StateVolume: "marshal-state", WorkSubpath: "work/ag1", SocketSubpath: "sockets/ag1", SocketDir: "/s",
		Env: withEnv(map[string]string{"X": "1"}, w.Env), Network: w.Network, ExtraVolumes: w.Volumes,
	}
	joined := strings.Join(newContainerTransport(cfg).buildRunArgs(), " ")
	tok := f.egress.tokenFor("ag1")
	if !strings.Contains(joined, "--network marshal-agents") {
		t.Errorf("agent not on the internal network:\n%s", joined)
	}
	wantProxy := "http://ag1:" + tok + "@marshal-egress:3128"
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if !strings.Contains(joined, "-e "+k+"="+wantProxy) {
			t.Errorf("missing %s=%s", k, wantProxy)
		}
	}
	if !strings.Contains(joined, "-e NO_PROXY= ") {
		t.Errorf("NO_PROXY must be set empty:\n%s", joined)
	}
	if strings.Contains(joined, "SSL_CERT_FILE") || strings.Contains(joined, "/marshal/ca") {
		t.Errorf("no injection configured, yet CA wiring present:\n%s", joined)
	}
	if strings.Contains(joined, "--network host") {
		t.Error("host network")
	}
	pol := f.egress.snapshot().Agents["ag1"]
	if pol.Mode != EgressModeOpen || pol.Token != tok || pol.IP != "" || pol.Workspace != "default" {
		t.Fatalf("policy before start = %+v", pol)
	}
	// After start the policy is pinned to the container's address.
	rt.agentIP = "172.20.0.5"
	f.egressAttached(a)
	if got := f.egress.snapshot().Agents["ag1"].IP; got != "172.20.0.5" {
		t.Fatalf("IP = %q", got)
	}
	inspects := rt.commands("inspect --format {{(index .NetworkSettings.Networks \"marshal-agents\").IPAddress}}")
	if len(inspects) != 1 || inspects[0][len(inspects[0])-1] != containerNameFor("ag1") {
		t.Fatalf("inspect calls = %v", inspects)
	}
	f.egress.remove("ag1")
	if _, ok := f.egress.snapshot().Agents["ag1"]; ok {
		t.Fatal("agent still in the policy after removal")
	}
}

func TestEgressTokensSurviveRestart(t *testing.T) {
	f, _ := testEgressFleet(t, false)
	h1 := newEgressHost(f)
	if err := h1.loadTokenKey(); err != nil {
		t.Fatal(err)
	}
	h2 := newEgressHost(f)
	if err := h2.loadTokenKey(); err != nil {
		t.Fatal(err)
	}
	if h1.tokenFor("a") != h2.tokenFor("a") {
		t.Fatal("a reattached container's token would stop verifying")
	}
	if h1.tokenFor("a") == h1.tokenFor("b") || len(h1.tokenFor("a")) != 64 {
		t.Fatal("tokens must be distinct per agent and 32 bytes")
	}
}

func TestEgressInjectionNeedsWritableBackend(t *testing.T) {
	ctx := context.Background()
	f, _ := testEgressFleet(t, false) // env backend
	t.Setenv("MARSHAL_TEST_KEY", "k")
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Mode: EgressModeOpen, Inject: []EgressInjectSpec{
			{Host: "api.example.com", Header: "Authorization", Ref: "vault:env/MARSHAL_TEST_KEY", Format: "Bearer {value}"}}}, nil
	}
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.egressPrepare(ctx, Agent{ID: "ag1"})
	if !errors.Is(err, ErrInjectionUnavailable) {
		t.Fatalf("err = %v, want ErrInjectionUnavailable", err)
	}
	if _, ok := f.egress.snapshot().Agents["ag1"]; ok {
		t.Fatal("a refused agent stayed registered")
	}
}

func TestEgressInjectionAddsCAMountAndEnv(t *testing.T) {
	ctx := context.Background()
	f, _ := testEgressFleet(t, true) // local backend
	f.Close()                        // re-created below; testFleet registers cleanup
	f, _ = testEgressFleet(t, true)
	f.secrets.Put(ctx, DefaultOwnerID, "git/key", []byte("sekret\n"))
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Mode: EgressModeAllowlist, Allow: []string{"github.com"}, Inject: []EgressInjectSpec{
			{Host: "API.example.com", Header: "Authorization", Ref: "vault:git/key", Format: "Bearer {value}"}}}, nil
	}
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	w, _, err := f.egressPrepare(ctx, Agent{ID: "ag1"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Env["SSL_CERT_FILE"] != "/marshal/ca/dev-bundle.pem" || w.Env["NODE_EXTRA_CA_CERTS"] != "/marshal/ca/dev.pem" ||
		w.Env["GIT_SSL_CAINFO"] == "" || w.Env["REQUESTS_CA_BUNDLE"] == "" || w.Env["CURL_CA_BUNDLE"] == "" {
		t.Fatalf("CA env = %v", w.Env)
	}
	cfg := ContainerConfig{RuntimeName: "docker", StateVolume: "v", ExtraVolumes: w.Volumes, Network: w.Network, Image: "i", Name: "n"}
	joined := strings.Join(newContainerTransport(cfg).buildRunArgs(), " ")
	if !strings.Contains(joined, "target=/marshal/ca,volume-subpath=ca,readonly") {
		t.Errorf("CA not mounted read-only:\n%s", joined)
	}
	for _, e := range w.Env {
		if strings.Contains(e, "sekret") {
			t.Fatal("the secret reached the agent environment")
		}
	}
	pol := f.egress.snapshot().Agents["ag1"]
	if inj := pol.Inject["api.example.com"]; inj.Header != "Authorization" || inj.Value != "Bearer sekret" {
		t.Fatalf("inject = %+v", pol.Inject)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, "ca", "dev-bundle.pem")); err != nil {
		t.Fatalf("bundle not written: %v", err)
	}
	// Rotating the stored secret reaches live policy.
	f.secrets.Put(ctx, DefaultOwnerID, "git/key", []byte("rotated"))
	f.egress.refreshAll(ctx)
	if got := f.egress.snapshot().Agents["ag1"].Inject["api.example.com"].Value; got != "Bearer rotated" {
		t.Fatalf("after refresh = %q", got)
	}
}

func TestEgressProviderKeysInjectedAndHostsAllowed(t *testing.T) {
	ctx := context.Background()
	f, _ := testEgressFleet(t, true)
	f.providerHosts = func(context.Context) map[string]string {
		return map[string]string{"openai": "api.openai.com", "local": "localhost"}
	}
	f.secrets.Put(ctx, DefaultOwnerID, "providers/openai", []byte("sk-abc"))
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.egressPrepare(ctx, Agent{ID: "ag1"}); err != nil || !ok {
		t.Fatalf("prepare: %v", err)
	}
	pol := f.egress.snapshot().Agents["ag1"]
	if inj := pol.Inject["api.openai.com"]; inj.Header != "Authorization" || inj.Value != "Bearer sk-abc" {
		t.Fatalf("provider injection = %+v", pol.Inject)
	}
	if _, bad := pol.Inject["localhost"]; bad {
		t.Fatal("provider without a stored key was injected")
	}
	if !allowedListContains(pol.Allow, "api.openai.com") || !allowedListContains(pol.Allow, "localhost") {
		t.Fatalf("provider hosts not allowlisted: %v", pol.Allow)
	}
}

func allowedListContains(list []string, host string) bool {
	for _, h := range list {
		if h == host {
			return true
		}
	}
	return false
}

func TestEgressControlSocketStreamsPolicyUpdates(t *testing.T) {
	ctx := context.Background()
	f, _ := testEgressFleet(t, false)
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	sockPath := f.egress.controlSocketPath()
	if info, err := os.Stat(sockPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control socket: %v %v", info, err)
	}
	f.egress.register(ctx, "ag1", "dev", EgressSpec{Mode: EgressModeAllowlist, Allow: []string{"example.com"}})
	c, err := controlClient("unix://" + sockPath)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://c/policy", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan EgressPolicy, 4)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var p EgressPolicy
			if json.Unmarshal(sc.Bytes(), &p) == nil {
				lines <- p
			}
		}
	}()
	next := func() EgressPolicy {
		select {
		case p := <-lines:
			return p
		case <-time.After(3 * time.Second):
			t.Fatal("no policy line")
			return EgressPolicy{}
		}
	}
	first := next()
	if len(first.Agents["ag1"].Grants) != 0 || first.Agents["ag1"].Mode != EgressModeAllowlist {
		t.Fatalf("initial = %+v", first)
	}
	if !f.egress.grant("ag1", "Registry.NPMJS.org.") {
		t.Fatal("grant refused")
	}
	second := next()
	if g := second.Agents["ag1"].Grants; len(g) != 1 || g[0] != "registry.npmjs.org" {
		t.Fatalf("after grant = %+v", second.Agents["ag1"])
	}
	if f.egress.grant("ghost", "x.com") {
		t.Fatal("granted to an unknown agent")
	}
}

// TestEgressSidecarEndToEnd runs the real sidecar against the bridge's
// control socket: the policy streams in, a request is allowed, another is
// blocked and reported, records land in the network log, and an injected
// host is intercepted with a leaf the bridge signed.
func TestEgressSidecarEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _ := testEgressFleet(t, true)
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen http.Header
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain") }))
	defer plain.Close()
	f.secrets.Put(ctx, DefaultOwnerID, "k/up", []byte("tok123"))
	upHost := hostOf(t, up.URL)
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Mode: EgressModeAllowlist, Allow: []string{upHost},
			Inject: []EgressInjectSpec{{Host: upHost, Header: "X-Api-Key", Ref: "vault:k/up"}}}, nil
	}
	w, _, err := f.egressPrepare(ctx, Agent{ID: "ag1"})
	if err != nil {
		t.Fatal(err)
	}
	tok := f.egress.tokenFor("ag1")

	sc, err := NewEgressSidecar("unix://" + f.egress.controlSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	upPool := x509.NewCertPool()
	upPool.AddCert(up.Certificate())
	sc.Proxy().upstream = &tls.Config{RootCAs: upPool}
	go sc.Run(ctx)
	proxySrv := httptest.NewServer(sc.Proxy())
	defer proxySrv.Close()

	ca, _, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	pu, _ := url.Parse(proxySrv.URL)
	pu.User = url.UserPassword("ag1", tok)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: roots}}}

	// The policy arrives asynchronously.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(up.URL)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(b) == "ok" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("injected request never succeeded: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	got := seen.Get("X-Api-Key")
	mu.Unlock()
	if got != "tok123" {
		t.Fatalf("upstream saw X-Api-Key %q", got)
	}
	// A host outside the allowlist is refused and reported.
	resp, err := client.Get(strings.Replace(plain.URL, "127.0.0.1", "localhost", 1))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain host = %d, want 403", resp.StatusCode)
	}
	client.CloseIdleConnections()
	waitCond(t, "network log records", func() bool {
		hosts := f.netlog.Hosts("dev", "")
		var injected, blocked bool
		for _, h := range hosts {
			injected = injected || (h.Host == upHost && h.Injected && h.Decision == "allow")
			blocked = blocked || (h.Decision == "block" && h.Blocked > 0)
		}
		return injected && blocked
	})
	waitCond(t, "network_block delta", func() bool { return fleetHasDelta(f, "network_block", "ag1") })
	_ = w
}

func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestEgressProcessMode(t *testing.T) {
	ctx := context.Background()
	f, _ := testEgressFleet(t, false)
	f.runner = nil
	f.egressRuntime = func() (string, bool) { return "", false }
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.egressProcessMode() || f.egress.proxy == nil {
		t.Fatal("process mode not active")
	}
	if !strings.HasPrefix(f.egress.proxyAddr, "127.0.0.1:") {
		t.Fatalf("proxy addr = %q", f.egress.proxyAddr)
	}
	w, ok, err := f.egressPrepare(ctx, Agent{ID: "ag1"})
	if err != nil || !ok {
		t.Fatal(err)
	}
	if w.Network != "" || len(w.Volumes) != 0 {
		t.Fatalf("process mode must not use a container network: %+v", w)
	}
	want := "http://ag1:" + f.egress.tokenFor("ag1") + "@" + f.egress.proxyAddr
	if w.Env["HTTPS_PROXY"] != want {
		t.Fatalf("HTTPS_PROXY = %q, want %q", w.Env["HTTPS_PROXY"], want)
	}
	// The in-process proxy enforces the policy it was given.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") }))
	defer up.Close()
	pu, _ := url.Parse(w.Env["HTTP_PROXY"])
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("open mode request = %d", resp.StatusCode)
	}
	waitCond(t, "record in network log", func() bool { return len(f.netlog.Hosts("default", "")) == 1 })
	f.egress.register(ctx, "ag1", "default", EgressSpec{Mode: EgressModeOff})
	resp, err = c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("off mode request = %d", resp.StatusCode)
	}
}

func TestEgressOffLeavesSpawnUnchanged(t *testing.T) {
	f := testFleetWithAudit(t)
	if _, ok, err := f.egressPrepare(context.Background(), Agent{ID: "a"}); ok || err != nil {
		t.Fatalf("egress off must be a no-op: %v %v", ok, err)
	}
	if !f.egressProcessMode() {
		t.Fatal("no proxy means not isolated")
	}
}

func TestEgressDownRefusesRestrictedWorkspaces(t *testing.T) {
	f, _ := testEgressFleet(t, false)
	// Force the proxy start to fail: no executable to copy into the image.
	egressExecutable = func() (string, error) { return "", os.ErrNotExist }
	if err := f.StartEgress(context.Background()); err == nil {
		t.Fatal("start should fail")
	}
	if f.egressDown() == nil {
		t.Fatal("egressDown should report the failure")
	}
	ctx := context.Background()
	for _, spec := range []EgressSpec{
		{Mode: EgressModeAllowlist},
		{Mode: EgressModeOff},
		{Mode: EgressModeOpen, Inject: []EgressInjectSpec{{Host: "api.example.com", Header: "Authorization", Ref: "vault:x/y"}}},
	} {
		spec := spec
		f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) { return "dev", spec, nil }
		if _, _, err := f.egressPrepare(ctx, Agent{ID: "ag1"}); err == nil {
			t.Fatalf("spec %+v started without a proxy", spec)
		}
	}
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Mode: EgressModeOpen}, nil
	}
	if _, proxied, err := f.egressPrepare(ctx, Agent{ID: "ag2"}); err != nil || proxied {
		t.Fatalf("open workspace: proxied=%v err=%v", proxied, err)
	}
}

func TestEgressRefreshPublishesOnlyOnChange(t *testing.T) {
	f, _ := testEgressFleet(t, true)
	ctx := context.Background()
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	f.secrets.Put(ctx, DefaultOwnerID, "api/key", []byte("one"))
	f.workspaceEgress = func(context.Context, Agent) (string, EgressSpec, error) {
		return "dev", EgressSpec{Inject: []EgressInjectSpec{{Host: "api.example.com", Header: "X-Key", Ref: "vault:api/key"}}}, nil
	}
	if _, _, err := f.egressPrepare(ctx, Agent{ID: "ag1"}); err != nil {
		t.Fatal(err)
	}
	h := f.egress
	h.mu.Lock()
	ch := h.changed
	h.mu.Unlock()
	h.refreshAll(ctx)
	select {
	case <-ch:
		t.Fatal("unchanged secrets published a policy")
	default:
	}
	f.secrets.Put(ctx, DefaultOwnerID, "api/key", []byte("two"))
	h.refreshAll(ctx)
	select {
	case <-ch:
	default:
		t.Fatal("rotated secret was not published")
	}
	if got := h.snapshot().Agents["ag1"].Inject["api.example.com"].Value; got != "two" {
		t.Fatalf("injected value = %q", got)
	}
}
