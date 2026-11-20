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
	host := remotePreviewAt(t, name, h)
	reportHosts(t, s, host)
	return host.PreviewPort
}

// remotePreviewAt is a forwarded host's preview listener as a report entry,
// which a test that has several hosts reports in one go: a report replaces the
// hosts it names.
func remotePreviewAt(t *testing.T, name string, h http.Handler) contract.DaemonHost {
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
	return host
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
	// The daemon's own preview origin is the one its requests reach it on, so a
	// link recorded for the same agent and port is recorded on that origin.
	if err := p.srv.SetPreviewURL("http://example.com:9911"); err != nil {
		t.Fatal(err)
	}
	relayed := false
	p.f.previewForward = func(w http.ResponseWriter, _ *http.Request, _ string, _ int, _ string) {
		w.WriteHeader(http.StatusNoContent)
	}
	remotePreview(t, p.srv, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayed = true
	}))
	// A relay records the same agent and port, as a forward of another host's
	// link for it would.
	p.srv.recordPreviewRelay("box", "http://example.com:9911/preview/"+p.id+"/3000/?t="+strings.Repeat("ef", 24))

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
// for a new one, whichever host and agent the entries name (one-daemon open
// question 1).
func TestThePreviewRelayTableIsBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := testFleet(t)
	f.clock = func() time.Time { return now }
	s := NewServer(f)
	if err := s.SetPreviewURL("https://p{token}.preview.example.com"); err != nil {
		t.Fatal(err)
	}
	// Alternating hosts and agents: the table keyed by agent and port is bounded
	// with it.
	tokens := make([]string, 0, maxPreviewRelays+1)
	for i := 0; i <= maxPreviewRelays; i++ {
		now = now.Add(time.Second)
		tok := strings.Repeat("a", 40) + strconv.FormatInt(int64(i), 16)
		tokens = append(tokens, tok)
		host := "box" + strconv.Itoa(i%2)
		agent := "a" + strconv.Itoa(i%3)
		s.recordPreviewRelay(host, "https://p"+tok+".preview.example.com/preview/"+agent+"/3000/")
	}
	newestHost := "box" + strconv.Itoa(maxPreviewRelays%2)
	newestAgent := "a" + strconv.Itoa(maxPreviewRelays%3)

	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	if got := len(s.relaysByToken); got != maxPreviewRelays {
		t.Fatalf("the table holds %d links, want its bound %d", got, maxPreviewRelays)
	}
	if got := len(s.relaysByAgent); got > maxPreviewRelays {
		t.Fatalf("the table holds %d links by agent and port, want no more than %d", got, maxPreviewRelays)
	}
	if _, ok := s.relaysByToken[tokens[0]]; ok {
		t.Fatal("the oldest link survived a full table")
	}
	if _, ok := s.relaysByToken[tokens[len(tokens)-1]]; !ok {
		t.Fatal("the newest link was dropped")
	}
	byAgent, ok := s.relaysByAgent[previewRelayKey(newestAgent, 3000)]
	if !ok || byAgent.Token != tokens[len(tokens)-1] || byAgent.Host != newestHost {
		t.Fatalf("the newest link by agent = %+v (ok %v), want %s's", byAgent, ok, newestHost)
	}
}

