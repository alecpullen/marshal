package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// unixServer serves handler on a unix socket under a short temp path: a
// socket path is capped at 104 bytes on macOS.
func unixServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "marshal.sock")

	srv := httptest.NewUnstartedServer(handler)
	// Replace the TCP listener the server made for itself with the
	// unix one before it starts serving.
	_ = srv.Listener.Close()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	return socket
}

func TestTheClientReadsTheDaemon(t *testing.T) {
	const body = `{"version":"1.2.3","profile":"local","pid":4242,` +
		`"startedAt":1700000000,"socket":"/tmp/marshal.sock",` +
		`"projects":["/work/one","/work/two"]}`

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    Info
		wantErr string
	}{
		{
			name: "answers",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != "/api/daemon" {
					t.Errorf("path = %q, want /api/daemon", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			},
			want: Info{
				Version:   "1.2.3",
				Profile:   "local",
				PID:       4242,
				StartedAt: 1700000000,
				Socket:    "/tmp/marshal.sock",
				Projects:  []string{"/work/one", "/work/two"},
			},
		},
		{
			name: "refuses",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "not a daemon"})
			},
			wantErr: "404",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := unixServer(t, tc.handler)
			got, err := NewClient(socket).Info(context.Background())

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Info() = %+v, want an error naming %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Info() error = %q, want it to name %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "not a daemon") {
					t.Errorf("Info() error = %q, want the body's error text", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Info(): %v", err)
			}
			if got.Version != tc.want.Version || got.Profile != tc.want.Profile ||
				got.PID != tc.want.PID || got.StartedAt != tc.want.StartedAt ||
				got.Socket != tc.want.Socket || len(got.Projects) != len(tc.want.Projects) {
				t.Fatalf("Info() = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want.Projects {
				if got.Projects[i] != tc.want.Projects[i] {
					t.Errorf("Projects[%d] = %q, want %q", i, got.Projects[i], tc.want.Projects[i])
				}
			}
		})
	}
}

func TestTheClientReportsAnUnansweredSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nobody.sock")
	if _, err := NewClient(socket).Info(context.Background()); err == nil {
		t.Fatalf("Info() on a socket nothing listens on returned no error")
	}
}
