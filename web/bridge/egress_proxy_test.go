package bridge

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type egressRig struct {
	proxy   *EgressProxy
	srv     *httptest.Server
	records chan EgressRecord
	mu      sync.Mutex
	blocked []string
	ca      *caCache
}

func newEgressRig(t *testing.T) *egressRig {
	t.Helper()
	cert, certPEM, key, err := generateWorkspaceCA("t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rig := &egressRig{records: make(chan EgressRecord, 64), ca: newCACache(cert, certPEM, key)}
	rig.proxy = NewEgressProxy(
		func(workspace, ca, host string) (*tls.Certificate, error) { return rig.ca.leaf(host) },
		rig.records,
		func(agentID, host string) {
			rig.mu.Lock()
			rig.blocked = append(rig.blocked, agentID+"|"+host)
			rig.mu.Unlock()
		},
		nil,
	)
	rig.srv = httptest.NewServer(rig.proxy)
	t.Cleanup(rig.srv.Close)
	return rig
}

func (r *egressRig) blockedList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.blocked...)
}

func (r *egressRig) setPolicy(agents map[string]EgressAgentPolicy) {
	r.proxy.SetPolicy(EgressPolicy{Agents: agents})
}

// client returns an HTTP client that goes through the proxy as agent.
func (r *egressRig) client(agent, token string, roots *x509.CertPool) *http.Client {
	pu, _ := url.Parse(r.srv.URL)
	pu.User = url.UserPassword(agent, token)
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{RootCAs: roots},
		},
	}
}

func (r *egressRig) record(t *testing.T) EgressRecord {
	t.Helper()
	select {
	case rec := <-r.records:
		return rec
	case <-time.After(3 * time.Second):
		t.Fatal("no record emitted")
		return EgressRecord{}
	}
}

func proxyGet(t *testing.T, c *http.Client, u string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func plainUpstream(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Header.Clone()
		last.Set("X-Seen-Host", r.Host)
		mu.Unlock()
		io.WriteString(w, "hello from upstream")
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header { mu.Lock(); defer mu.Unlock(); return last }
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	return u.Hostname()
}

func TestEgressProxyAllowlistAllowsPlainAndTunnel(t *testing.T) {
	rig := newEgressRig(t)
	up, _ := plainUpstream(t)
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls ok") }))
	defer tlsUp.Close()
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": {Token: "tok", Workspace: "t", Mode: EgressModeAllowlist, Allow: []string{hostOf(t, up.URL)}}})

	c := rig.client("a1", "tok", nil)
	if code, body := proxyGet(t, c, up.URL, nil); code != 200 || body != "hello from upstream" {
		t.Fatalf("plain = %d %q", code, body)
	}
	rec := rig.record(t)
	if rec.Decision != "allow" || rec.AgentID != "a1" || rec.Host != "127.0.0.1" || rec.BytesDown == 0 || rec.Injected {
		t.Fatalf("plain record = %+v", rec)
	}

	pool := x509.NewCertPool()
	pool.AddCert(tlsUp.Certificate())
	c = rig.client("a1", "tok", pool)
	if code, body := proxyGet(t, c, tlsUp.URL, nil); code != 200 || body != "tls ok" {
		t.Fatalf("tunnel = %d %q", code, body)
	}
	c.CloseIdleConnections()
	rec = rig.record(t)
	if rec.Decision != "allow" || rec.BytesUp == 0 || rec.BytesDown == 0 || rec.Injected {
		t.Fatalf("tunnel record = %+v", rec)
	}
}

func TestEgressProxyBlocksUnlistedHost(t *testing.T) {
	rig := newEgressRig(t)
	up, _ := plainUpstream(t)
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": {Token: "tok", Workspace: "t", Mode: EgressModeAllowlist, Allow: []string{"example.com"}}})
	c := rig.client("a1", "tok", nil)
	if code, _ := proxyGet(t, c, up.URL, nil); code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", code)
	}
	rec := rig.record(t)
	if rec.Decision != "block" || rec.Host != "127.0.0.1" {
		t.Fatalf("record = %+v", rec)
	}
	if got := rig.blockedList(); len(got) != 1 || got[0] != "a1|127.0.0.1" {
		t.Fatalf("blocked callback = %v", got)
	}
	// CONNECT is refused with 403 too.
	tlsUp := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsUp.Close()
	pool := x509.NewCertPool()
	pool.AddCert(tlsUp.Certificate())
	if _, err := rig.client("a1", "tok", pool).Get(tlsUp.URL); err == nil {
		t.Fatal("CONNECT to a blocked host succeeded")
	}
	if rec := rig.record(t); rec.Decision != "block" {
		t.Fatalf("CONNECT record = %+v", rec)
	}
}

