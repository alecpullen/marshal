package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"marshal/web/bridge/contract"
)

// hostsRequest issues one request carrying a JSON body against an arbitrary
// handler, so a test can drive the socket's report without a token.
func hostsRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// reportedHostsOnTheSocket is the hosts GET /api/daemon reports on the socket.
func reportedHostsOnTheSocket(t *testing.T, s *Server) []DaemonHost {
	t.Helper()
	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var info DaemonInfo
	decodeBody(t, rec, &info)
	return info.Hosts
}

// TestTheSocketTakesAHostsReport is one-daemon D7: the tunnel helper's report
// lands on the socket's server, sorted by name, and GET /api/daemon answers
// every field of it.
func TestTheSocketTakesAHostsReport(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")
	zeta := DaemonHost{
		Name:   "zeta",
		SSH:    "zeta.example",
		State:  contract.HostLive,
		Socket: "/run/user/501/marshal/zeta.sock",
		Since:  1700000000000,
	}
	alpha := DaemonHost{
		Name:      "alpha",
		SSH:       "alpha.example",
		State:     contract.HostDown,
		Socket:    "/run/user/501/marshal/alpha.sock",
		LastError: "connection refused",
		Since:     1700000001000,
	}

	rec := hostsRequest(t, s.SocketHandler(), http.MethodPut, daemonHostsPath,
		contract.DaemonHostsReport{Hosts: []DaemonHost{zeta, alpha}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("hosts report = %d %s, want 204", rec.Code, rec.Body.String())
	}

	if got, want := reportedHostsOnTheSocket(t, s), []DaemonHost{alpha, zeta}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %+v, want %+v", got, want)
	}
}

// TestAHostsReportReplacesTheLastOne: a report is the whole state, so a host
// the new report leaves out is gone.
func TestAHostsReportReplacesTheLastOne(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")
	first := []DaemonHost{
		{Name: "alpha", SSH: "alpha.example", State: contract.HostLive, Socket: "/run/user/501/marshal/alpha.sock", Since: 1700000000000},
		{Name: "zeta", SSH: "zeta.example", State: contract.HostConnecting, Socket: "/run/user/501/marshal/zeta.sock", Since: 1700000001000},
	}
	if rec := hostsRequest(t, s.SocketHandler(), http.MethodPut, daemonHostsPath,
		contract.DaemonHostsReport{Hosts: first}); rec.Code != http.StatusNoContent {
		t.Fatalf("first hosts report = %d %s, want 204", rec.Code, rec.Body.String())
	}

	beta := DaemonHost{Name: "beta", SSH: "beta.example", State: contract.HostDown, Socket: "/run/user/501/marshal/beta.sock", LastError: "ssh: no route to host", Since: 1700000002000}
	if rec := hostsRequest(t, s.SocketHandler(), http.MethodPut, daemonHostsPath,
		contract.DaemonHostsReport{Hosts: []DaemonHost{beta}}); rec.Code != http.StatusNoContent {
		t.Fatalf("second hosts report = %d %s, want 204", rec.Code, rec.Body.String())
	}

	if got, want := reportedHostsOnTheSocket(t, s), []DaemonHost{beta}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts after the second report = %+v, want %+v", got, want)
	}
}

// TestDaemonInfoListsNoHostsBeforeAReport: a daemon that has heard from no
// tunnel helper reports an empty list, not null.
func TestDaemonInfoListsNoHostsBeforeAReport(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hosts":[]`) {
		t.Fatalf("body = %s, want an empty hosts list", rec.Body.String())
	}
}

// TestAHostsReportIsAPut: the report has one method, and the answer says so.
func TestAHostsReportIsAPut(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, daemonHostsPath)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET hosts report = %d %s, want 405", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPut {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPut)
	}
}

// TestAHostsReportWithAnUnknownStateIsRefused: the three states are the whole
// contract, and a report that leaves them changes nothing.
func TestAHostsReportWithAnUnknownStateIsRefused(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")
	alpha := DaemonHost{Name: "alpha", SSH: "alpha.example", State: contract.HostLive, Socket: "/run/user/501/marshal/alpha.sock", Since: 1700000000000}
	if rec := hostsRequest(t, s.SocketHandler(), http.MethodPut, daemonHostsPath,
		contract.DaemonHostsReport{Hosts: []DaemonHost{alpha}}); rec.Code != http.StatusNoContent {
		t.Fatalf("first hosts report = %d %s, want 204", rec.Code, rec.Body.String())
	}

	rec := hostsRequest(t, s.SocketHandler(), http.MethodPut, daemonHostsPath,
		contract.DaemonHostsReport{Hosts: []DaemonHost{
			{Name: "beta", SSH: "beta.example", State: contract.HostState("up"), Socket: "/run/user/501/marshal/beta.sock", Since: 1700000002000},
		}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown state = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "beta") {
		t.Fatalf("refusal = %s, want it to name the host", rec.Body.String())
	}

	if got, want := reportedHostsOnTheSocket(t, s), []DaemonHost{alpha}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts after a refused report = %+v, want the earlier report %+v", got, want)
	}
}

// TestTheNetworkListenerHasNoHostsReport: the report is the socket's own, so
// an authenticated network client still gets 404 (one-daemon D7).
func TestTheNetworkListenerHasNoHostsReport(t *testing.T) {
	s := daemonTestServer(t, "/home/u/alpha")
	report := contract.DaemonHostsReport{Hosts: []DaemonHost{
		{Name: "alpha", SSH: "alpha.example", State: contract.HostLive, Socket: "/run/user/501/marshal/alpha.sock", Since: 1700000000000},
	}}

	rec := doReq(t, s, http.MethodPut, daemonHostsPath, report, map[string]string{"Authorization": "Bearer secret"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("network hosts report = %d %s, want 404", rec.Code, rec.Body.String())
	}
}
