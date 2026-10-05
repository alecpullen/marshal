package bridge

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// previewEnv is a workspace-spawned agent declaring one preview port.
type previewEnv struct {
	*wsSpawnEnv
	srv  *Server
	id   string
	port int
	now  time.Time
}

func newPreviewEnv(t *testing.T, port int) *previewEnv {
	t.Helper()
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Preview = &WSPreview{Ports: []int{port}}
	e.builtTemplate(t, "svc", doc)
	id, err := e.spawn(t, SpawnOptions{Workspace: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(e.f, "")
	srv.SetPreviewPort(9911)
	pe := &previewEnv{wsSpawnEnv: e, srv: srv, id: id, port: port, now: time.Unix(1_800_000_000, 0)}
	e.f.clock = func() time.Time { return pe.now }
	return pe
}

func (p *previewEnv) do(method, target string, mod func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "example.com:7700"
	if mod != nil {
		mod(req)
	}
	rec := httptest.NewRecorder()
	// Previews are served by their own handler (their own origin); the
	// API by the main one.
	if strings.HasPrefix(req.URL.Path, previewPrefix) {
		p.srv.PreviewHandler().ServeHTTP(rec, req)
	} else {
		p.srv.ServeHTTP(rec, req)
	}
	return rec
}

// issue returns the token and URL for the port.
func (p *previewEnv) issue(t *testing.T, port int) (string, string) {
	t.Helper()
	rec := p.do("POST", "/api/agents/"+p.id+"/preview/"+strconv.Itoa(port), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	i := strings.Index(body, `"url":"`)
	if i < 0 {
		t.Fatalf("body %s", body)
	}
	raw := body[i+len(`"url":"`):]
	raw = raw[:strings.Index(raw, `"`)]
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "example.com:9911" {
		t.Fatalf("preview URL %q is not on the preview origin", raw)
	}
	return u.Query().Get("t"), u.RequestURI()
}

func TestPreviewAuthUndeclaredPortIs403(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	if rec := p.do("POST", "/api/agents/"+p.id+"/preview/4000", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("undeclared = %d %s", rec.Code, rec.Body)
	}
	if rec := p.do("POST", "/api/agents/"+p.id+"/preview/abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad port = %d", rec.Code)
	}
	if rec := p.do("POST", "/api/agents/nope/preview/3000", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", rec.Code)
	}
}

func TestPreviewAuthAgentWithoutAWorkspaceIs403(t *testing.T) {
	e := newWSSpawnEnv(t)
	id, err := e.spawn(t, SpawnOptions{})
	if err != nil {
		t.Skipf("plain spawn unavailable: %v", err)
	}
	rec := httptest.NewRecorder()
	srv := NewServer(e.f, "")
	srv.SetPreviewPort(9911)
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/"+id+"/preview/3000", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no workspace = %d %s", rec.Code, rec.Body)
	}
}