func TestEgressProxyOffAndOpenModes(t *testing.T) {
	rig := newEgressRig(t)
	up, _ := plainUpstream(t)
	rig.setPolicy(map[string]EgressAgentPolicy{
		"off":     {Token: "t1", Mode: EgressModeOff, Allow: []string{"127.0.0.1"}},
		"open":    {Token: "t2", Mode: EgressModeOpen},
		"unknown": {Token: "t3", Mode: "wide-open", Allow: []string{"127.0.0.1"}},
		"empty":   {Token: "t4", Allow: []string{"127.0.0.1"}},
	})
	if code, _ := proxyGet(t, rig.client("off", "t1", nil), up.URL, nil); code != http.StatusForbidden {
		t.Errorf("off = %d", code)
	}
	if code, _ := proxyGet(t, rig.client("unknown", "t3", nil), up.URL, nil); code != http.StatusForbidden {
		t.Errorf("unknown mode = %d (must fail closed)", code)
	}
	if code, _ := proxyGet(t, rig.client("empty", "t4", nil), up.URL, nil); code != http.StatusForbidden {
		t.Errorf("empty mode = %d (must fail closed)", code)
	}
	if code, _ := proxyGet(t, rig.client("open", "t2", nil), up.URL, nil); code != 200 {
		t.Errorf("open = %d", code)
	}
}

func TestEgressProxyAuthentication(t *testing.T) {
	rig := newEgressRig(t)
	up, _ := plainUpstream(t)
	rig.setPolicy(map[string]EgressAgentPolicy{
		"a1":  {Token: "tok", Mode: EgressModeOpen},
		"pin": {Token: "tok", Mode: EgressModeOpen, IP: "10.9.9.9"},
		"ok":  {Token: "tok", Mode: EgressModeOpen, IP: "127.0.0.1"},
		"nt":  {Mode: EgressModeOpen},
	})
	for name, c := range map[string]*http.Client{
		"wrong token":   rig.client("a1", "nope", nil),
		"unknown agent": rig.client("ghost", "tok", nil),
		"wrong ip":      rig.client("pin", "tok", nil),
		"empty token":   rig.client("nt", "", nil),
	} {
		if code, _ := proxyGet(t, c, up.URL, nil); code != http.StatusProxyAuthRequired {
			t.Errorf("%s = %d, want 407", name, code)
		}
	}
	if code, _ := proxyGet(t, rig.client("ok", "tok", nil), up.URL, nil); code != 200 {
		t.Errorf("matching ip = %d", code)
	}
	// No credentials at all.
	pu, _ := url.Parse(rig.srv.URL)
	bare := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if code, _ := proxyGet(t, bare, up.URL, nil); code != http.StatusProxyAuthRequired {
		t.Errorf("no auth = %d", code)
	}
	select {
	case rec := <-rig.records:
		if rec.Decision != "allow" {
			t.Errorf("only allowed requests should be recorded, got %+v", rec)
		}
	case <-time.After(time.Second):
	}
}

func TestEgressProxyGrantAppliesWithoutRestart(t *testing.T) {
	rig := newEgressRig(t)
	up, _ := plainUpstream(t)
	pol := EgressAgentPolicy{Token: "tok", Mode: EgressModeAllowlist, Allow: []string{"example.com"}}
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": pol})
	c := rig.client("a1", "tok", nil)
	if code, _ := proxyGet(t, c, up.URL, nil); code != http.StatusForbidden {
		t.Fatalf("before grant = %d", code)
	}
	pol.Grants = []string{"127.0.0.1"}
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": pol})
	if code, _ := proxyGet(t, c, up.URL, nil); code != 200 {
		t.Fatalf("after grant = %d", code)
	}
}

func TestEgressHostMatching(t *testing.T) {
	cases := []struct {
		pat, host string
		want      bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "EXAMPLE.com.", true},
		{"example.com", "a.example.com", false},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "badexample.com", false},
		{"*.", "x", false},
		{"", "x", false},
		{"10.0.0.1", "10.0.0.1", true},
	}
	for _, c := range cases {
		if got := hostMatches(c.pat, c.host); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v, want %v", c.pat, c.host, got, c.want)
		}
	}
}

// injectRig sets up an injected TLS upstream the proxy reaches with a
// trusting upstream config, and a client that trusts the workspace CA.
func injectRig(t *testing.T) (*egressRig, *httptest.Server, func() http.Header, *x509.CertPool) {
	t.Helper()
	rig := newEgressRig(t)
	var mu sync.Mutex
	var last http.Header
	var lastHost string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last, lastHost = r.Header.Clone(), r.Host
		mu.Unlock()
		w.Header().Set("X-Upstream", "yes")
		io.WriteString(w, "secret-bearing response for "+r.URL.Path)
	}))
	t.Cleanup(up.Close)
	upPool := x509.NewCertPool()
	upPool.AddCert(up.Certificate())
	rig.proxy.upstream = &tls.Config{RootCAs: upPool}
	roots := x509.NewCertPool()
	roots.AddCert(rig.ca.cert)
	return rig, up, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		h := last.Clone()
		h.Set("X-Seen-Host", lastHost)
		return h
	}, roots
}