// TestAnExpiredPreviewRelayIsDropped: a link past previewTokenTTL is served by
// nobody, and is dropped rather than kept, whether it is looked up by its token
// or — for a request carrying no credential at all — by its agent and port
// (one-daemon open question 1).
func TestAnExpiredPreviewRelayIsDropped(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	now := base
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
	if _, ok := s.relaysByToken[tok]; ok {
		t.Fatal("the expired link was kept under its token")
	}
	if _, ok := s.relaysByAgent["a1/3000"]; ok {
		t.Fatal("the expired link was kept under its agent and port")
	}
	s.relaysMu.Unlock()

	// The agent-keyed path: a request on a shared origin that carries no ?t=
	// and no cookie is served by the host recorded for its agent and port, and
	// that recording expires too.
	now = base
	shared := NewServer(f)
	if err := shared.SetPreviewURL("https://preview.example.com"); err != nil {
		t.Fatal(err)
	}
	sharedTok := strings.Repeat("cd", 24)
	shared.recordPreviewRelay("box", "https://preview.example.com/preview/b2/3000/?t="+sharedTok)
	bare := testRequest(http.MethodGet, "/preview/b2/3000/", nil)
	bare.Host = "preview.example.com"
	rel, ok := shared.previewRelayFor(bare)
	if !ok || rel.Token != sharedTok || rel.Host != "box" {
		t.Fatalf("a live agent-keyed link = %+v (ok %v), want box's", rel, ok)
	}

	now = base.Add(previewTokenTTL + time.Second)
	if rel, ok := shared.previewRelayFor(bare); ok {
		t.Fatalf("an expired agent-keyed link was served: %+v", rel)
	}
	shared.relaysMu.Lock()
	defer shared.relaysMu.Unlock()
	if _, ok := shared.relaysByAgent[previewRelayKey("b2", 3000)]; ok {
		t.Fatal("the expired agent-keyed link was kept under its agent and port")
	}
	if _, ok := shared.relaysByToken[sharedTok]; ok {
		t.Fatal("the expired agent-keyed link was kept under its token")
	}
}

// TestARelayCarriesNoForwardedHeaders pins two things about the relay's own hop:
// it adds none of the X-Forwarded-* headers, so the host reads the browser's
// request and nothing else, and it carries none of the caller's credentials, so
// a proxy's Basic or bearer token stops here (one-daemon open question 1).
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
		r.Header.Set("Authorization", "Bearer the-proxy's-token")
		r.AddCookie(&http.Cookie{Name: "mp_a1_3000", Value: tok})
	}); rec.Code != http.StatusOK {
		t.Fatalf("the relay = %d", rec.Code)
	}
	seen := <-got
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "Authorization"} {
		if v := seen.Values(h); len(v) != 0 {
			t.Errorf("the relay carried the caller's %s: %q", h, v)
		}
	}
	// The preview's own auth is not the caller's, and it still crosses the hop.
	if cookie := seen.Get("Cookie"); cookie != "mp_a1_3000="+tok {
		t.Errorf("the host saw Cookie %q, want the preview's own", cookie)
	}
}

// TestTwoHostsRecordingOneAgentAndPortAreBothServed: the link a request carries
// is its token, so a second host recording the same agent and port does not take
// the first one's preview away (one-daemon open question 1).
func TestTwoHostsRecordingOneAgentAndPortAreBothServed(t *testing.T) {
	s := templatedRelayServer(t)
	first, second := strings.Repeat("ab", 24), strings.Repeat("cd", 24)
	box := remotePreviewAt(t, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "box's page")
	}))
	loft := remotePreviewAt(t, "loft", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "loft's page")
	}))
	reportHosts(t, s, box, loft)
	s.recordPreviewRelay("box", "https://p"+first+".preview.example.com/preview/a1/3000/")
	s.recordPreviewRelay("loft", "https://p"+second+".preview.example.com/preview/a1/3000/")

	for _, c := range []struct{ tok, want string }{{first, "box's page"}, {second, "loft's page"}} {
		rec := previewAs(s, "p"+c.tok+".preview.example.com", "/preview/a1/3000/", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Fatalf("the preview of %s = %d %q, want %q", c.tok, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// TestARelayedTokenIsNotServedForAnotherAgentsPath: a link's token names one
// agent and port, and the table is looked up by that token, so a path naming
// another agent is not served by it (one-daemon open question 1).
func TestARelayedTokenIsNotServedForAnotherAgentsPath(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	reached := make(chan struct{}, 2)
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		_, _ = io.WriteString(w, "the host's page")
	}))
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	// The recorded token rides in the host and in ?t=; neither is agent b2's.
	for _, host := range []string{"p" + tok + ".preview.example.com", "preview.example.com"} {
		if rec := previewAs(s, host, "/preview/b2/3000/?t="+tok, nil); rec.Code != http.StatusNotFound {
			t.Errorf("another agent's path on %s = %d, want 404", host, rec.Code)
		}
	}
	if rec := previewAs(s, "p"+tok+".preview.example.com", "/preview/b2/3000/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another agent's path = %d, want 404", rec.Code)
	}
	select {
	case <-reached:
		t.Fatal("a recorded link's token served another agent's path")
	default:
	}
}

