package tunnels

import (
	"os"
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

func TestForwardArgvForwardsTheSocket(t *testing.T) {
	const (
		local  = "/run/u/.local/share/marshal/daemon/hosts/box.sock"
		remote = "/home/u/.local/share/marshal/daemon/daemon.sock"
	)
	argv := forwardArgv("", fleetprofile.Host{Name: "box", SSH: "me@box"}, local, remote)
	want := []string{
		"ssh", "-N",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", local + ":" + remote,
		"me@box",
	}
	assertArgv(t, argv, want)
}

func TestRemoteSocketReadsTheDocument(t *testing.T) {
	doc, err := os.ReadFile("../../test/contract/daemon-status.json")
	if err != nil {
		t.Fatalf("read the status contract: %v", err)
	}
	socket, err := remoteSocket("box", doc)
	if err != nil {
		t.Fatalf("remoteSocket: %v", err)
	}
	want := "/home/u/.local/share/marshal/daemon/daemon.sock"
	if socket != want {
		t.Fatalf("remoteSocket = %q, want %q", socket, want)
	}
}

func TestRemoteSocketSaysTheDaemonIsNotAnswering(t *testing.T) {
	doc := []byte(`{"schemaVersion":1,"answering":false}`)
	_, err := remoteSocket("box", doc)
	if err == nil {
		t.Fatal("remoteSocket accepted a daemon that is not answering")
	}
	if want := "the daemon on box is not answering; start it there with marshal daemon start"; err.Error() != want {
		t.Fatalf("remoteSocket error = %q, want %q", err.Error(), want)
	}
}

func TestRemoteSocketRefusesAnotherSchema(t *testing.T) {
	doc := []byte(`{"schemaVersion":2,"answering":true,"daemon":{"socket":"/tmp/box.sock"}}`)
	_, err := remoteSocket("box", doc)
	if err == nil {
		t.Fatal("remoteSocket accepted schema 2")
	}
	if want := "box's marshal writes status schema 2; this marshal reads 1"; err.Error() != want {
		t.Fatalf("remoteSocket error = %q, want %q", err.Error(), want)
	}
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