func TestEgressProxyInjectsHeaderUpstreamOnly(t *testing.T) {
	rig, up, seen, roots := injectRig(t)
	host := hostOf(t, up.URL)
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": {
		Token: "tok", Workspace: "t", Mode: EgressModeAllowlist, Allow: []string{host},
		Inject: map[string]EgressInjection{host: {Header: "Authorization", Value: "Bearer s3cret"}},
	}})
	c := rig.client("a1", "tok", roots)
	// The client tries to spoof the header and the Host.
	code, body := proxyGet(t, c, up.URL+"/v1/x", map[string]string{"Authorization": "Bearer client-forged", "Host": "evil.example"})
	if code != 200 || !strings.Contains(body, "/v1/x") {
		t.Fatalf("got %d %q", code, body)
	}
	h := seen()
	if got := h.Values("Authorization"); len(got) != 1 || got[0] != "Bearer s3cret" {
		t.Fatalf("upstream Authorization = %v", got)
	}
	if h.Get("Proxy-Authorization") != "" {
		t.Error("proxy credentials reached the upstream")
	}
	if strings.Contains(h.Get("X-Seen-Host"), "evil") {
		t.Errorf("upstream saw client Host %q; the credential must only go to the CONNECT target", h.Get("X-Seen-Host"))
	}
	if strings.Contains(body, "s3cret") {
		t.Error("the client received the credential")
	}
	c.CloseIdleConnections()
	rec := rig.record(t)
	if !rec.Injected || rec.Decision != "allow" || rec.BytesUp == 0 || rec.BytesDown == 0 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestEgressProxyInjectionOverKeepAliveAndPolicyChange(t *testing.T) {
	rig, up, seen, roots := injectRig(t)
	host := hostOf(t, up.URL)
	pol := EgressAgentPolicy{
		Token: "tok", Workspace: "t", Mode: EgressModeAllowlist, Allow: []string{host},
		Inject: map[string]EgressInjection{host: {Header: "X-Api-Key", Value: "k1"}},
	}
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": pol})
	c := rig.client("a1", "tok", roots)
	for i := 0; i < 3; i++ {
		if code, _ := proxyGet(t, c, up.URL, nil); code != 200 {
			t.Fatalf("request %d = %d", i, code)
		}
	}
	if seen().Get("X-Api-Key") != "k1" {
		t.Fatal("not injected on a reused connection")
	}
	// A rotated credential applies to the next request on the same
	// connection, and a revoked host is refused.
	pol.Inject = map[string]EgressInjection{host: {Header: "X-Api-Key", Value: "k2"}}
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": pol})
	if code, _ := proxyGet(t, c, up.URL, nil); code != 200 || seen().Get("X-Api-Key") != "k2" {
		t.Fatalf("rotated credential not applied (%q)", seen().Get("X-Api-Key"))
	}
	pol.Mode = EgressModeOff
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": pol})
	req, _ := http.NewRequest(http.MethodGet, up.URL, nil)
	resp, err := c.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("revoked host still served on an open connection")
		}
	}
}

func TestEgressProxyNonInjectedHostIsNotIntercepted(t *testing.T) {
	rig, up, _, roots := injectRig(t)
	host := hostOf(t, up.URL)
	// Injection is configured for a different host: this one is tunnelled
	// untouched, so the client must verify the real certificate.
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": {
		Token: "tok", Workspace: "t", Mode: EgressModeOpen,
		Inject: map[string]EgressInjection{"other.example": {Header: "Authorization", Value: "x"}},
	}})
	if _, err := rig.client("a1", "tok", roots).Get(up.URL); err == nil {
		t.Fatal("client trusted only the workspace CA, so a real tunnel to the test server must fail verification")
	}
	upPool := x509.NewCertPool()
	upPool.AddCert(up.Certificate())
	if code, _ := proxyGet(t, rig.client("a1", "tok", upPool), up.URL, nil); code != 200 {
		t.Fatalf("tunnel with the real cert = %d (%s)", code, host)
	}
}

func TestEgressProxyRejectsNonProxyRequests(t *testing.T) {
	rig := newEgressRig(t)
	rig.setPolicy(map[string]EgressAgentPolicy{"a1": {Token: "tok", Mode: EgressModeOpen}})
	req, _ := http.NewRequest(http.MethodGet, rig.srv.URL+"/relative", nil)
	req.SetBasicAuth("a1", "tok")
	req.Header.Set("Proxy-Authorization", "Basic YTE6dG9r") // a1:tok
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("relative request = %d, want 400", resp.StatusCode)
	}
}
