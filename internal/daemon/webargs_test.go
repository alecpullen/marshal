package daemon

import (
	"strings"
	"testing"
	"time"
)

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
		},
		IdleSessionTimeout: time.Minute,
		WebListen:          "/run/marshal/web.sock",
		WebGroup:           "marshal",
		PublicURL:          "https://box.example.com",
		PreviewListen:      "127.0.0.1:7790",
		PreviewURL:         "https://preview.example.com",
		ProjectsDir:        "~/code",
	}

	argv := spec.Args()
	want := [][2]string{
		{"--web-listen", "/run/marshal/web.sock"},
		{"--web-group", "marshal"},
		{"--public-url", "https://box.example.com"},
		{"--preview-addr", "127.0.0.1:7790"},
		{"--preview-url", "https://preview.example.com"},
		{"--projects-dir", "~/code"},
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
		},
		IdleSessionTimeout: time.Minute,
	}

	flags := []string{"--web-listen", "--web-group", "--public-url", "--preview-addr", "--preview-url", "--projects-dir"}
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
