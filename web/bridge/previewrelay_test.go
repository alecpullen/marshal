package bridge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// remotePreview is a forwarded host's preview listener: an HTTP server on a
// loopback port this machine can dial, reported as that host's PreviewPort with
// the host live.
func remotePreview(t *testing.T, s *Server, name string, h http.Handler) int {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse %q: %v", up.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("%q has no port: %v", up.URL, err)
	}
	host := liveHost(name, "/tmp/"+name+".sock")
	host.PreviewPort = port
	reportHosts(t, s, host)
	return port
}

// previewAs sends one request to this daemon's preview listener, addressed to
// host.
func previewAs(s *Server, host, target string, mod func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	if mod != nil {
		mod(req)
	}
	rec := httptest.NewRecorder()
	s.PreviewHandler().ServeHTTP(rec, req)
	return rec
}

// templatedRelayServer is a daemon that publishes a templated preview_url, so a
// link it relays carries its token in the host.
func templatedRelayServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(testFleet(t))
	if err := s.SetPreviewURL("https://p{token}.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestARecordedTemplatedPreviewIsRelayed: a request for a recorded link on a
// templated origin is carried to the host that issued it, as the host's own
// listener reads it — the token in a localhost host, and the path unchanged
// (one-daemon open question 1).
func TestARecordedTemplatedPreviewIsRelayed(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	got := make(chan *http.Request, 1)
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(r.Context())
		_, _ = io.WriteString(w, "the host's page")
	}))
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	rec := previewAs(s, "p"+tok+".preview.example.com", "/preview/a1/3000/app/main.js", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "the host's page" {
		t.Fatalf("the relay = %d %q, want the host's own answer", rec.Code, rec.Body.String())
	}
	select {
	case seen := <-got:
		if want := "p" + tok + ".localhost"; seen.Host != want {
			t.Fatalf("the host was addressed as %q, want %q", seen.Host, want)
		}
		if want := "/preview/a1/3000/app/main.js"; seen.URL.Path != want {
			t.Fatalf("the host was sent %q, want the browser's path %q", seen.URL.Path, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the relay did not reach the host's preview listener")
	}
}

// TestARecordedSharedPreviewIsRelayedWithItsCookie: a ?t= link is relayed with
// the browser's host, so the host's own cookie and redirect come back to the
// browser, and the cookie the browser then sends reaches the host too
// (one-daemon open question 1).
func TestARecordedSharedPreviewIsRelayedWithItsCookie(t *testing.T) {
	s := NewServer(testFleet(t))
	if err := s.SetPreviewURL("https://preview.example.com"); err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("cd", 24)
	seen := make(chan string, 2)
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host + " " + r.URL.RequestURI() + " t=" + r.URL.Query().Get("t") + " cookie=" + r.Header.Get("Cookie")
		if r.URL.Query().Get("t") != "" {
			http.SetCookie(w, &http.Cookie{Name: "mp_a1_3000", Value: tok, Path: "/preview/a1/3000/"})
			http.Redirect(w, r, "/preview/a1/3000/", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "the host's page")
	}))
	s.recordPreviewRelay("box", "https://preview.example.com/preview/a1/3000/?t="+tok)

	// The ?t= request: the host's own answer, its cookie included, comes back.
	rec := previewAs(s, "preview.example.com", "/preview/a1/3000/?t="+tok, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("the ?t= relay = %d, want the host's own 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/preview/a1/3000/" {
		t.Fatalf("Location = %q, want the host's own", loc)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != "mp_a1_3000" || cs[0].Value != tok {
		t.Fatalf("cookies = %+v, want the host's own", cs)
	}
	select {
	case got := <-seen:
		if want := "preview.example.com /preview/a1/3000/?t=" + tok + " t=" + tok + " cookie="; got != want {
			t.Fatalf("the host saw %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ?t= relay did not reach the host's preview listener")
	}

	// The cookie request: the browser's cookie reaches the host unchanged.
	rec = previewAs(s, "preview.example.com", "/preview/a1/3000/app.js", func(r *http.Request) {
		r.AddCookie(cs[0])
	})
	if rec.Code != http.StatusOK || rec.Body.String() != "the host's page" {
		t.Fatalf("the cookie relay = %d %q, want the host's own answer", rec.Code, rec.Body.String())
	}
	select {
	case got := <-seen:
		if want := "preview.example.com /preview/a1/3000/app.js t= cookie=mp_a1_3000=" + tok; got != want {
			t.Fatalf("the host saw %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cookie relay did not reach the host's preview listener")
	}
}

// TestThisDaemonsOwnPreviewIsNotRelayed: a preview this daemon issued is served
// here, even when a relay recorded the same agent ID (one-daemon open question
// 1).
func TestThisDaemonsOwnPreviewIsNotRelayed(t *testing.T) {
	p := newPreviewEnv(t, 3000)
	relayed := false
	p.f.previewForward = func(w http.ResponseWriter, _ *http.Request, _ string, _ int, _ string) {
		w.WriteHeader(http.StatusNoContent)
	}
	remotePreview(t, p.srv, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayed = true
	}))
	// A relay records the same agent and port, as a forward of another host's
	// link for it would.
	p.srv.recordPreviewRelay("box", "https://preview.example.com/preview/"+p.id+"/3000/?t="+strings.Repeat("ef", 24))

	tok, _ := p.issue(t, 3000)

	// The token in a localhost host is this daemon's own.
	if rec := p.do("GET", "/preview/"+p.id+"/3000/app.js", func(r *http.Request) {
		r.Host = "p" + tok + ".localhost:9911"
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("this daemon's own host token = %d %s, want it served here", rec.Code, rec.Body)
	}
	// And a ?t= token is this daemon's own too.
	if rec := p.do("GET", "/preview/"+p.id+"/3000/?t="+tok, nil); rec.Code != http.StatusFound {
		t.Fatalf("this daemon's own ?t= = %d %s, want it served here", rec.Code, rec.Body)
	}
	if relayed {
		t.Fatal("a preview of this daemon's own agent reached a forwarded host")
	}
}

// TestAnUnrecordedPreviewIs404: a link nothing recorded is served by nobody,
// and every preview failure is a plain 404 (one-daemon open question 1).
func TestAnUnrecordedPreviewIs404(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unrecorded preview reached a host")
	}))

	for _, host := range []string{"p" + tok + ".preview.example.com", "p" + tok + ".localhost:9911"} {
		rec := previewAs(s, host, "/preview/a1/3000/", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("host %s = %d, want 404", host, rec.Code)
		}
	}
}

// TestARelayToAHostThatIsDownIs404: a host that is gone, not live, or reports
// no preview port answers as every preview failure does (one-daemon open
// question 1).
func TestARelayToAHostThatIsDownIs404(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	link := "https://p" + tok + ".preview.example.com/preview/a1/3000/"
	s.recordPreviewRelay("box", link)
	host := "p" + tok + ".preview.example.com"

	if rec := previewAs(s, host, "/preview/a1/3000/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no report at all = %d, want 404", rec.Code)
	}
	reportHosts(t, s, contract.DaemonHost{
		Name: "box", SSH: "me@box", State: contract.HostDown, Socket: "/tmp/box.sock", PreviewPort: 54400,
		LastError: "ssh: connect: refused", Since: time.Now().UnixMilli(),
	})
	if rec := previewAs(s, host, "/preview/a1/3000/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a host that is down = %d, want 404", rec.Code)
	}
	reportHosts(t, s, liveHost("box", "/tmp/box.sock"))
	if rec := previewAs(s, host, "/preview/a1/3000/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a host with no preview port = %d, want 404", rec.Code)
	}
}