func TestPreviewAuthTokenThenCookieReachesTheForwarder(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	type hit struct {
		id   string
		port int
		rest string
		q    string
	}
	var hits []hit
	p.f.previewForward = func(w http.ResponseWriter, r *http.Request, id string, port int, rest string) {
		hits = append(hits, hit{id, port, rest, r.URL.RawQuery})
		w.WriteHeader(http.StatusTeapot)
	}
	tok, raw := p.issue(t, 3000)
	if len(tok) < 30 || !strings.HasPrefix(raw, "/preview/"+p.id+"/3000/?t=") {
		t.Fatalf("url = %q", raw)
	}

	// First request: the token sets the cookie and redirects without it.
	rec := p.do("GET", raw+"&x=1", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("token request = %d %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/preview/"+p.id+"/3000/?x=1" {
		t.Fatalf("redirect = %q", loc)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != "mp_"+p.id+"_3000" || cs[0].Value != tok {
		t.Fatalf("cookies = %+v", cs)
	}
	c := cs[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/preview/"+p.id+"/3000/" || c.Secure {
		t.Fatalf("cookie attrs = %+v", c)
	}
	if len(hits) != 0 {
		t.Fatal("the token request must not reach the app")
	}

	// Second request: the cookie authenticates and the app is reached.
	rec = p.do("GET", "/preview/"+p.id+"/3000/app/main.js?v=2", func(r *http.Request) { r.AddCookie(c) })
	if rec.Code != http.StatusTeapot || len(hits) != 1 || hits[0] != (hit{p.id, 3000, "/app/main.js", "v=2"}) {
		t.Fatalf("cookie request = %d, hits %+v", rec.Code, hits)
	}
}

func TestPreviewAuthSecureCookieOnTLS(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	_, raw := p.issue(t, 3000)
	rec := p.do("GET", raw, func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	cs := rec.Result().Cookies()
	if len(cs) != 1 || !cs[0].Secure {
		t.Fatalf("cookies = %+v", cs)
	}
}

func TestPreviewAuthFailuresAre404(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	called := false
	p.f.previewForward = func(http.ResponseWriter, *http.Request, string, int, string) { called = true }
	tok, _ := p.issue(t, 3000)
	cookie := &http.Cookie{Name: "mp_" + p.id + "_3000", Value: tok}

	for name, req := range map[string]func() *httptest.ResponseRecorder{
		"no credentials": func() *httptest.ResponseRecorder { return p.do("GET", "/preview/"+p.id+"/3000/", nil) },
		"bad token":      func() *httptest.ResponseRecorder { return p.do("GET", "/preview/"+p.id+"/3000/?t=nope", nil) },
		"bad cookie": func() *httptest.ResponseRecorder {
			return p.do("GET", "/preview/"+p.id+"/3000/", func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: cookie.Name, Value: "nope"})
			})
		},
		"token for another port": func() *httptest.ResponseRecorder {
			return p.do("GET", "/preview/"+p.id+"/3001/?t="+tok, nil)
		},
		"cookie on another port": func() *httptest.ResponseRecorder {
			return p.do("GET", "/preview/"+p.id+"/3001/", func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "mp_" + p.id + "_3001", Value: tok})
			})
		},
		"token for another agent": func() *httptest.ResponseRecorder {
			return p.do("GET", "/preview/other/3000/?t="+tok, nil)
		},
		"malformed path": func() *httptest.ResponseRecorder { return p.do("GET", "/preview/"+p.id+"/notaport/", nil) },
	} {
		if rec := req(); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
	if called {
		t.Fatal("a rejected request reached the forwarder")
	}
}

func TestPreviewAuthExpiredTokenIs404(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	called := false
	p.f.previewForward = func(http.ResponseWriter, *http.Request, string, int, string) { called = true }
	tok, raw := p.issue(t, 3000)
	cookie := &http.Cookie{Name: "mp_" + p.id + "_3000", Value: tok}

	p.now = p.now.Add(previewTokenTTL + time.Second)
	if rec := p.do("GET", raw, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expired token = %d", rec.Code)
	}
	if rec := p.do("GET", "/preview/"+p.id+"/3000/", func(r *http.Request) { r.AddCookie(cookie) }); rec.Code != http.StatusNotFound {
		t.Fatalf("expired cookie = %d", rec.Code)
	}
	if called {
		t.Fatal("expired credentials reached the forwarder")
	}
}

func TestPreviewTokensDieWithTheAgent(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	tok, _ := p.issue(t, 3000)
	p.f.stopAgent(p.id)
	if p.f.previewTokenValid(tok, p.id, 3000) {
		t.Fatal("a token outlived its agent")
	}
}

func TestPreviewIsServedFromItsOwnOrigin(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	_, raw := p.issue(t, 3000)
	called := false
	p.f.previewForward = func(w http.ResponseWriter, r *http.Request, _ string, _ int, _ string) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}
	tok, _ := p.issue(t, 3000)
	cookie := &http.Cookie{Name: "mp_" + p.id + "_3000", Value: tok}
	_ = raw

	// The API origin never serves /preview, even with a valid cookie: a
	// page the agent serves there would share the UI's sessionStorage.
	req := httptest.NewRequest("GET", "/preview/"+p.id+"/3000/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	p.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || called {
		t.Fatalf("API origin served a preview: %d", rec.Code)
	}

	// The preview origin serves nothing but previews: no /api, no UI.
	for _, path := range []string{"/api/agents", "/api/config", "/", "/index.html"} {
		rec := httptest.NewRecorder()
		p.srv.PreviewHandler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("preview origin served %s: %d", path, rec.Code)
		}
	}
}

