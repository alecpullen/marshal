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
	// The tunnel helper stays the last word, and the refresh helper just
	// before it: neither is a value the user set (decision 5).
	if last := argv[len(argv)-1]; last != "--tunnels" {
		t.Errorf("args end with %q, want --tunnels", last)
	}
	if prev := argv[len(argv)-2]; prev != "--oauth-refresh" {
		t.Errorf("args end with %q before --tunnels, want --oauth-refresh", prev)
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

// TestArgsWriteTheWebListener pins the five arguments the web listener and
// the origins it advertises add (D3): each is a flag and its value, they come
// in decision 10's order, and they sit after the proxy and before the helper
// flags that close the list.
func TestArgsWriteTheWebListener(t *testing.T) {
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
		WebListen:          "/run/marshal/web.sock",
		WebGroup:           "marshal",
		PublicURL:          "https://box.example.com",
		PreviewListen:      "127.0.0.1:7790",
		PreviewURL:         "https://preview.example.com",
	}

	argv := spec.Args()
	want := [][2]string{
		{"--web-listen", "/run/marshal/web.sock"},
		{"--web-group", "marshal"},
		{"--public-url", "https://box.example.com"},
		{"--preview-addr", "127.0.0.1:7790"},
		{"--preview-url", "https://preview.example.com"},
	}
	at := argvIndexOf(t, argv, "--egress")
	for _, pair := range want {
		flag := argvIndexOf(t, argv, pair[0])
		if flag < at {
			t.Errorf("args = %q, want %s after the argument before it", argv, pair[0])
		}
		if flag+1 >= len(argv) || argv[flag+1] != pair[1] {
			t.Fatalf("args = %q, want %s followed by %q", argv, pair[0], pair[1])
		}
		at = flag
	}
	// The helper flags still have the last word, after the values the user
	// set.
	if oauth := argvIndexOf(t, argv, "--oauth-refresh"); oauth < at {
		t.Errorf("args = %q, want the web listener before --oauth-refresh", argv)
	}
	if last := argv[len(argv)-1]; last != "--tunnels" {
		t.Errorf("args end with %q, want --tunnels", last)
	}
}

// TestArgsWriteNoWebFlagsByDefault holds the other half (D3): a unit that
// carries no web listener writes none of the five flags, and one that carries
// a single value writes only its flag.
func TestArgsWriteNoWebFlagsByDefault(t *testing.T) {
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

	flags := []string{"--web-listen", "--web-group", "--public-url", "--preview-addr", "--preview-url"}
	joined := strings.Join(spec.Args(), " ")
	for _, flag := range flags {
		if strings.Contains(joined, flag) {
			t.Errorf("args without a web listener carry %s: %q", flag, joined)
		}
	}

	// Only the origin the page is reached on: the other four stay absent.
	spec.PublicURL = "https://box.example.com"
	argv := spec.Args()
	joined = strings.Join(argv, " ")
	if want := "--public-url https://box.example.com"; !strings.Contains(joined, want) {
		t.Errorf("args with a public URL = %q, want it to carry %q", joined, want)
	}
	for _, flag := range flags {
		if flag == "--public-url" {
			continue
		}
		if strings.Contains(joined, flag) {
			t.Errorf("args with only a public URL carry %s: %q", flag, joined)
		}
	}
}

// argvIndexOf is the position of flag in argv, or a fatal failure: the tests
// above are about arguments that must be there.
func argvIndexOf(t *testing.T, argv []string, flag string) int {
	t.Helper()
	for i, arg := range argv {
		if arg == flag {
			return i
		}
	}
	t.Fatalf("args = %q, want them to carry %s", argv, flag)
	return -1
}