// TestARelayCarriesAWebSocketUpgrade: a preview's own websocket reaches the host
// that issued the link, through this daemon (one-daemon open question 1).
func TestARelayCarriesAWebSocketUpgrade(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	ln := upgradeServer(t)
	host := liveHost("box", "/tmp/box.sock")
	host.PreviewPort = listenPort(t, ln)
	reportHosts(t, s, host)
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	bridge := httptest.NewServer(s.PreviewHandler())
	t.Cleanup(bridge.Close)
	handshakeAndEcho(t, strings.TrimPrefix(bridge.URL, "http://"), "p"+tok+".preview.example.com", "/preview/a1/3000/ws", "")
}

// TestThePreviewRelayTableIsBounded: a table at its bound drops its oldest entry
// for a new one (one-daemon open question 1).
func TestThePreviewRelayTableIsBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := testFleet(t)
	f.clock = func() time.Time { return now }
	s := NewServer(f)
	if err := s.SetPreviewURL("https://p{token}.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 0, maxPreviewRelays+1)
	for i := 0; i <= maxPreviewRelays; i++ {
		now = now.Add(time.Second)
		tok := strings.Repeat("a", 40) + strconv.FormatInt(int64(i), 16)
		tokens = append(tokens, tok)
		s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")
	}

	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	if got := len(s.relaysByToken); got != maxPreviewRelays {
		t.Fatalf("the table holds %d links, want its bound %d", got, maxPreviewRelays)
	}
	if _, ok := s.relaysByToken[tokens[0]]; ok {
		t.Fatal("the oldest link survived a full table")
	}
	if _, ok := s.relaysByToken[tokens[len(tokens)-1]]; !ok {
		t.Fatal("the newest link was dropped")
	}
}

// TestAnExpiredPreviewRelayIsDropped: a link past previewTokenTTL is served by
// nobody, and is dropped rather than kept (one-daemon open question 1).
func TestAnExpiredPreviewRelayIsDropped(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := testFleet(t)
	f.clock = func() time.Time { return now }
	s := NewServer(f)
	if err := s.SetPreviewURL("https://p{token}.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("ab", 24)
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	now = now.Add(previewTokenTTL + time.Second)
	req := testRequest(http.MethodGet, "/preview/a1/3000/", nil)
	req.Host = "p" + tok + ".preview.example.com"
	if rel, ok := s.previewRelayFor(req); ok {
		t.Fatalf("an expired link was served: %+v", rel)
	}
	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	if _, ok := s.relaysByToken[tok]; ok {
		t.Fatal("the expired link was kept under its token")
	}
	if _, ok := s.relaysByAgent["a1/3000"]; ok {
		t.Fatal("the expired link was kept under its agent and port")
	}
}

// TestARelayCarriesNoForwardedHeaders pins one thing about the relay's own hop:
// it adds none of the X-Forwarded-* headers, so the host reads the browser's
// request and nothing else.
func TestARelayCarriesNoForwardedHeaders(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	got := make(chan http.Header, 1)
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
	}))
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	if rec := previewAs(s, "p"+tok+".preview.example.com", "/preview/a1/3000/", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	}); rec.Code != http.StatusOK {
		t.Fatalf("the relay = %d", rec.Code)
	}
	seen := <-got
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v := seen.Values(h); len(v) != 0 {
			t.Errorf("the relay added %s: %q", h, v)
		}
	}
}