// TestALinkOnAnotherOriginRecordsNothing: a host issues its link through the
// preview_url this daemon gave it, so a link on any other origin records
// nothing, and neither does one whose token is not a token this daemon could
// have issued (one-daemon open question 1).
func TestALinkOnAnotherOriginRecordsNothing(t *testing.T) {
	s := templatedRelayServer(t) // https://p{token}.preview.example.com
	tok := strings.Repeat("ab", 24)
	s.recordPreviewRelay("box", "https://p"+tok+".other.example.com/preview/a1/3000/")
	s.recordPreviewRelay("box", "https://p"+strings.Repeat("z", 24)+".preview.example.com/preview/a1/3000/")

	shared := NewServer(testFleet(t))
	if err := shared.SetPreviewURL("https://preview.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, issued := range []string{
		"https://elsewhere.example.com/preview/a1/3000/?t=" + tok,
		"https://preview.example.com.evil.test/preview/a1/3000/?t=" + tok,
		"https://preview.example.com/preview/a1/3000/",
		"https://preview.example.com/preview/a1/3000/?t=not-a-token",
		"https://preview.example.com/preview/a1/3000/?t=" + strings.Repeat("a", maxPreviewRelayToken+1),
	} {
		shared.recordPreviewRelay("box", issued)
	}

	for name, srv := range map[string]*Server{"templated": s, "shared": shared} {
		srv.relaysMu.Lock()
		byToken, byAgent := len(srv.relaysByToken), len(srv.relaysByAgent)
		srv.relaysMu.Unlock()
		if byToken != 0 || byAgent != 0 {
			t.Fatalf("the %s daemon recorded %d/%d links for other origins, want none", name, byToken, byAgent)
		}
	}

	// The same link on this daemon's own origin is recorded, so the test says
	// something about the origin and not about recording at all.
	shared.recordPreviewRelay("box", "https://preview.example.com/preview/a1/3000/?t="+tok)
	shared.relaysMu.Lock()
	defer shared.relaysMu.Unlock()
	if _, ok := shared.relaysByToken[tok]; !ok {
		t.Fatal("a link on this daemon's own origin was not recorded")
	}
}

// TestARelayGivesUpOnAHostThatNeverAnswers: a host that takes the connection and
// never writes a header is answered as every other preview failure is, rather
// than holding the relay's goroutine for as long as the host stays wedged
// (one-daemon open question 1).
func TestARelayGivesUpOnAHostThatNeverAnswers(t *testing.T) {
	s := templatedRelayServer(t)
	tok := strings.Repeat("ab", 24)
	release := make(chan struct{})
	remotePreview(t, s, "box", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	// Close runs before the host's server, which waits for its handler.
	t.Cleanup(func() { close(release) })
	s.recordPreviewRelay("box", "https://p"+tok+".preview.example.com/preview/a1/3000/")

	// The hop's wait for the host's first header, shrunk so the test is quick:
	// it bounds time to the first header and nothing else.
	saved := previewRelayTransport
	previewRelayTransport = newPreviewRelayTransport(100 * time.Millisecond)
	t.Cleanup(func() { previewRelayTransport = saved })

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- previewAs(s, "p"+tok+".preview.example.com", "/preview/a1/3000/", nil)
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusNotFound {
			t.Fatalf("a host that never answered = %d, want 404", rec.Code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay waited on a host that never answered")
	}
}
