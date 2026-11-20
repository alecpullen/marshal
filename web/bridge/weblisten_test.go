package bridge

import (
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// unixSocketPathLimit is the shortest unix socket path limit of the systems
// these tests run on (macOS caps one at 104 bytes, Linux at 108). A socket
// path over it is skipped rather than failed: which temporary directory the
// platform hands out is not this test's subject.
const unixSocketPathLimit = 104

// webSocketPath joins web.sock onto dir, skipping the case rather than failing
// when the platform's limit leaves no room for that path.
func webSocketPath(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, webSocketName)
	if len(path) > unixSocketPathLimit {
		t.Skipf("socket path %q is %d bytes, over the %d-byte limit", path, len(path), unixSocketPathLimit)
	}
	return path
}

// webSocketGID reads a socket's group id without naming the platform's stat
// layout, which differs between the systems that have unix sockets; ok is
// false where the layout carries no gid at all.
func webSocketGID(fi os.FileInfo) (gid int, ok bool) {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	f := v.FieldByName("Gid")
	if !f.IsValid() {
		return 0, false
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		return int(f.Int()), true
	case reflect.Uint, reflect.Uint32, reflect.Uint64:
		return int(f.Uint()), true
	}
	return 0, false
}

func TestParseWebListenReadsUnix(t *testing.T) {
	got, err := ParseWebListen("unix", "", "/run/marshal/daemon/marshal.sock")
	if err != nil {
		t.Fatalf("ParseWebListen(unix): %v", err)
	}
	want := WebListen{Network: "unix", Address: "/run/marshal/daemon/web.sock"}
	if got != want {
		t.Errorf("ParseWebListen(unix) = %+v, want %+v", got, want)
	}
	if s := got.String(); s != "unix:/run/marshal/daemon/web.sock" {
		t.Errorf("String() = %q, want %q", s, "unix:/run/marshal/daemon/web.sock")
	}
}

func TestParseWebListenReadsAPath(t *testing.T) {
	got, err := ParseWebListen("/run/marshal/web.sock", "", "/run/marshal/daemon/marshal.sock")
	if err != nil {
		t.Fatalf("ParseWebListen(path): %v", err)
	}
	want := WebListen{Network: "unix", Address: "/run/marshal/web.sock"}
	if got != want {
		t.Errorf("ParseWebListen(path) = %+v, want %+v", got, want)
	}

	// A group belongs on an absolute path, and is kept there.
	shared, err := ParseWebListen("/run/marshal/web.sock", "marshal-web", "/run/marshal/daemon/marshal.sock")
	if err != nil {
		t.Fatalf("ParseWebListen(path, group): %v", err)
	}
	want = WebListen{Network: "unix", Address: "/run/marshal/web.sock", Group: "marshal-web"}
	if shared != want {
		t.Errorf("ParseWebListen(path, group) = %+v, want %+v", shared, want)
	}
}

func TestParseWebListenReadsALoopbackAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7700", "[::1]:7700", "localhost:7700"} {
		got, err := ParseWebListen(addr, "", "/run/marshal/daemon/marshal.sock")
		if err != nil {
			t.Errorf("ParseWebListen(%s): %v", addr, err)
			continue
		}
		want := WebListen{Network: "tcp", Address: addr}
		if got != want {
			t.Errorf("ParseWebListen(%s) = %+v, want %+v", addr, got, want)
		}
		if s := got.String(); s != addr {
			t.Errorf("String() = %q, want %q", s, addr)
		}
	}
}

func TestParseWebListenRefusesANetworkAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7700", ":7700", "192.168.1.2:7700", "box.local:7700"} {
		got, err := ParseWebListen(addr, "", "/run/marshal/daemon/marshal.sock")
		if err == nil {
			t.Errorf("ParseWebListen(%s) accepted a network address: %+v", addr, got)
			continue
		}
		for _, want := range []string{"--web-listen", "D3"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ParseWebListen(%s) error = %q, want it to name %s", addr, err, want)
			}
		}
	}
}

func TestAWebGroupNeedsAnAbsolutePath(t *testing.T) {
	_, err := ParseWebListen("unix", "marshal-web", "/run/marshal/daemon/marshal.sock")
	if err == nil {
		t.Fatal("ParseWebListen(unix, group) accepted a group")
	}
	const wantUnix = "--web-group needs --web-listen to be an absolute socket path"
	if !strings.Contains(err.Error(), wantUnix) {
		t.Errorf("ParseWebListen(unix, group) error = %q, want it to contain %q", err, wantUnix)
	}

	_, err = ParseWebListen("127.0.0.1:7700", "marshal-web", "/run/marshal/daemon/marshal.sock")
	if err == nil {
		t.Fatal("ParseWebListen(loopback, group) accepted a group")
	}
	const wantTCP = "--web-group is for a unix --web-listen"
	if !strings.Contains(err.Error(), wantTCP) {
		t.Errorf("ParseWebListen(loopback, group) error = %q, want it to contain %q", err, wantTCP)
	}
}

