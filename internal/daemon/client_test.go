package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unixServer serves handler on a unix socket under a short temp path: a
// socket path is capped at 104 bytes on macOS.
func unixServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := shortSocket(t)

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
		`"startedAt":1700000000000,"socket":"/tmp/marshal.sock",` +
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
				StartedAt: 1700000000000,
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

// shortSocket is a unix socket path under a short directory: a socket path
// is capped at 104 bytes on macOS and t.TempDir can be longer than that.
func shortSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "d.sock")
	if len(path) < 100 {
		return path
	}
	// A long TMPDIR pushes the socket path over the sun_path cap. Both macOS
	// (/private/tmp) and Linux (/tmp) have a short one; take whichever exists.
	for _, root := range []string{"/private/tmp", "/tmp"} {
		dir, err := os.MkdirTemp(root, "m-attach-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return filepath.Join(dir, "d.sock")
	}
	t.Fatalf("no short temp dir for a unix socket (path would be %d bytes)", len(path))
	return ""
}

// attachStub listens on a unix socket and hands the one connection a client
// makes to serve, once the request line and headers have been read. It plays
// the bridge's half of the handshake by hand rather than with an
// http.Server, which cannot answer 101 without hijacking.
func attachStub(t *testing.T, serve func(conn net.Conn, req *http.Request)) string {
	t.Helper()
	socket := shortSocket(t)

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		serve(conn, req)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return socket
}

