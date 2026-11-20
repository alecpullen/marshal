package bridge

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// daemonTestServer is a server with daemon info and the given projects, in
// the order the workspace holds them.
func daemonTestServer(t *testing.T, projects ...string) *Server {
	t.Helper()
	f := testFleet(t)
	for _, root := range projects {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(f)
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
// the daemon's own description plus the workspace's projects, sorted, and the
// web listener serves it on the mux too.
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
	// A daemon that never started a preview listener names no preview
	// address: the field is absent, not an empty string a caller could take
	// for an origin it can reach (one-daemon open question 1).
	if body := rec.Body.String(); strings.Contains(body, "previewAddr") {
		t.Fatalf("daemon info %s reports previewAddr with no preview listener", body)
	}
}

// TestThePreviewAddrIsReported: a daemon that started a preview listener
// reports its bound address, and the listener's port with it, so a caller can
// reach that origin and name it back (one-daemon open question 1).
func TestThePreviewAddrIsReported(t *testing.T) {
	s := daemonTestServer(t)
	s.SetPreviewAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 54400})

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got DaemonInfo
	decodeBody(t, rec, &got)
	if got.PreviewAddr != "127.0.0.1:54400" {
		t.Fatalf("previewAddr = %q, want the bound preview address", got.PreviewAddr)
	}
	if port := s.previewPort.Load(); port != 54400 {
		t.Fatalf("preview port = %d, want SetPreviewAddr to set 54400", port)
	}
}

// TestTheDaemonRouteIsAbsentWithoutInfo: a server that is not a daemon
// answers 404, so the server profile's API is unchanged in effect.
func TestTheDaemonRouteIsAbsentWithoutInfo(t *testing.T) {
	s := NewServer(testFleet(t))

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

// TestTheDaemonRouteReportsThePins is decision 9: the daemon route lists the
// pinned roots even when they have no child, and marks the child a pinned
// root runs.
func TestTheDaemonRouteReportsThePins(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	root := filepath.Clean(t.TempDir())
	if err := f.ws.SetPinned(root, true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	s := NewServer(f)
	s.SetDaemonInfo(DaemonInfo{Version: "v1", Profile: "local"})
	sock := s.SocketHandler()

	rec := viaHandler(t, sock, http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got DaemonInfo
	decodeBody(t, rec, &got)
	if len(got.Pinned) != 1 || got.Pinned[0] != root {
		t.Fatalf("pinned = %v, want [%s]", got.Pinned, root)
	}
	if len(got.Children) != 0 {
		t.Fatalf("children = %+v, want none before the pinned child runs", got.Children)
	}

	// The pinned root's child, once it runs, is marked pinned.
	f.StartPinned(ctlContext(t))
	if fakeFor(root) == nil {
		t.Fatal("StartPinned started no child for the pinned root")
	}
	rec = viaHandler(t, sock, http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &got)
	if len(got.Children) != 1 {
		t.Fatalf("children = %+v, want one", got.Children)
	}
	if got.Children[0].Root != root || !got.Children[0].Pinned {
		t.Fatalf("child = %+v, want %s pinned", got.Children[0], root)
	}
}
