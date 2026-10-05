package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// egressExecutable locates the binary copied into the sidecar image.
var egressExecutable = os.Executable

// Names of the container-mode topology.
const (
	egressNetwork   = "marshal-agents"
	egressContainer = "marshal-egress"
	egressPort      = 3128
	// egressPreviewPort is the sidecar's preview listener.
	egressPreviewPort = 8081
	egressSubpath     = "egress"
	egressMountDir    = "/egress"

	// Where the CA material mounts inside an agent container.
	containerCABundle = "/marshal/ca-bundle.pem"
	containerCACert   = "/marshal/ca.pem"
)

// Egress modes of the host.
const (
	egressModeContainer = "container"
	egressModeProcess   = "process"
)

// EgressSpec is what a workspace asks of the proxy for one agent. The
// zero value is the default: open, nothing injected.
type EgressSpec struct {
	// Mode is off, open or allowlist; empty means open (today's
	// behaviour for a workspace with no [network]).
	Mode  string
	Allow []string
	// Inject lists credentials the proxy adds for a host.
	Inject []EgressInjectSpec
}

// EgressInjectSpec is one injected credential: Ref names the secret and
// Format wraps its value, with "{value}" standing for it.
type EgressInjectSpec struct {
	Host   string
	Header string
	Ref    string // vault:<path>
	Format string // default "{value}"
}

// egressAgent is the proxy's state for one agent.
type egressAgent struct {
	token  string
	ip     string
	spec   EgressSpec
	grants []string
	policy EgressAgentPolicy
	// workspace names the agent's workspace, for the CA.
	workspace string
	// caID is the CA generation the agent was started with. Rotation
	// retires it but the proxy keeps signing this agent's leaves with it.
	caID string
	// previewPorts are the workspace's declared preview ports.
	previewPorts []int
}

// egressHost owns the egress policy and the way agents reach the proxy.
type egressHost struct {
	f    *Fleet
	mode string

	mu      sync.Mutex
	agents  map[string]*egressAgent
	changed chan struct{} // closed and replaced on every policy change

	tokenKey []byte
	// caGens remembers which CA generation each agent uses (agent ->
	// workspace/id) across bridge restarts, persisted as ca-gen.json.
	caGens map[string]caGen
	// proxyAddr is host:port agents dial (container name or loopback).
	proxyAddr string

	// previewPort is the host port the sidecar's preview listener is
	// published on (container mode), 0 until known.
	previewPort atomic.Int32

	// process mode
	proxy   *EgressProxy
	records chan EgressRecord
	procSrv *http.Server

	ctlSock net.Listener
	ctlSrv  *http.Server
	stop    chan struct{}
	wg      sync.WaitGroup

	// executable is the binary copied into the sidecar image.
	executable func() (string, error)
}

type caGen struct {
	Workspace string `json:"workspace"`
	ID        string `json:"id"`
}

func newEgressHost(f *Fleet) *egressHost {
	return &egressHost{
		f: f, agents: map[string]*egressAgent{}, caGens: map[string]caGen{}, changed: make(chan struct{}),
		stop: make(chan struct{}), executable: egressExecutable,
	}
}

// loadTokenKey reads or creates the HMAC key that derives agent tokens.
// A token is a function of the agent ID, so a container that outlives a
// bridge restart keeps working: its baked-in token verifies again.
func (h *egressHost) loadTokenKey() error {
	dir := filepath.Join(h.f.stateDir, egressSubpath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "token.key")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		h.tokenKey = b
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return err
	}
	h.tokenKey = key
	return nil
}

func (h *egressHost) tokenFor(agentID string) string {
	m := hmac.New(sha256.New, h.tokenKey)
	m.Write([]byte(agentID))
	return hex.EncodeToString(m.Sum(nil))
}

// snapshot copies the current policy.
func (h *egressHost) snapshotLocked() EgressPolicy {
	p := EgressPolicy{Agents: make(map[string]EgressAgentPolicy, len(h.agents))}
	for id, a := range h.agents {
		p.Agents[id] = a.policy
	}
	return p
}