func TestPreviewIssueNeedsAPreviewListener(t *testing.T) {
	e := newWSSpawnEnv(t)
	srv := NewServer(e.f, "")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/a/preview/3000", nil))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "preview_unconfigured") {
		t.Fatalf("no listener = %d %s", rec.Code, rec.Body)
	}
}

func TestPreviewOriginUsesTheCallersHostAndScheme(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "bridge.lan:7700"
	if got, _ := s.previewOrigin(req, 9000, "a"); got != "http://bridge.lan:9000" {
		t.Fatalf("origin = %q", got)
	}
	req.TLS = &tls.ConnectionState{}
	req.Host = "[2001:db8::1]:7700"
	if got, _ := s.previewOrigin(req, 9000, "a"); got != "https://[2001:db8::1]:9000" {
		t.Fatalf("v6 origin = %q", got)
	}
	s.publicURLBase = "https://studio.example.com"
	if got, _ := s.previewOrigin(req, 9000, "a"); got != "https://studio.example.com:9000" {
		t.Fatalf("public origin = %q", got)
	}
}

func TestPreviewOriginCarriesTheTokenOnLocalhost(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "localhost:7700"
	a, inHost := s.previewOrigin(req, 9000, "aaaa")
	b, _ := s.previewOrigin(req, 9000, "bbbb")
	if !inHost || a != "http://paaaa.localhost:9000" || a == b {
		t.Fatalf("origins %q, %q (inHost %v)", a, b, inHost)
	}
	req.Host = "x.localhost:7700"
	if got, _ := s.previewOrigin(req, 9000, "aaaa"); got != a {
		t.Fatalf("from a localhost subdomain: %q, want %q", got, a)
	}
	for _, h := range []string{"127.0.0.1:7700", "[::1]:7700"} {
		req.Host = h
		if got, in := s.previewOrigin(req, 9000, "aaaa"); got != a || !in {
			t.Fatalf("loopback %s: %q, want %q", h, got, a)
		}
	}
	req.Host = "192.168.1.5:7700"
	if got, in := s.previewOrigin(req, 9000, "aaaa"); got != "http://192.168.1.5:9000" || in {
		t.Fatalf("a LAN IP cannot be subdivided: %q", got)
	}
}

func TestPreviewOnLocalhostNeedsNoCookie(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	var got []string
	p.f.previewForward = func(w http.ResponseWriter, r *http.Request, id string, port int, rest string) {
		got = append(got, id+"|"+rest)
		w.WriteHeader(http.StatusOK)
	}
	rec := p.do("POST", "/api/agents/"+p.id+"/preview/3000", func(r *http.Request) { r.Host = "localhost:7700" })
	if rec.Code != http.StatusOK {
		t.Fatalf("issue = %d %s", rec.Code, rec.Body)
	}
	var out struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(out.URL)
	if !strings.HasSuffix(u.Host, ".localhost:9911") || !strings.HasPrefix(u.Host, "p") || u.RawQuery != "" {
		t.Fatalf("url %q: want the token in the host and no query", out.URL)
	}
	if rec := p.do("GET", u.Path, func(r *http.Request) { r.Host = u.Host }); rec.Code != http.StatusOK {
		t.Fatalf("own origin = %d", rec.Code)
	}
	// Another token's origin, a bare localhost, and a cookie-less other
	// host label are refused.
	for _, host := range []string{"p" + strings.Repeat("0", 48) + ".localhost:9911", "localhost:9911", "agent.localhost:9911"} {
		if rec := p.do("GET", u.Path, func(r *http.Request) { r.Host = host }); rec.Code != http.StatusNotFound {
			t.Errorf("host %s = %d, want 404", host, rec.Code)
		}
	}
	// A direct request by loopback IP carries no token in its host.
	if rec := p.do("GET", u.Path, func(r *http.Request) { r.Host = "127.0.0.1:9911" }); rec.Code != http.StatusNotFound {
		t.Errorf("loopback IP host = %d, want 404", rec.Code)
	}
	// A token for another port is no use on this one.
	if rec := p.do("GET", "/preview/"+p.id+"/4000/", func(r *http.Request) { r.Host = u.Host }); rec.Code != http.StatusNotFound {
		t.Errorf("other port = %d", rec.Code)
	}
}

