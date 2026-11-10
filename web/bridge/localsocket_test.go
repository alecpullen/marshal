package bridge

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// socketDir creates the private directory a socket goes in, one level
// below t.TempDir: testing's temp dir asks for 0777 (0755 after the
// umask), which is exactly the directory decision 5 refuses. The nesting
// keeps the path short too — macOS caps a unix socket path at 104 bytes.
func socketDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "daemon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("make socket directory: %v", err)
	}
	return dir
}

// socketPath returns a fresh socket path under socketDir.
func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(socketDir(t), "daemon.sock")
}

// requirePeerCreds skips on the systems where peerUID answers an error
// by design (decision 6).
func requirePeerCreds(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("peer credentials are not supported on %s", runtime.GOOS)
	}
}

// acceptWithin fails the test rather than blocking forever when Accept
// never produces a connection.
func acceptWithin(t *testing.T, ln net.Listener, d time.Duration) (net.Conn, error) {
	t.Helper()
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- result{conn: conn, err: err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-time.After(d):
		t.Fatalf("Accept did not return within %v", d)
		return nil, nil
	}
}

func TestListenLocalMakesAPrivateSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daemon")
	path := filepath.Join(dir, "bridge.sock")

	ln, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal(%s): %v", path, err)
	}
	defer ln.Close()

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("socket directory mode = %04o, want 0700", got)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %04o, want 0600", got)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("%s is not a socket (mode %v)", path, fi.Mode())
	}
}

func TestListenLocalRefusesALiveDaemon(t *testing.T) {
	path := socketPath(t)

	first, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal(%s): %v", path, err)
	}
	defer first.Close()

	second, err := ListenLocal(path)
	if !errors.Is(err, ErrDaemonRunning) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second ListenLocal error = %v, want ErrDaemonRunning", err)
	}

	// The refused attempt probes the first listener, so its connection is
	// already queued; take it before asserting on our own dial.
	probe, err := acceptWithin(t, first, 5*time.Second)
	if err != nil {
		t.Fatalf("Accept of the refused attempt's probe: %v", err)
	}
	_ = probe.Close()

	// The first daemon still accepts a dial.
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the live daemon after a refused takeover: %v", err)
	}
	defer client.Close()

	served, err := acceptWithin(t, first, 5*time.Second)
	if err != nil {
		t.Fatalf("live daemon stopped accepting: %v", err)
	}
	defer served.Close()
}

func TestListenLocalReplacesAStaleSocket(t *testing.T) {
	path := socketPath(t)

	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("make stale socket: %v", err)
	}
	unix, ok := stale.(*net.UnixListener)
	if !ok {
		t.Fatalf("%T is not a *net.UnixListener", stale)
	}
	// Leave the socket file behind, as a crashed daemon does.
	unix.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket was not left at %s: %v", path, err)
	}

	ln, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal did not replace a stale socket: %v", err)
	}
	defer ln.Close()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the replacement daemon: %v", err)
	}
	defer client.Close()

	served, err := acceptWithin(t, ln, 5*time.Second)
	if err != nil {
		t.Fatalf("replacement daemon did not accept: %v", err)
	}
	_ = served.Close()
}

func TestListenLocalRefusesAnOpenDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("make directory: %v", err)
	}
	// The umask may have narrowed the mode Mkdir asked for.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod directory: %v", err)
	}
	path := filepath.Join(dir, "bridge.sock")

	ln, err := ListenLocal(path)
	if err == nil {
		_ = ln.Close()
		t.Fatal("ListenLocal accepted a directory open to group and other")
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat refused directory: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("refused directory mode = %04o, want 0755 (left as it was)", got)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket created in a directory ListenLocal refused: %v", err)
	}
}

func TestListenLocalLeavesAFileAlone(t *testing.T) {
	path := socketPath(t)
	const content = "not a socket"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ln, err := ListenLocal(path)
	if err == nil {
		_ = ln.Close()
		t.Fatal("ListenLocal removed a regular file")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("regular file at the path was not kept: %v", err)
	}
	if string(got) != content {
		t.Errorf("file content = %q, want %q", got, content)
	}
}