func (h *egressHost) snapshot() EgressPolicy {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// publishLocked wakes policy streams and updates the in-process proxy.
func (h *egressHost) publishLocked() {
	if h.proxy != nil {
		h.proxy.SetPolicy(h.snapshotLocked())
	}
	close(h.changed)
	h.changed = make(chan struct{})
}

func (a *egressAgent) rebuild(allow []string, inject map[string]EgressInjection) {
	mode := a.spec.Mode
	if mode == "" {
		mode = EgressModeOpen
	}
	allowAll := append(append([]string(nil), a.spec.Allow...), allow...)
	a.policy = EgressAgentPolicy{
		Token: a.token, IP: a.ip, Workspace: a.workspace, CA: a.caID, Mode: mode,
		Allow: allowAll, Grants: append([]string(nil), a.grants...), Inject: inject,
		PreviewPorts: append([]int(nil), a.previewPorts...),
	}
}

// Grant adds a per-agent host grant and pushes the new policy.
func (h *egressHost) grant(agentID, host string) bool {
	host = normalizeHost(host)
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[agentID]
	if !ok {
		return false
	}
	for _, g := range a.grants {
		if g == host {
			return true
		}
	}
	a.grants = append(a.grants, host)
	a.policy.Grants = append([]string(nil), a.grants...)
	h.publishLocked()
	return true
}

// setPreviewPorts records the ports an agent's workspace declares for
// preview and pushes the new policy.
func (h *egressHost) setPreviewPorts(agentID string, ports []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.agents[agentID]; ok {
		a.previewPorts = append([]int(nil), ports...)
		a.policy.PreviewPorts = append([]int(nil), ports...)
		h.publishLocked()
	}
}

func (h *egressHost) setIP(agentID, ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.agents[agentID]; ok {
		a.ip = ip
		a.policy.IP = ip
		h.publishLocked()
	}
}

func (h *egressHost) remove(agentID string) {
	h.mu.Lock()
	a, ok := h.agents[agentID]
	var ws string
	if ok {
		ws = a.workspace
		delete(h.agents, agentID)
		h.publishLocked()
	}
	if _, had := h.caGens[agentID]; had {
		delete(h.caGens, agentID)
		h.saveCAGensLocked()
	}
	h.mu.Unlock()
	if ok && ws != "" {
		// A retired CA nobody uses any more can go.
		h.f.pruneRetiredCAs(context.Background(), ws)
	}
}

func (h *egressHost) caGenPath() string {
	return filepath.Join(h.f.stateDir, egressSubpath, "ca-gen.json")
}

func (h *egressHost) saveCAGensLocked() {
	b, err := json.Marshal(h.caGens)
	if err == nil {
		err = os.WriteFile(h.caGenPath(), b, 0o600)
	}
	if err != nil {
		slog.Default().Warn("webbridge: could not persist CA generations", "err", err)
	}
}

// loadCAGens restores agent->CA generation records and reloads the
// retired generations still in use, so the trust files keep them.
func (h *egressHost) loadCAGens(ctx context.Context) {
	b, err := os.ReadFile(h.caGenPath())
	if err != nil {
		return
	}
	var gens map[string]caGen
	if json.Unmarshal(b, &gens) != nil {
		return
	}
	h.mu.Lock()
	h.caGens = gens
	h.mu.Unlock()
	for _, g := range gens {
		if _, err := h.f.caByID(ctx, g.Workspace, g.ID); err != nil {
			slog.Default().Warn("webbridge: CA generation of a running agent is unavailable", "workspace", g.Workspace, "err", err)
		}
	}
	seen := map[string]bool{}
	for _, g := range gens {
		if !seen[g.Workspace] {
			seen[g.Workspace] = true
			h.f.refreshCAFiles(g.Workspace)
		}
	}
}

// setCA records the CA generation an agent uses and pushes the policy.
func (h *egressHost) setCA(agentID, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[agentID]
	if !ok {
		return
	}
	a.caID = id
	a.policy.CA = id
	h.caGens[agentID] = caGen{Workspace: a.workspace, ID: id}
	h.saveCAGensLocked()
	h.publishLocked()
}

// caGenerationsInUse lists the CA generations of workspace that a known
// agent still uses.
func (h *egressHost) caGenerationsInUse(workspace string) map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	for _, g := range h.caGens {
		if g.Workspace == workspace {
			out[g.ID] = true
		}
	}
	for _, a := range h.agents {
		if a.workspace == workspace && a.caID != "" {
			out[a.caID] = true
		}
	}
	return out
}

// ---- policy construction --------------------------------------------------

// injectionValue formats a secret for a header.
func injectionValue(format string, secret []byte) string {
	if format == "" {
		format = "{value}"
	}
	return strings.ReplaceAll(format, "{value}", strings.TrimSpace(string(secret)))
}

// providerHost extracts the host of a provider base URL.
func providerHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return normalizeHost(u.Hostname())
}

// providerHostMap maps provider name to base-URL host, from the control
// agent's config/get. Failure yields an empty map: the proxy then simply
// has no provider hosts to add.
func (f *Fleet) providerHostMap(ctx context.Context) map[string]string {
	if f.providerHosts != nil {
		return f.providerHosts(ctx)
	}
	raw, err := f.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		slog.Default().Debug("webbridge: egress could not read provider hosts", "err", err)
		return nil
	}
	var res struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return nil
	}
	out := map[string]string{}
	for name, p := range res.Providers {
		if h := providerHost(p.BaseURL); h != "" {
			out[name] = h
		}
	}
	return out
}