// ---- forwarding -----------------------------------------------------------

func listenPort(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPreviewForwardProcessModeReachesThePort(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		_, _ = io.WriteString(w, "hello from the app")
	}))
	defer up.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(up.URL[strings.LastIndex(up.URL, ":"):], ":"))

	p := newPreviewEnv(t, port)
	tok, _ := p.issue(t, port)
	cookie := &http.Cookie{Name: "mp_" + p.id + "_" + strconv.Itoa(port), Value: tok}
	rec := p.do("GET", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/a/b.css?x=1", func(r *http.Request) {
		r.AddCookie(cookie)
		r.AddCookie(&http.Cookie{Name: "session", Value: "keep"})
		r.Header.Set("Authorization", "Bearer bridge-secret")
	})
	if rec.Code != http.StatusOK || rec.Body.String() != "hello from the app" {
		t.Fatalf("forward = %d %q", rec.Code, rec.Body)
	}
	if gotPath != "/a/b.css" || gotQuery != "x=1" {
		t.Fatalf("upstream saw %q ? %q", gotPath, gotQuery)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization leaked: %q", gotAuth)
	}
	if gotCookie != "session=keep" {
		t.Fatalf("upstream cookies = %q, want only the app's own", gotCookie)
	}
}

func TestPreviewOnLocalhostSendsNoReferrer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "unsafe-url")
		_, _ = io.WriteString(w, "hi")
	}))
	defer up.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(up.URL[strings.LastIndex(up.URL, ":"):], ":"))
	p := newPreviewEnv(t, port)
	rec := p.do("POST", "/api/agents/"+p.id+"/preview/"+strconv.Itoa(port), func(r *http.Request) { r.Host = "127.0.0.1:7700" })
	var out struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(out.URL)
	rec = p.do("GET", u.Path, func(r *http.Request) { r.Host = u.Host })
	if rec.Code != http.StatusOK || rec.Body.String() != "hi" {
		t.Fatalf("forward = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != "no-referrer" {
		t.Fatalf("Referrer-Policy = %v, want only no-referrer", got)
	}
}

func TestPreviewForwardNothingListeningIs502(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := listenPort(t, ln)
	ln.Close()
	p := newPreviewEnv(t, port)
	tok, _ := p.issue(t, port)
	rec := p.do("GET", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "mp_" + p.id + "_" + strconv.Itoa(port), Value: tok})
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("closed port = %d", rec.Code)
	}
}

// upgradeServer is a minimal WebSocket-style upgrader: it answers the
// handshake with 101 and then echoes bytes.
func upgradeServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		hj := w.(http.Hijacker)
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln
}

// handshakeAndEcho dials addr, sends a WebSocket upgrade for path with the
// given cookie and checks that bytes echo back.
func handshakeAndEcho(t *testing.T, addr, host, path, cookie string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nCookie: " + cookie + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("handshake status %q, %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, "ping-through-the-proxy"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("ping-through-the-proxy"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping-through-the-proxy" {
		t.Fatalf("echo %q, %v", buf, err)
	}
}

