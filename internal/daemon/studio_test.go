package daemon

import (
	"strings"
	"testing"
	"time"
)

// TestArgsServeTheStudioWhenAsked pins the two arguments the Studio listener
// adds, and that leaving it unset keeps the socket-only daemon's argv: a
// daemon with no browser door must not open a TCP port by accident.
func TestArgsServeTheStudioWhenAsked(t *testing.T) {
	spec := UnitSpec{
		Webbridge: "/bin/webbridge",
		Marshal:   "/bin/marshal",
		Paths: Paths{
			Socket:   "/d/marshal.sock",
			StateDir: "/d/state",
			Registry: "/d/fleet.json",
			Token:    "/d/studio.token",
		},
		IdleSessionTimeout: time.Minute,
	}

	joined := strings.Join(spec.Args(), " ")
	if !strings.Contains(joined, "--addr off") {
		t.Errorf("args without a studio address = %q, want --addr off", joined)
	}
	if strings.Contains(joined, "--token-file") {
		t.Errorf("args without a studio address carry --token-file: %q", joined)
	}

	spec.StudioAddr = "127.0.0.1:7788"
	argv := spec.Args()
	joined = strings.Join(argv, " ")
	for _, want := range []string{"--addr 127.0.0.1:7788", "--token-file /d/studio.token"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args with a studio address = %q, want it to carry %q", joined, want)
		}
	}
	if strings.Contains(joined, "--addr off") {
		t.Errorf("args with a studio address still disable the listener: %q", joined)
	}
	// The refresh helper stays the last word: it is the one argument that is
	// not a value the user set (decision 5).
	if last := argv[len(argv)-1]; last != "--oauth-refresh" {
		t.Errorf("args end with %q, want --oauth-refresh", last)
	}
}

// TestArgsWriteTheJoin pins P8 D12's relay arguments: a join writes where it
// joins, under what name, the secret file the daemon reads, and the CA bundle
// when the config names one — after --workspace, which names the project the
// daemon serves.
func TestArgsWriteTheJoin(t *testing.T) {
	spec := UnitSpec{
		Webbridge: "/bin/webbridge",
		Marshal:   "/bin/marshal",
		Paths: Paths{
			Socket:    "/d/marshal.sock",
			StateDir:  "/d/state",
			Registry:  "/d/fleet.json",
			Token:     "/d/studio.token",
			JoinToken: "/d/join.token",
		},
		IdleSessionTimeout: time.Minute,
		Join: &Join{
			URL:  "https://relay.example.com",
			Name: "box",
		},
	}

	argv := spec.Args()
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--join https://relay.example.com",
		"--join-name box",
		"--join-token-file /d/join.token",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args with a join = %q, want it to carry %q", joined, want)
		}
	}
	// No CA file configured: the relay's certificate comes from the system
	// store, so the flag is absent rather than empty.
	if strings.Contains(joined, "--join-ca-file") {
		t.Errorf("args with no CA file carry --join-ca-file: %q", joined)
	}
	// The join follows the project registry it serves beside.
	workspaceAt := strings.Index(joined, "--workspace")
	joinAt := strings.Index(joined, "--join")
	if workspaceAt < 0 || joinAt < workspaceAt {
		t.Errorf("args = %q, want --workspace before --join", joined)
	}

	spec.Join.CAFile = "/d/relay-ca.pem"
	joined = strings.Join(spec.Args(), " ")
	if want := "--join-ca-file /d/relay-ca.pem"; !strings.Contains(joined, want) {
		t.Errorf("args with a CA file = %q, want it to carry %q", joined, want)
	}
}

// TestArgsWriteTheTokenFileForAJoinWithoutAStudio holds the other half: a
// daemon that joins a relay has a listener whatever daemon.studio_addr says,
// so the unit names the token file it answers on even with the Studio off.
func TestArgsWriteTheTokenFileForAJoinWithoutAStudio(t *testing.T) {
	spec := UnitSpec{
		Webbridge: "/bin/webbridge",
		Marshal:   "/bin/marshal",
		Paths: Paths{
			Socket:    "/d/marshal.sock",
			StateDir:  "/d/state",
			Registry:  "/d/fleet.json",
			Token:     "/d/studio.token",
			JoinToken: "/d/join.token",
		},
		IdleSessionTimeout: time.Minute,
		Join:               &Join{URL: "https://relay.example.com", Name: "box"},
	}

	joined := strings.Join(spec.Args(), " ")
	if !strings.Contains(joined, "--addr off") {
		t.Errorf("args with a join and no studio address = %q, want --addr off", joined)
	}
	if want := "--token-file /d/studio.token"; !strings.Contains(joined, want) {
		t.Errorf("args with a join and no studio address = %q, want it to carry %q", joined, want)
	}
}
