package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"marshal/internal/daemon"
)

// TestStudioSignInURLKeepsTheCodeInTheFragment is the whole point: a fragment
// never leaves the browser, so the code reaches no request line and no access
// log, and the page can read it without the server seeing it.
func TestStudioSignInURLKeepsTheCodeInTheFragment(t *testing.T) {
	got := studioSignInURL("http://127.0.0.1:7788", "a b+c/d")
	want := "http://127.0.0.1:7788/#code=a+b%2Bc%2Fd"
	if got != want {
		t.Fatalf("studioSignInURL = %q, want %q", got, want)
	}
	if got == "http://127.0.0.1:7788/?token=" {
		t.Fatal("the URL carries a query, not a fragment")
	}
}

// TestProbeStudioRefusesAnotherDaemon: the launcher reads the daemon's identity
// over its socket and refuses a port that answers as something else, before a
// code is sent to it.
func TestProbeStudioRefusesAnotherDaemon(t *testing.T) {
	const socket = "/run/user/501/marshal/daemon.sock"
	want := daemon.Info{PID: 42, StartedAt: 1700000000000, Socket: socket}

	for _, tc := range []struct {
		name    string
		answer  studioHello
		status  int
		wantErr bool
	}{
		{
			name:   "the same daemon",
			answer: studioHello{PID: 42, StartedAt: 1700000000000, Socket: socket},
			status: http.StatusOK,
		},
		{
			name:    "another process on the port",
			answer:  studioHello{PID: 7, StartedAt: 1, Socket: "/somewhere/else.sock"},
			status:  http.StatusOK,
			wantErr: true,
		},
		{
			name:    "the same pid, restarted",
			answer:  studioHello{PID: 42, StartedAt: 1700000000001, Socket: socket},
			status:  http.StatusOK,
			wantErr: true,
		},
		{
			name:    "no sign-in route",
			status:  http.StatusNotFound,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/studio/hello" {
					t.Errorf("probed %s, want /api/studio/hello", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("probe sent a credential: %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tc.answer)
				}
			}))
			defer srv.Close()

			err := probeStudio(context.Background(), srv.URL, want)
			if tc.wantErr && err == nil {
				t.Fatal("probe accepted a port that is not this daemon")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("probe refused this daemon: %v", err)
			}
		})
	}
}

// TestDaemonStudioAddr pins what daemon.studio_addr accepts. The daemon runs
// host processes with the user's own authority and authenticates the browser
// with a single-use code over that listener, so an address that is not
// loopback must be refused at install time rather than published.
func TestDaemonStudioAddr(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		want    string
		wantErr bool
	}{
		{name: "default", setting: "127.0.0.1:7788", want: "127.0.0.1:7788"},
		{name: "another loopback port", setting: "127.0.0.1:9000", want: "127.0.0.1:9000"},
		{name: "ipv6 loopback", setting: "[::1]:7788", want: "[::1]:7788"},
		{name: "hostname loopback", setting: "localhost:7788", want: "localhost:7788"},
		{name: "unset serves nothing", setting: "", want: ""},
		{name: "off serves nothing", setting: "off", want: ""},
		{name: "none serves nothing", setting: "none", want: ""},
		{name: "padded off", setting: "  off  ", want: ""},
		{name: "wildcard is refused", setting: "0.0.0.0:7788", wantErr: true},
		{name: "routable address is refused", setting: "192.168.1.5:7788", wantErr: true},
		{name: "blank host is refused", setting: ":7788", wantErr: true},
		{name: "no port is refused", setting: "127.0.0.1", wantErr: true},
		{name: "nonsense is refused", setting: "not an address", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := daemonStudioAddr(tc.setting)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("daemonStudioAddr(%q) = %q, want an error", tc.setting, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("daemonStudioAddr(%q): %v", tc.setting, err)
			}
			if got != tc.want {
				t.Errorf("daemonStudioAddr(%q) = %q, want %q", tc.setting, got, tc.want)
			}
		})
	}
}