// buildPolicy resolves a spec and the stored provider keys into the
// allowlist additions and injected headers for one agent. Values are
// fetched through the secret provider now; a missing secret is skipped
// and logged by ref, never by value.
func (f *Fleet) buildPolicy(ctx context.Context, spec EgressSpec) (allow []string, inject map[string]EgressInjection) {
	inject = map[string]EgressInjection{}
	for _, in := range spec.Inject {
		path, err := parseInjectionRef(in.Ref)
		host := normalizeHost(in.Host)
		if err != nil || host == "" || in.Header == "" {
			slog.Default().Warn("webbridge: skipping invalid egress injection", "host", in.Host, "ref", in.Ref)
			continue
		}
		v, err := f.secrets.Get(ctx, DefaultOwnerID, path)
		if err != nil {
			slog.Default().Warn("webbridge: egress injection secret unavailable", "host", host, "ref", in.Ref, "err", err)
			continue
		}
		inject[host] = EgressInjection{Header: in.Header, Value: injectionValue(in.Format, v)}
	}
	// Provider keys held in the vault become Authorization headers for the
	// provider's host, for every workspace; the key never reaches the agent.
	mode := spec.Mode
	provKeys, _ := f.secrets.List(ctx, DefaultOwnerID, "providers/")
	if mode == EgressModeAllowlist || len(provKeys) > 0 {
		hosts := f.providerHostMap(ctx)
		for _, h := range hosts {
			allow = append(allow, h)
		}
		for _, ref := range provKeys {
			name := strings.TrimPrefix(ref, "providers/")
			host, ok := hosts[name]
			if !ok {
				continue
			}
			v, err := f.secrets.Get(ctx, DefaultOwnerID, ref)
			if err != nil {
				continue
			}
			if _, taken := inject[host]; !taken {
				inject[host] = EgressInjection{Header: "Authorization", Value: "Bearer " + strings.TrimSpace(string(v))}
			}
		}
	}
	sort.Strings(allow)
	if len(inject) == 0 {
		inject = nil
	}
	return allow, inject
}

// register creates the agent's policy entry (without an IP yet) and
// returns the token it must present.
func (h *egressHost) register(ctx context.Context, agentID, workspace string, spec EgressSpec) string {
	allow, inject := h.f.buildPolicy(ctx, spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	a := &egressAgent{token: h.tokenFor(agentID), spec: spec, workspace: workspace}
	if old, ok := h.agents[agentID]; ok {
		a.ip, a.grants, a.caID, a.previewPorts = old.ip, old.grants, old.caID, old.previewPorts
	}
	a.rebuild(allow, inject)
	h.agents[agentID] = a
	h.publishLocked()
	return a.token
}

// refreshAll rebuilds every agent's policy from its spec and the current
// secrets, keeping tokens, IPs and grants. Called after a secret changes.
func (h *egressHost) refreshAll(ctx context.Context) {
	h.mu.Lock()
	type job struct {
		id   string
		spec EgressSpec
	}
	var jobs []job
	for id, a := range h.agents {
		jobs = append(jobs, job{id, a.spec})
	}
	h.mu.Unlock()
	changed := false
	for _, j := range jobs {
		allow, inject := h.f.buildPolicy(ctx, j.spec)
		h.mu.Lock()
		if a, ok := h.agents[j.id]; ok {
			before := a.policy
			a.rebuild(allow, inject)
			if !reflect.DeepEqual(before, a.policy) {
				changed = true
			}
		}
		h.mu.Unlock()
	}
	if changed {
		h.mu.Lock()
		h.publishLocked()
		h.mu.Unlock()
	}
}

// refreshLoop re-reads injected secrets on an interval, so a value rotated
// in the backend (OpenBao, the environment) reaches running agents without
// a bridge restart. It publishes only when something changed.
func (h *egressHost) refreshLoop(interval time.Duration) {
	defer h.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			h.refreshAll(ctx)
			cancel()
		case <-h.stop:
			return
		}
	}
}

// ---- control link ---------------------------------------------------------

