package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newWebListener is the web listener behind a proxy: the daemon publishes
// https://studio.example.com as the address that proxy serves, so that name and
// loopback are this daemon's, and every other name is another site's.
func newWebListener(t *testing.T) *Server {
	t.Helper()
	return NewServer(testFleet(t), "https://studio.example.com")
}

// webRequest issues one request against the web listener from host, with
// headers. An empty header value leaves that header off.
func webRequest(t *testing.T, s *Server, method, path, body, host string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.Host = host
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec
}

func TestTheWebListenerRefusesAnotherHost(t *testing.T) {
	s := newWebListener(t)
	rec := webRequest(t, s, http.MethodGet, "/api/agents", "", "evil.example:7700", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another host = %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("refusal Content-Type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "web_url") {
		t.Fatalf("the reason does not name web_url: %s", rec.Body)
	}
}

func TestTheWebListenerAnswersLoopbackHosts(t *testing.T) {
	s := newWebListener(t)
	for _, host := range []string{"localhost:7700", "127.0.0.1:7700", "[::1]:7700"} {
		rec := webRequest(t, s, http.MethodGet, "/api/agents", "", host, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("host %s = %d %s", host, rec.Code, rec.Body)
		}
	}
}

func TestTheWebListenerAnswersThePublicHost(t *testing.T) {
	s := newWebListener(t)
	for _, host := range []string{"studio.example.com", "studio.example.com:443"} {
		rec := webRequest(t, s, http.MethodGet, "/api/agents", "", host, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("public host %s = %d %s", host, rec.Code, rec.Body)
		}
	}
}

func TestACrossSitePostIsRefused(t *testing.T) {
	s := newWebListener(t)
	for _, site := range []string{"cross-site", "same-site"} {
		rec := webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com",
			map[string]string{"Sec-Fetch-Site": site, "Content-Type": "application/json"})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-site request refused") {
			t.Fatalf("Sec-Fetch-Site %s = %d %s", site, rec.Code, rec.Body)
		}
	}
}

func TestASameOriginPostPasses(t *testing.T) {
	s := newWebListener(t)
	rec := webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com",
		map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"})
	// The guard let it through, so the route answered: a spawn with no project
	// is the route's own refusal, not the guard's.
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnsupportedMediaType {
		t.Fatalf("same-origin POST = %d %s", rec.Code, rec.Body)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same-origin POST reached no route: %d %s", rec.Code, rec.Body)
	}
}

func TestAnOriginFromAnotherSiteIsRefused(t *testing.T) {
	s := newWebListener(t)
	// "null" is what a sandboxed frame and a file: page send.
	for _, origin := range []string{"https://evil.example", "null"} {
		rec := webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com",
			map[string]string{"Origin": origin, "Content-Type": "application/json"})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-site request refused") {
			t.Fatalf("Origin %s = %d %s", origin, rec.Code, rec.Body)
		}
	}
}

func TestARequestWithNeitherHeaderPasses(t *testing.T) {
	s := newWebListener(t)
	// curl, a CLI and the e2e suite send neither header: they are not a
	// browser acting for another site. "none" is a navigation the user
	// started, which the plan allows for the same reason.
	for _, headers := range []map[string]string{
		{"Content-Type": "application/json"},
		{"Content-Type": "application/json", "Sec-Fetch-Site": "none"},
	} {
		rec := webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com", headers)
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnsupportedMediaType {
			t.Fatalf("headers %v = %d %s", headers, rec.Code, rec.Body)
		}
		// The guard let it through, so the route answered: a spawn with no
		// project is the route's own refusal.
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("headers %v reached no route: %d %s", headers, rec.Code, rec.Body)
		}
	}
}

func TestAnAPIBodyMustBeJSON(t *testing.T) {
	s := newWebListener(t)
	rec := webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com",
		map[string]string{"Content-Type": "text/plain"})
	if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), "application/json") {
		t.Fatalf("text/plain = %d %s", rec.Code, rec.Body)
	}
	rec = webRequest(t, s, http.MethodPost, "/api/agents", `{}`, "studio.example.com",
		map[string]string{"Content-Type": "application/json; charset=utf-8"})
	if rec.Code == http.StatusUnsupportedMediaType {
		t.Fatalf("json with a charset = %d %s", rec.Code, rec.Body)
	}
}

func TestHooksStatusAndMCPAreExempt(t *testing.T) {
	s := newWebListener(t)
	// Each of these carries its own credential and checks its own caller, so
	// the guard stays out of its way: what answers a cross-site request is the
	// route's own refusal, in the route's own words.
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		// No secret is configured for the repo, so the delivery is refused as
		// an unverifiable signature.
		{hooksPrefix + "r1", http.StatusUnauthorized, "invalid signature"},
		// A status link is a read of its own token, and a POST is not one.
		{statusPrefix + "tok", http.StatusNotFound, "not found"},
		// No bearer token, so /mcp's own authentication answers.
		{"/mcp", http.StatusUnauthorized, "unauthorized"},
	} {
		rec := webRequest(t, s, http.MethodPost, tc.path, "", "studio.example.com",
			map[string]string{"Sec-Fetch-Site": "cross-site"})
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("cross-site POST %s = %d %s, want the route's own %d and %q",
				tc.path, rec.Code, rec.Body, tc.status, tc.body)
		}
	}
}

func TestTheSocketHasNoGuard(t *testing.T) {
	s := newWebListener(t)
	// The socket's callers are peer-checked as the owner: a Host that would be
	// another site's on the web listener is the owner's own request here.
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Host = "evil.example:7700"
	rec := httptest.NewRecorder()
	s.SocketHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("socket = %d %s", rec.Code, rec.Body)
	}
}