func TestPreviewForwardWebSocketUpgradePassesThrough(t *testing.T) {
	ln := upgradeServer(t)
	port := listenPort(t, ln)
	p := newPreviewEnv(t, port)
	tok, _ := p.issue(t, port)

	bridge := httptest.NewServer(p.srv.PreviewHandler())
	defer bridge.Close()
	cookie := "mp_" + p.id + "_" + strconv.Itoa(port) + "=" + tok
	addr := strings.TrimPrefix(bridge.URL, "http://")
	// Another host: the ?t= token and cookie path.
	handshakeAndEcho(t, addr, "bridge.lan:7700", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/ws", cookie)
	// A localhost name: the token in the host, no cookie.
	handshakeAndEcho(t, addr, "p"+tok+".localhost:9911", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/ws", "")
}

// ---- the sidecar's preview listener ---------------------------------------

func TestEgressPreviewHandlerForwardsDeclaredPortsOnly(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path + "?" + r.URL.RawQuery + " auth=" + r.Header.Get(previewAuthHeader)
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	port, _ := strconv.Atoi(up.URL[strings.LastIndex(up.URL, ":")+1:])

	px := NewEgressProxy(nil, nil, nil, nil)
	px.SetPolicy(EgressPolicy{Agents: map[string]EgressAgentPolicy{
		"a1": {Token: "tok-a1", IP: "127.0.0.1", PreviewPorts: []int{port}},
		"a2": {Token: "tok-a2", IP: "127.0.0.1", PreviewPorts: []int{1}},
		"a3": {Token: "tok-a3", PreviewPorts: []int{port}}, // address not pinned yet
	}})
	h := httptest.NewServer(px.PreviewHandler())
	defer h.Close()

	get := func(path, auth string) (int, string) {
		req, _ := http.NewRequest("GET", h.URL+path, nil)
		if auth != "" {
			req.Header.Set(previewAuthHeader, auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	ps := strconv.Itoa(port)
	if code, body := get("/a1/"+ps+"/x/y?z=1", "tok-a1"); code != 200 || body != "ok" || seen != "/x/y?z=1 auth=" {
		t.Fatalf("declared port = %d %q (upstream saw %q)", code, body, seen)
	}
	if code, _ := get("/a1/"+strconv.Itoa(port+1)+"/", "tok-a1"); code != http.StatusForbidden {
		t.Fatalf("undeclared port = %d, want 403", code)
	}
	if code, _ := get("/a2/"+ps+"/", "tok-a2"); code != http.StatusForbidden {
		t.Fatalf("another agent's port list = %d, want 403", code)
	}
	if code, _ := get("/a1/"+ps+"/", ""); code != http.StatusNotFound {
		t.Fatalf("no auth = %d, want 404", code)
	}
	if code, _ := get("/a1/"+ps+"/", "tok-a2"); code != http.StatusNotFound {
		t.Fatalf("another agent's token = %d, want 404", code)
	}
	if code, _ := get("/nobody/"+ps+"/", "tok-a1"); code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d, want 404", code)
	}
	if code, _ := get("/a3/"+ps+"/", "tok-a3"); code != http.StatusBadGateway {
		t.Fatalf("unpinned address = %d, want 502", code)
	}
}

func TestEgressPreviewHandlerWebSocketUpgradePassesThrough(t *testing.T) {
	ln := upgradeServer(t)
	port := listenPort(t, ln)
	px := NewEgressProxy(nil, nil, nil, nil)
	px.SetPolicy(EgressPolicy{Agents: map[string]EgressAgentPolicy{
		"a1": {Token: "tok", IP: "127.0.0.1", PreviewPorts: []int{port}},
	}})
	h := httptest.NewServer(px.PreviewHandler())
	defer h.Close()
	addr := strings.TrimPrefix(h.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /a1/"+strconv.Itoa(port)+"/ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+previewAuthHeader+": tok\r\n\r\n")
	br := bufio.NewReader(conn)
	if status, _ := br.ReadString('\n'); !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
}

// TestPreviewForwardContainerModeGoesThroughTheSidecar wires the bridge
// forwarder to a real sidecar handler and an upstream app.
func TestPreviewForwardContainerModeGoesThroughTheSidecar(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "app saw "+r.URL.Path)
	}))
	defer up.Close()
	port, _ := strconv.Atoi(up.URL[strings.LastIndex(up.URL, ":")+1:])

	p := newPreviewEnv(t, port)
	h := newEgressHost(p.f)
	h.mode = egressModeContainer
	h.tokenKey = []byte("0123456789abcdef0123456789abcdef")
	p.f.egress = h
	px := NewEgressProxy(nil, nil, nil, nil)
	px.SetPolicy(EgressPolicy{Agents: map[string]EgressAgentPolicy{
		p.id: {Token: h.tokenFor(p.id), IP: "127.0.0.1", PreviewPorts: []int{port}},
	}})
	sidecar := httptest.NewServer(px.PreviewHandler())
	defer sidecar.Close()
	sp, _ := strconv.Atoi(sidecar.URL[strings.LastIndex(sidecar.URL, ":")+1:])
	h.previewPort.Store(int32(sp))
	rt, err := p.f.runtimeForAgent(p.id)
	if err != nil {
		t.Fatal(err)
	}
	p.f.mu.Lock()
	rt.child.Containerized = true
	p.f.mu.Unlock()

	tok, _ := p.issue(t, port)
	rec := p.do("GET", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/hello", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "mp_" + p.id + "_" + strconv.Itoa(port), Value: tok})
	})
	if rec.Code != http.StatusOK || rec.Body.String() != "app saw /hello" {
		t.Fatalf("via sidecar = %d %q", rec.Code, rec.Body)
	}

	// Without a published preview port the bridge says so instead of
	// reaching for a random loopback port.
	h.previewPort.Store(0)
	rec = p.do("GET", "/preview/"+p.id+"/"+strconv.Itoa(port)+"/hello", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "mp_" + p.id + "_" + strconv.Itoa(port), Value: tok})
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("no preview port = %d", rec.Code)
	}
}