func (h *egressHost) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /policy", h.servePolicy)
	mux.HandleFunc("POST /log", func(w http.ResponseWriter, r *http.Request) {
		var recs []EgressRecord
		if !decodeJSON(w, r, &recs) {
			return
		}
		if h.f.netlog != nil {
			if err := h.f.netlog.Append(recs); err != nil {
				slog.Default().Warn("webbridge: network log append failed", "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /blocked", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			AgentID string `json:"agentId"`
			Host    string `json:"host"`
		}
		if !decodeJSON(w, r, &b) {
			return
		}
		h.f.noteBlocked(b.AgentID, b.Host)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /leaf", h.serveLeaf)
	return mux
}

// servePolicy streams one JSON policy snapshot per line: the full policy
// on connect and again after every change.
func (h *egressHost) servePolicy(w http.ResponseWriter, r *http.Request) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for {
		h.mu.Lock()
		snap := h.snapshotLocked()
		ch := h.changed
		h.mu.Unlock()
		if err := enc.Encode(snap); err != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		case <-h.stop:
			return
		}
	}
}

type leafResponse struct {
	Cert string `json:"cert"` // PEM chain
	Key  string `json:"key"`  // PKCS#8 PEM
}

// serveLeaf signs a leaf for the sidecar. The sidecar never holds a CA
// key: it receives only the short-lived leaf and its key.
func (h *egressHost) serveLeaf(w http.ResponseWriter, r *http.Request) {
	ws, host := r.URL.Query().Get("workspace"), normalizeHost(r.URL.Query().Get("host"))
	if host == "" || strings.ContainsAny(host, "/ \t\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid host"})
		return
	}
	cert, err := h.f.leafFor(r.Context(), ws, r.URL.Query().Get("ca"), host)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var chain bytes.Buffer
	for _, der := range cert.Certificate {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	writeJSON(w, http.StatusOK, leafResponse{
		Cert: chain.String(),
		Key:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	})
}

func (h *egressHost) controlSocketPath() string {
	return filepath.Join(h.f.stateDir, egressSubpath, "control.sock")
}

// listenControl serves the control link on a mode-0600 Unix socket.
func (h *egressHost) listenControl() error {
	path := h.controlSocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("egress control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	h.ctlSock = ln
	h.ctlSrv = &http.Server{Handler: h.controlHandler(), ReadHeaderTimeout: 10 * time.Second}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		_ = h.ctlSrv.Serve(ln)
	}()
	return nil
}

// ---- process mode ---------------------------------------------------------

// startProcessProxy runs the proxy in this process on loopback.
func (h *egressHost) startProcessProxy() error {
	h.records = make(chan EgressRecord, 1024)
	h.proxy = NewEgressProxy(
		func(ws, ca, host string) (*tls.Certificate, error) {
			return h.f.leafFor(context.Background(), ws, ca, host)
		},
		h.records, h.f.noteBlocked, nil,
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h.proxyAddr = ln.Addr().String()
	h.procSrv = &http.Server{Handler: h.proxy, ReadHeaderTimeout: 10 * time.Second}
	h.wg.Add(2)
	go func() {
		defer h.wg.Done()
		_ = h.procSrv.Serve(ln)
	}()
	go func() {
		defer h.wg.Done()
		h.drainRecords()
	}()
	h.mu.Lock()
	h.proxy.SetPolicy(h.snapshotLocked())
	h.mu.Unlock()
	return nil
}

// drainRecords moves process-mode records into the network log in
// batches.
func (h *egressHost) drainRecords() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var batch []EgressRecord
	flush := func() {
		if len(batch) > 0 && h.f.netlog != nil {
			if err := h.f.netlog.Append(batch); err != nil {
				slog.Default().Warn("webbridge: network log append failed", "err", err)
			}
		}
		batch = nil
	}
	for {
		select {
		case r := <-h.records:
			batch = append(batch, r)
			if len(batch) >= 200 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-h.stop:
			for {
				select {
				case r := <-h.records:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (h *egressHost) close() {
	select {
	case <-h.stop:
		return
	default:
	}
	close(h.stop)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if h.ctlSrv != nil {
		_ = h.ctlSrv.Shutdown(ctx)
	}
	if h.procSrv != nil {
		_ = h.procSrv.Shutdown(ctx)
	}
	h.wg.Wait()
	if h.ctlSock != nil {
		_ = os.Remove(h.controlSocketPath())
	}
}

// proxyURL is the proxy address an agent is given.
func (h *egressHost) proxyURL(agentID, token string) string {
	return "http://" + url.QueryEscape(agentID) + ":" + token + "@" + h.proxyAddr
}

// proxyEnv is the environment that points an agent at the proxy.
func (h *egressHost) proxyEnv(agentID, token string) map[string]string {
	u := h.proxyURL(agentID, token)
	return map[string]string{
		"HTTP_PROXY": u, "HTTPS_PROXY": u, "http_proxy": u, "https_proxy": u,
		"NO_PROXY": "", "no_proxy": "",
	}
}

// caEnv points TLS clients at the workspace bundle.
func caEnv(bundlePath, certPath string) map[string]string {
	return map[string]string{
		"SSL_CERT_FILE": bundlePath, "REQUESTS_CA_BUNDLE": bundlePath,
		"CURL_CA_BUNDLE": bundlePath, "GIT_SSL_CAINFO": bundlePath,
		"NODE_EXTRA_CA_CERTS": certPath,
	}
}

var errEgressOff = errors.New("bridge: egress proxy is not running")

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}