func TestListenWebMakesAPrivateSocket(t *testing.T) {
	path := webSocketPath(t, t.TempDir())

	ln, err := ListenWeb(WebListen{Network: "unix", Address: path})
	if err != nil {
		t.Fatalf("ListenWeb(%s): %v", path, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		_ = ln.Close()
		t.Errorf("socket mode = %04o, want 0600", got)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		_ = ln.Close()
		t.Errorf("%s is not a socket (mode %v)", path, fi.Mode())
	}

	client, err := net.Dial("unix", path)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("dial the web listener: %v", err)
	}
	served, err := acceptWithin(t, ln, 5*time.Second)
	if err != nil {
		_ = client.Close()
		_ = ln.Close()
		t.Fatalf("web listener did not accept: %v", err)
	}
	_ = served.Close()
	_ = client.Close()

	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket %s survived Close: %v", path, err)
	}
}

func TestListenWebSharesASocketWithAGroup(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("the user's primary group %s has no name: %v", u.Gid, err)
	}
	path := webSocketPath(t, t.TempDir())

	ln, err := ListenWeb(WebListen{Network: "unix", Address: path, Group: g.Name})
	if err != nil {
		t.Fatalf("ListenWeb(%s, group %s): %v", path, g.Name, err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o660 {
		t.Errorf("shared socket mode = %04o, want 0660", got)
	}
	gid, ok := webSocketGID(fi)
	if !ok {
		t.Skipf("this system's stat layout carries no gid to check")
	}
	if got := strconv.Itoa(gid); got != u.Gid {
		t.Errorf("shared socket gid = %s, want %s", got, u.Gid)
	}
}

func TestListenWebRefusesAnUnknownGroup(t *testing.T) {
	path := webSocketPath(t, t.TempDir())
	const group = "marshal-no-such-group"

	ln, err := ListenWeb(WebListen{Network: "unix", Address: path, Group: group})
	if err == nil {
		_ = ln.Close()
		t.Fatalf("ListenWeb accepted unknown group %s", group)
	}
	if !strings.Contains(err.Error(), group) {
		t.Errorf("error = %q, want it to name %s", err, group)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket %s left behind after a refused group: %v", path, err)
	}
}

func TestListenWebReplacesAStaleSocket(t *testing.T) {
	path := webSocketPath(t, t.TempDir())

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

	ln, err := ListenWeb(WebListen{Network: "unix", Address: path})
	if err != nil {
		t.Fatalf("ListenWeb did not replace a stale socket: %v", err)
	}
	defer ln.Close()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the replacement listener: %v", err)
	}
	defer client.Close()

	served, err := acceptWithin(t, ln, 5*time.Second)
	if err != nil {
		t.Fatalf("replacement listener did not accept: %v", err)
	}
	_ = served.Close()
}

func TestListenWebRefusesALiveSocket(t *testing.T) {
	path := webSocketPath(t, t.TempDir())

	first, err := ListenWeb(WebListen{Network: "unix", Address: path})
	if err != nil {
		t.Fatalf("ListenWeb(%s): %v", path, err)
	}
	defer first.Close()

	second, err := ListenWeb(WebListen{Network: "unix", Address: path})
	if !errors.Is(err, ErrDaemonRunning) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second ListenWeb error = %v, want ErrDaemonRunning", err)
	}

	// The refused attempt probes the live listener, so its connection is
	// already queued; take it before asserting on our own dial.
	probe, err := acceptWithin(t, first, 5*time.Second)
	if err != nil {
		t.Fatalf("Accept of the refused attempt's probe: %v", err)
	}
	_ = probe.Close()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the live listener after a refused takeover: %v", err)
	}
	defer client.Close()

	served, err := acceptWithin(t, first, 5*time.Second)
	if err != nil {
		t.Fatalf("live listener stopped accepting: %v", err)
	}
	_ = served.Close()
}

func TestListenWebRefusesADirectoryOthersCanWrite(t *testing.T) {
	dir := t.TempDir()
	// The umask may have narrowed what Mkdir asked for; group and other
	// writes are what this test is about.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod directory: %v", err)
	}
	path := webSocketPath(t, dir)

	ln, err := ListenWeb(WebListen{Network: "unix", Address: path})
	if err == nil {
		_ = ln.Close()
		t.Fatal("ListenWeb accepted a directory writable by group and other")
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat refused directory: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o777 {
		t.Errorf("refused directory mode = %04o, want 0777 (left as it was)", got)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket created in a directory ListenWeb refused: %v", err)
	}
}

func TestListenWebOpensALoopbackAddress(t *testing.T) {
	ln, err := ListenWeb(WebListen{Network: "tcp", Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("ListenWeb(tcp 127.0.0.1:0): %v", err)
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("%T is not a *net.TCPAddr", ln.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("web listener bound %s, want a loopback address", addr)
	}
}
