package tunnels

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"marshal/internal/fleetprofile"
)

func TestStatusArgvAsksForTheJSONStatus(t *testing.T) {
	argv := statusArgv("", fleetprofile.Host{Name: "box", SSH: "me@box"})
	want := []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"me@box",
		"'marshal' daemon status --json",
	}
	assertArgv(t, argv, want)
}

func TestRemoteCommandQuotesTheBinary(t *testing.T) {
	cases := []struct{ marshal, want string }{
		{"/opt/my marshal/marshal", "'/opt/my marshal/marshal' daemon status --json"},
		{"/o'dd/marshal", `'/o'\''dd/marshal' daemon status --json`},
		{"~/.local/bin/marshal", "~/'.local/bin/marshal' daemon status --json"},
	}
	for _, c := range cases {
		if got := remoteCommand(c.marshal); got != c.want {
			t.Errorf("remoteCommand(%q) = %q, want %q", c.marshal, got, c.want)
		}
	}
}

func TestForwardArgvWithoutPreviews(t *testing.T) {
	const (
		local  = "/run/u/.local/share/marshal/daemon/hosts/box.sock"
		remote = "/home/u/.local/share/marshal/daemon/daemon.sock"
	)
	argv := forwardArgv("", fleetprofile.Host{Name: "box", SSH: "me@box"}, local, remote, 0, "")
	want := []string{
		"ssh", "-N",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", local + ":" + remote,
		"me@box",
	}
	assertArgv(t, argv, want)

	// An address without the port it is forwarded to is no forward: the
	// listener would be 127.0.0.1:0, which carries nothing.
	argv = forwardArgv("", fleetprofile.Host{Name: "box", SSH: "me@box"}, local, remote, 0, "127.0.0.1:54321")
	assertArgv(t, argv, want)
	if forwarded := forwards(argv); len(forwarded) != 1 {
		t.Fatalf("the forward's -L arguments = %q, want only the socket's", forwarded)
	}
}

func TestForwardArgvForwardsThePreviewPort(t *testing.T) {
	const (
		local  = "/run/u/.local/share/marshal/daemon/hosts/box.sock"
		remote = "/home/u/.local/share/marshal/daemon/daemon.sock"
	)
	host := fleetprofile.Host{Name: "box", SSH: "me@box"}
	argv := forwardArgv("", host, local, remote, 54400, "127.0.0.1:54321")
	want := []string{
		"ssh", "-N",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", local + ":" + remote,
		"-L", "127.0.0.1:54400:127.0.0.1:54321",
		"me@box",
	}
	assertArgv(t, argv, want)

	// An IPv6 listener is bracketed, as net.JoinHostPort writes it.
	argv = forwardArgv("", host, local, remote, 54400, "[::1]:54321")
	if got, want := argv[len(argv)-2], "127.0.0.1:54400:[::1]:54321"; got != want {
		t.Fatalf("the preview forward = %q, want %q (argv %q)", got, want, argv)
	}
}

func TestRemoteDaemonReadsThePreviewAddr(t *testing.T) {
	doc, err := os.ReadFile("../../test/contract/daemon-status.json")
	if err != nil {
		t.Fatalf("read the status contract: %v", err)
	}
	socket, previewAddr, err := remoteDaemon("box", doc)
	if err != nil {
		t.Fatalf("remoteDaemon: %v", err)
	}
	want := "/home/u/.local/share/marshal/daemon/daemon.sock"
	if socket != want {
		t.Fatalf("remoteDaemon socket = %q, want %q", socket, want)
	}
	if wantAddr := "127.0.0.1:54321"; previewAddr != wantAddr {
		t.Fatalf("remoteDaemon preview address = %q, want %q", previewAddr, wantAddr)
	}
}

func TestRemoteDaemonIgnoresANonLoopbackPreviewAddr(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:54321", "box.lan:54321", "[2001:db8::1]:54321", "garbage"} {
		doc := []byte(`{"schemaVersion":1,"answering":true,"daemon":{"socket":"/tmp/box.sock","previewAddr":"` + addr + `"}}`)
		_, previewAddr, err := remoteDaemon("box", doc)
		if err != nil {
			t.Fatalf("remoteDaemon(%q): %v", addr, err)
		}
		if previewAddr != "" {
			t.Errorf("remoteDaemon forwarded %q for the preview address %q, want none", previewAddr, addr)
		}
	}
}

func TestLoopbackAddrRefusesAnUnusablePort(t *testing.T) {
	// A port that is not a number, or outside 1..65535, is one ssh cannot
	// forward: it fails every attempt, so it is no preview listener.
	for _, addr := range []string{"127.0.0.1:abc", "localhost:99999", "127.0.0.1:0", "[::1]:-1", "::1:80"} {
		if got := loopbackAddr(addr); got != "" {
			t.Errorf("loopbackAddr(%q) = %q, want none", addr, got)
		}
	}
	for _, addr := range []string{"127.0.0.1:54321", "localhost:1", "[::1]:65535"} {
		if got := loopbackAddr(addr); got != addr {
			t.Errorf("loopbackAddr(%q) = %q, want it kept", addr, got)
		}
	}
}

func TestRemoteDaemonSaysTheDaemonIsNotAnswering(t *testing.T) {
	doc := []byte(`{"schemaVersion":1,"answering":false}`)
	_, _, err := remoteDaemon("box", doc)
	if err == nil {
		t.Fatal("remoteDaemon accepted a daemon that is not answering")
	}
	if want := "the daemon on box is not answering; start it there with marshal daemon start"; err.Error() != want {
		t.Fatalf("remoteDaemon error = %q, want %q", err.Error(), want)
	}
}

func TestRemoteDaemonRefusesAnotherSchema(t *testing.T) {
	doc := []byte(`{"schemaVersion":2,"answering":true,"daemon":{"socket":"/tmp/box.sock"}}`)
	_, _, err := remoteDaemon("box", doc)
	if err == nil {
		t.Fatal("remoteDaemon accepted schema 2")
	}
	if want := "box's marshal writes status schema 2; this marshal reads 1"; err.Error() != want {
		t.Fatalf("remoteDaemon error = %q, want %q", err.Error(), want)
	}
}

func TestFreeLoopbackPortAnswersAPort(t *testing.T) {
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatalf("freeLoopbackPort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("freeLoopbackPort = %d, want a port in range", port)
	}
	// The port it answered was free at the time: it can be bound now.
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("the answered port %d is not free: %v", port, err)
	}
	listener.Close()
}

func TestSocketPathRefusesALongName(t *testing.T) {
	_, err := socketPath("/tmp/m", strings.Repeat("n", 100))
	if err == nil {
		t.Fatal("socketPath accepted a 100-byte name")
	}
	if want := "is longer than a unix socket allows"; !strings.Contains(err.Error(), want) {
		t.Fatalf("socketPath error = %q, want it to contain %q", err.Error(), want)
	}
	path, err := socketPath("/tmp/m", "box")
	if err != nil {
		t.Fatalf("socketPath(box): %v", err)
	}
	if want := "/tmp/m/box.sock"; path != want {
		t.Fatalf("socketPath(box) = %q, want %q", path, want)
	}
}

// assertArgv fails unless got is exactly want.
func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (whole argv %q)", i, got[i], want[i], got)
		}
	}
}
