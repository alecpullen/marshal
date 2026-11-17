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