func TestClosingRemovesTheSocket(t *testing.T) {
	path := socketPath(t)

	ln, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal(%s): %v", path, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket %s survived Close: %v", path, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the directory was removed with the socket: %v", err)
	}
}

func TestPeerUIDIsOurs(t *testing.T) {
	requirePeerCreds(t)
	path := socketPath(t)

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	served, err := acceptWithin(t, ln, 5*time.Second)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer served.Close()

	uid, err := peerUID(served)
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if uid != os.Getuid() {
		t.Errorf("peer uid = %d, want %d", uid, os.Getuid())
	}
}

func TestPeerCheckedRefusesAnotherUser(t *testing.T) {
	requirePeerCreds(t)
	path := socketPath(t)

	base, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal(%s): %v", path, err)
	}

	logs := &syncBuffer{}
	refusing := PeerChecked(base, os.Getuid()+1, slog.New(slog.NewTextHandler(logs, nil)))

	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		conn, err := refusing.Accept()
		accepted <- result{conn: conn, err: err}
	}()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// The kernel accepts the dial; the wrapper is what closes it, so the
	// client sees a clean EOF rather than a refused connect.
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := client.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("dial from another user read = %v, want EOF within 1s", err)
	}

	// Refused means the wrapper looped back to Accept, not that it handed
	// the connection on.
	select {
	case r := <-accepted:
		if r.conn != nil {
			_ = r.conn.Close()
		}
		t.Fatalf("Accept returned a connection for another user (err %v)", r.err)
	case <-time.After(200 * time.Millisecond):
	}

	// The log names the peer — us — not the uid that was wanted.
	if !strings.Contains(logs.String(), fmt.Sprintf("uid=%d", os.Getuid())) {
		t.Errorf("refusal was not logged with the peer's uid; log: %s", logs.String())
	}

	// Closing the wrapper closes the wrapped listener.
	if err := refusing.Close(); err != nil {
		t.Fatalf("close wrapper: %v", err)
	}

	// Wanted uid = ours: the same kind of client is served.
	base2, err := ListenLocal(path)
	if err != nil {
		t.Fatalf("ListenLocal after closing the wrapper: %v", err)
	}
	defer base2.Close()
	serving := PeerChecked(base2, os.Getuid(), slog.New(slog.DiscardHandler))

	client2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial as our own user: %v", err)
	}
	defer client2.Close()

	served, err := acceptWithin(t, serving, 5*time.Second)
	if err != nil {
		t.Fatalf("Accept for our own user: %v", err)
	}
	defer served.Close()

	uid, err := peerUID(served)
	if err != nil {
		t.Fatalf("peerUID of the served connection: %v", err)
	}
	if uid != os.Getuid() {
		t.Errorf("served peer uid = %d, want %d", uid, os.Getuid())
	}
}

func TestDefaultDaemonDirFollowsTheDataDirRule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name   string
		data   string
		xdg    string
		wanted string
	}{
		{
			name:   "MARSHAL_DATA_DIR wins",
			data:   filepath.Join(home, "data"),
			xdg:    filepath.Join(home, "xdg"),
			wanted: filepath.Join(home, "data", "daemon"),
		},
		{
			name:   "XDG_DATA_HOME second",
			xdg:    filepath.Join(home, "xdg"),
			wanted: filepath.Join(home, "xdg", "marshal", "daemon"),
		},
		{
			name:   "home last",
			wanted: filepath.Join(home, ".local", "share", "marshal", "daemon"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MARSHAL_DATA_DIR", tc.data)
			t.Setenv("XDG_DATA_HOME", tc.xdg)

			got, err := DefaultDaemonDir()
			if err != nil {
				t.Fatalf("DefaultDaemonDir(): %v", err)
			}
			if got != tc.wanted {
				t.Errorf("DefaultDaemonDir() = %q, want %q", got, tc.wanted)
			}
		})
	}
}
