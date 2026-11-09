package bridge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// viaHandler issues one request against an arbitrary handler, so a test
// can drive the socket handler without a token.
func viaHandler(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// daemonTestServer is a token-protected server with daemon info and two
// projects, in the order the workspace holds them.
func daemonTestServer(t *testing.T, projects ...string) *Server {
	t.Helper()
	f := testFleet(t)
	for _, root := range projects {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(f, "secret")
	s.SetDaemonInfo(DaemonInfo{
		Version:   "v1.2.3",
		Profile:   "local",
		PID:       4242,
		StartedAt: 1700000000000,
		Socket:    "/run/user/501/marshal/daemon.sock",
	})
	return s
}

// TestTheDaemonRouteAnswersItsInfo is decision 8: the socket handler serves
// the daemon's own description plus the workspace's projects, sorted, with
// no token; the network handler still refuses an unauthenticated request.
func TestTheDaemonRouteAnswersItsInfo(t *testing.T) {
	s := daemonTestServer(t, "/home/u/zeta", "/home/u/alpha")

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got DaemonInfo
	decodeBody(t, rec, &got)
	if got.Version != "v1.2.3" || got.Profile != "local" || got.PID != 4242 ||
		got.StartedAt != 1700000000000 || got.Socket != "/run/user/501/marshal/daemon.sock" {
		t.Fatalf("daemon info = %+v", got)
	}
	if len(got.Projects) != 2 || got.Projects[0] != "/home/u/alpha" || got.Projects[1] != "/home/u/zeta" {
		t.Fatalf("projects = %v, want them sorted", got.Projects)
	}

	if rec := doReq(t, s, http.MethodGet, "/api/daemon", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("network /api/daemon without a token = %d, want 401", rec.Code)
	}
}

// TestTheDaemonRouteIsAbsentWithoutInfo: a server that is not a daemon
// answers 404, so the server profile's API is unchanged in effect.
func TestTheDaemonRouteIsAbsentWithoutInfo(t *testing.T) {
	s := NewServer(testFleet(t), "secret")

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] != "not a daemon" {
		t.Fatalf("body = %v, want the not-a-daemon error", body)
	}
}

// TestTheSocketHandlerServesEveryRoute: the socket handler is ServeHTTP's
// routing without the bearer check, not a subset of it.
func TestTheSocketHandlerServesEveryRoute(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")
	sock := s.SocketHandler()

	if rec := viaHandler(t, sock, http.MethodGet, "/api/projects"); rec.Code != http.StatusOK {
		t.Fatalf("socket /api/projects status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// An unknown /api/ path answers what the token-checked handler
	// answers: the mux's own 404, not the SPA fallback.
	const unknown = "/api/no-such-route"
	viaSocket := viaHandler(t, sock, http.MethodGet, unknown)
	viaNetwork := doReq(t, s, http.MethodGet, unknown, nil,
		map[string]string{"Authorization": "Bearer secret"})
	if viaSocket.Code != viaNetwork.Code {
		t.Fatalf("socket %s = %d, network with a token = %d", unknown, viaSocket.Code, viaNetwork.Code)
	}
	if viaSocket.Body.String() != viaNetwork.Body.String() {
		t.Fatalf("socket body %q, network body %q", viaSocket.Body.String(), viaNetwork.Body.String())
	}
}