// writeUpgrade writes a 101 and, in the same write, the bytes the stream
// should start with.
func writeUpgrade(w io.Writer, session, stream string) error {
	var b strings.Builder
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	b.WriteString("Upgrade: " + attachProtocol + "\r\n")
	if session != "" {
		b.WriteString(sessionHeader + ": " + session + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(stream)
	_, err := io.WriteString(w, b.String())
	return err
}

func TestAttachReturnsTheStream(t *testing.T) {
	const first = "the stream's first bytes"
	socket := attachStub(t, func(conn net.Conn, req *http.Request) {
		_ = req.Body.Close()
		if err := writeUpgrade(conn, "sess-1", first); err != nil {
			t.Errorf("stub write: %v", err)
		}
	})

	att, err := NewClient(socket).Attach(context.Background(), "/work/root", "")
	if err != nil {
		t.Fatalf("Attach(): %v", err)
	}
	defer att.Conn.Close()

	if att.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", att.SessionID, "sess-1")
	}

	// These bytes arrived in the same write as the 101, so the handshake's
	// reader holds them: a stream built on the connection alone loses them.
	got := make([]byte, len(first))
	if _, err := io.ReadFull(att.Conn, got); err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	if string(got) != first {
		t.Errorf("stream began with %q, want %q", got, first)
	}
}

func TestAttachSendsTheHandshake(t *testing.T) {
	requests := make(chan *http.Request, 1)
	bodies := make(chan string, 1)

	socket := attachStub(t, func(conn net.Conn, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		requests <- req
		bodies <- string(body)
		if err := writeUpgrade(conn, "sess-9", ""); err != nil {
			t.Errorf("stub write: %v", err)
		}
	})

	att, err := NewClient(socket).Attach(context.Background(), "/work/root", "sess-9")
	if err != nil {
		t.Fatalf("Attach(): %v", err)
	}
	defer att.Conn.Close()

	req := <-requests
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.Path != "/api/projects/attach" {
		t.Errorf("path = %q, want /api/projects/attach", req.URL.Path)
	}
	if got := req.Header.Get("Connection"); !strings.EqualFold(got, "upgrade") {
		t.Errorf("Connection = %q, want Upgrade", got)
	}
	if got := req.Header.Get("Upgrade"); got != attachProtocol {
		t.Errorf("Upgrade = %q, want %q", got, attachProtocol)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	raw := <-bodies
	var body struct {
		Root      string `json:"root"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode request body %q: %v", raw, err)
	}
	if body.Root != "/work/root" || body.SessionID != "sess-9" {
		t.Errorf("body = %+v, want root /work/root and session sess-9", body)
	}
}

func TestARefusedAttachIsAnAttachError(t *testing.T) {
	const sentence = "unknown session"

	socket := attachStub(t, func(conn net.Conn, req *http.Request) {
		_ = req.Body.Close()
		body := fmt.Sprintf(`{"error":%q}`, sentence)
		if _, err := io.WriteString(conn, "HTTP/1.1 404 Not Found\r\n"+
			"Content-Type: application/json\r\n"+
			fmt.Sprintf("Content-Length: %d\r\n", len(body))+
			"\r\n"+body); err != nil {
			t.Errorf("stub write: %v", err)
		}
	})

	att, err := NewClient(socket).Attach(context.Background(), "/work/root", "missing")
	if err == nil {
		_ = att.Conn.Close()
		t.Fatalf("Attach() = %+v, want a refusal", att)
	}
	if att != nil {
		t.Errorf("Attach() returned an attachment alongside a refusal")
	}

	var refused *AttachError
	if !errors.As(err, &refused) {
		t.Fatalf("Attach() error = %v (%T), want an *AttachError", err, err)
	}
	if refused.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", refused.Status, http.StatusNotFound)
	}
	const want = "daemon refused attach (404): unknown session"
	if got := refused.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestAnUpgradeWithoutASessionIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{
			name: "no session",
			response: "HTTP/1.1 101 Switching Protocols\r\n" +
				"Connection: Upgrade\r\n" +
				"Upgrade: " + attachProtocol + "\r\n\r\n",
		},
		{
			name: "another protocol",
			response: "HTTP/1.1 101 Switching Protocols\r\n" +
				"Connection: Upgrade\r\n" +
				"Upgrade: something-else\r\n" +
				sessionHeader + ": sess-1\r\n\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := make(chan error, 1)
			socket := attachStub(t, func(conn net.Conn, req *http.Request) {
				_ = req.Body.Close()
				if _, err := io.WriteString(conn, tc.response); err != nil {
					closed <- err
					return
				}
				// The refusal must close the connection rather than
				// leave it dangling: a read on the far side sees EOF.
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, err := conn.Read(make([]byte, 1))
				closed <- err
			})

			att, err := NewClient(socket).Attach(context.Background(), "/work/root", "")
			if err == nil {
				_ = att.Conn.Close()
				t.Fatalf("Attach() accepted an upgrade with %s", tc.name)
			}
			if att != nil {
				t.Errorf("Attach() returned an attachment alongside an error")
			}

			select {
			case readErr := <-closed:
				if !errors.Is(readErr, io.EOF) {
					t.Errorf("the connection was not closed: read = %v", readErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the stub never saw the connection close")
			}
		})
	}
}

func TestTheContextBoundsTheHandshakeOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	socket := attachStub(t, func(conn net.Conn, req *http.Request) {
		_ = req.Body.Close()
		// Answer inside the deadline, then hold the stream past it: the
		// handshake is bounded, the stream is not.
		time.Sleep(50 * time.Millisecond)
		if err := writeUpgrade(conn, "sess-1", ""); err != nil {
			t.Errorf("stub write: %v", err)
			return
		}
		time.Sleep(800 * time.Millisecond)
		if _, err := io.WriteString(conn, "late bytes"); err != nil {
			t.Errorf("stub write: %v", err)
		}
	})

	att, err := NewClient(socket).Attach(ctx, "/work/root", "")
	if err != nil {
		t.Fatalf("Attach(): %v", err)
	}
	defer att.Conn.Close()

	// The context is spent from here on; the stream must still work.
	cancel()

	got := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, len("late bytes"))
		if _, err := io.ReadFull(att.Conn, buf); err != nil {
			readErr <- err
			return
		}
		got <- string(buf)
	}()

	select {
	case bytes := <-got:
		if bytes != "late bytes" {
			t.Errorf("stream read %q, want %q", bytes, "late bytes")
		}
	case err := <-readErr:
		t.Fatalf("read the stream after the handshake's deadline: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the stream stalled past the handshake's deadline")
	}
}

func TestACancelledHandshakeEndsTheAttach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := attachStub(t, func(conn net.Conn, req *http.Request) {
		_ = req.Body.Close()
		// Never answer: the handshake can only end by the caller giving up.
		_, _ = io.Copy(io.Discard, conn)
	})

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := NewClient(socket).Attach(ctx, "/work/root", ""); err == nil {
		t.Fatal("a cancelled attach returned a stream")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("Attach() error = %v, want a cancellation", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("attach took %s to notice the cancellation", elapsed)
	}
}

func TestInfoDecodesTheChildren(t *testing.T) {
	const body = `{"version":"1.0.0","profile":"local","pid":7,"startedAt":1,` +
		`"socket":"/tmp/marshal.sock","projects":["/work/one"],"children":[` +
		`{"root":"/work/one","version":"2.0.0","sessions":[` +
		`{"id":"sess-1","viewers":2,"lastActive":1700000000000}]},` +
		`{"root":"/work/two","version":"2.0.1","sessions":[]}]}`

	socket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))

	info, err := NewClient(socket).Info(context.Background())
	if err != nil {
		t.Fatalf("Info(): %v", err)
	}
	if len(info.Children) != 2 {
		t.Fatalf("Children = %+v, want two", info.Children)
	}

	first := info.Children[0]
	if first.Root != "/work/one" || first.Version != "2.0.0" || len(first.Sessions) != 1 {
		t.Fatalf("Children[0] = %+v, want /work/one 2.0.0 with one session", first)
	}
	if got := first.Sessions[0]; got.ID != "sess-1" || got.Viewers != 2 || got.LastActive != 1700000000000 {
		t.Errorf("Children[0].Sessions[0] = %+v, want sess-1, 2 viewers, lastActive 1700000000000", got)
	}

	second := info.Children[1]
	if second.Root != "/work/two" || second.Version != "2.0.1" || len(second.Sessions) != 0 {
		t.Errorf("Children[1] = %+v, want /work/two 2.0.1 with no sessions", second)
	}
}