func TestEgressSidecarPublishesThePreviewListener(t *testing.T) {
	args := strings.Join(egressSidecarArgs("docker", "marshal-state", "marshal-egress:dev"), " ")
	for _, want := range []string{"-p 127.0.0.1::8081", "--preview-listen :8081", "--listen :3128"} {
		if !strings.Contains(args, want) {
			t.Errorf("sidecar args lack %q:\n%s", want, args)
		}
	}
}

func TestParsePublishedPort(t *testing.T) {
	for in, want := range map[string]int{
		"127.0.0.1:49153\n":           49153,
		"0.0.0.0:49153\n[::]:49153\n": 49153,
		"":                            0,
		"no mapping":                  0,
		"127.0.0.1:99999":             0,
		"[::1]:8081\n":                8081,
		"127.0.0.1:notaport":          0,
	} {
		if got := parsePublishedPort(in); got != want {
			t.Errorf("parsePublishedPort(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestReadPreviewPortStoresTheHostPort(t *testing.T) {
	e := newWSSpawnEnv(t)
	e.imgs.out["port"] = "127.0.0.1:50321\n"
	h := newEgressHost(e.f)
	if !e.f.readPreviewPort(h, "docker") || h.previewPort.Load() != 50321 {
		t.Fatalf("port = %d", h.previewPort.Load())
	}
	e.imgs.out["port"] = ""
	h2 := newEgressHost(e.f)
	if e.f.readPreviewPort(h2, "docker") {
		t.Fatal("an empty mapping counted as published")
	}
}

func TestPreviewStopsWhenThePortIsNoLongerDeclared(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	hits := 0
	p.f.previewForward = func(w http.ResponseWriter, r *http.Request, _ string, _ int, _ string) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}
	tok, _ := p.issue(t, 3000)
	get := func() int {
		return p.do("GET", "/preview/"+p.id+"/3000/", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "mp_" + p.id + "_3000", Value: tok})
		}).Code
	}
	if get() != http.StatusNoContent {
		t.Fatal("declared port was refused")
	}
	// The workspace drops the port.
	st := p.f.previewTokens()
	st.mu.Lock()
	st.ports[p.id] = previewPortsEntry{at: p.now}
	st.mu.Unlock()
	if get() != http.StatusNotFound || hits != 1 {
		t.Fatal("a port that is no longer declared was still served")
	}
	// Once the cache expires the declaration is read again.
	p.now = p.now.Add(previewPortsTTL + time.Second)
	if get() != http.StatusNoContent {
		t.Fatal("cache never refreshed")
	}
}

func TestPreviewRefusesOddAgentIDs(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	for _, id := range []string{"a;b", "a%3Bb", "a b", ".."} {
		if _, _, _, ok := parsePreviewPath("/preview/" + id + "/3000/"); ok {
			t.Errorf("accepted agent id %q", id)
		}
	}
	if _, err := p.f.IssuePreview(ctlContext(t), "a;b", 3000); err == nil {
		t.Fatal("issued a token for an odd agent id")
	}
}
