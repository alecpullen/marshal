package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/daemon"
)

// studioReplaceApp replaces the app opener: the test says what opening the app
// does, on a platform that has none to open.
func studioReplaceApp(t *testing.T, run func(context.Context) error) {
	t.Helper()
	old := studioOpenApp
	t.Cleanup(func() { studioOpenApp = old })
	studioOpenApp = run
}

// studioReplaceURL replaces the browser opener and answers the address it was
// asked for. A test that must not open a browser passes a nil want.
func studioReplaceURL(t *testing.T) *string {
	t.Helper()
	old := studioOpenURL
	t.Cleanup(func() { studioOpenURL = old })
	opened := ""
	studioOpenURL = func(url string) error {
		opened = url
		return nil
	}
	return &opened
}

// runStudioCommand runs the command and answers what it wrote.
func runStudioCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runStudio(context.Background(), args, &out, &errOut)
	return out.String(), errOut.String(), err
}

// TestStudioOpensTheApp: the native app is the first door (D14), so it is
// opened and nothing else is read or opened.
func TestStudioOpensTheApp(t *testing.T) {
	daemonTestEnv(t)

	appCalls := 0
	studioReplaceApp(t, func(context.Context) error {
		appCalls++
		return nil
	})
	url := studioReplaceURL(t)

	stdout, stderr, err := runStudioCommand(t)
	if err != nil {
		t.Fatalf("runStudio: %v\nstderr: %s", err, stderr)
	}
	if appCalls != 1 {
		t.Errorf("the app was opened %d times, want once", appCalls)
	}
	if want := "studio: opened the Marshal app\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if *url != "" {
		t.Errorf("opened the browser at %q, want nothing", *url)
	}
}

// TestStudioOpensTheWebURLWithoutAnApp: with no app installed, the web Studio
// daemon.web_url names is opened, and its address is printed.
func TestStudioOpensTheWebURLWithoutAnApp(t *testing.T) {
	home := daemonTestEnv(t)
	studioReplaceApp(t, func(context.Context) error { return errNoStudioApp })
	url := studioReplaceURL(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_url = \"https://box.example.com\"\n")
	// The listener is up, so the daemon's answer adds no warning.
	fakeDaemonSocket(t, fakeDaemonClient{info: daemon.Info{WebListen: "127.0.0.1:7788"}})

	stdout, stderr, err := runStudioCommand(t)
	if err != nil {
		t.Fatalf("runStudio: %v\nstderr: %s", err, stderr)
	}
	if want := "https://box.example.com"; *url != want {
		t.Errorf("opened %q, want %q", *url, want)
	}
	if want := "studio: https://box.example.com\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

// TestStudioWarnsWhenTheAppWillNotOpen: an opener failure that is not the
// absent-app answer is a warning, not a dead end. The web Studio is tried
// anyway, so it is opened and its address printed.
func TestStudioWarnsWhenTheAppWillNotOpen(t *testing.T) {
	home := daemonTestEnv(t)
	studioReplaceApp(t, func(context.Context) error {
		return errors.New("open the Marshal app: launchd refused")
	})
	url := studioReplaceURL(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_url = \"https://box.example.com\"\n")
	fakeDaemonSocket(t, fakeDaemonClient{info: daemon.Info{WebListen: "127.0.0.1:7788"}})

	stdout, stderr, err := runStudioCommand(t)
	if err != nil {
		t.Fatalf("runStudio: %v\nstderr: %s", err, stderr)
	}
	if want := "marshal studio: could not open the Marshal app (open the Marshal app: launchd refused)\n"; stderr != want {
		t.Errorf("stderr = %q, want the warning %q", stderr, want)
	}
	if want := "https://box.example.com"; *url != want {
		t.Errorf("opened %q, want %q after the app failed", *url, want)
	}
	if want := "studio: https://box.example.com\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestStudioPrintsTheWebURL: --print answers the address a script needs, so
// neither the app nor the browser is opened for it.
func TestStudioPrintsTheWebURL(t *testing.T) {
	home := daemonTestEnv(t)
	studioReplaceApp(t, func(context.Context) error {
		t.Error("--print opened the app")
		return errNoStudioApp
	})
	studioReplaceURL(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_url = \"https://box.example.com\"\n")
	fakeDaemonSocket(t, fakeDaemonClient{info: daemon.Info{WebListen: "127.0.0.1:7788"}})

	old := studioOpenURL
	t.Cleanup(func() { studioOpenURL = old })
	studioOpenURL = func(url string) error {
		t.Errorf("--print opened a browser at %q", url)
		return nil
	}

	stdout, stderr, err := runStudioCommand(t, "--print")
	if err != nil {
		t.Fatalf("runStudio --print: %v\nstderr: %s", err, stderr)
	}
	if want := "studio: https://box.example.com\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestStudioWithNeitherSaysSo: with no app and no daemon.web_url there is
// nothing to open, and the command says what would set either up.
func TestStudioWithNeitherSaysSo(t *testing.T) {
	daemonTestEnv(t)
	studioReplaceApp(t, func(context.Context) error { return errNoStudioApp })
	url := studioReplaceURL(t)
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})

	stdout, _, err := runStudioCommand(t)
	if err == nil {
		t.Fatalf("runStudio succeeded with neither an app nor a web Studio: stdout %q", stdout)
	}
	for _, want := range []string{
		"neither the Marshal app nor a web Studio is set up",
		"daemon.web_listen",
		"daemon.web_url",
		"marshal daemon install",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
	if *url != "" {
		t.Errorf("opened the browser at %q, want nothing", *url)
	}
}

// TestStudioWarnsWhenTheWebListenerIsDown: a set-up web Studio whose listener
// is not serving gets one warning line naming why, and the address is still
// opened. A daemon that does not answer adds no line.
func TestStudioWarnsWhenTheWebListenerIsDown(t *testing.T) {
	cases := []struct {
		name   string
		client fakeDaemonClient
		want   string
	}{
		{
			name:   "bind failed",
			client: fakeDaemonClient{info: daemon.Info{WebError: "listen tcp 127.0.0.1:7788: bind: address already in use"}},
			want:   "marshal studio: the daemon's web listener is not listening (listen tcp 127.0.0.1:7788: bind: address already in use)",
		},
		{
			name:   "no listener at all",
			client: fakeDaemonClient{info: daemon.Info{}},
			want:   "marshal studio: the daemon's web listener is not listening (daemon.web_listen is not set)",
		},
		{
			name:   "the daemon does not answer",
			client: fakeDaemonClient{err: errors.New("dial unix: no such file or directory")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := daemonTestEnv(t)
			studioReplaceApp(t, func(context.Context) error { return errNoStudioApp })
			studioReplaceURL(t)
			daemonWriteUserConfig(t, home, "[daemon]\nweb_url = \"https://box.example.com\"\n")
			fakeDaemonSocket(t, tc.client)

			stdout, stderr, err := runStudioCommand(t)
			if err != nil {
				t.Fatalf("runStudio: %v\nstderr: %s", err, stderr)
			}
			if want := "studio: https://box.example.com\n"; stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			switch tc.want {
			case "":
				if stderr != "" {
					t.Errorf("stderr = %q, want no line for a daemon that does not answer", stderr)
				}
			default:
				lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
				if len(lines) != 1 {
					t.Fatalf("stderr = %q, want one warning line", stderr)
				}
				if lines[0] != tc.want {
					t.Errorf("warning = %q, want %q", lines[0], tc.want)
				}
			}
		})
	}
}

// TestStudioRefusesNoOpen: --no-open went with the one-time code it protected,
// so it is an unknown flag rather than a flag that quietly does nothing.
func TestStudioRefusesNoOpen(t *testing.T) {
	daemonTestEnv(t)
	studioReplaceApp(t, func(context.Context) error {
		t.Error("--no-open opened the app")
		return errNoStudioApp
	})

	_, _, err := runStudioCommand(t, "--no-open")
	if err == nil {
		t.Fatal("runStudio accepted --no-open")
	}
	if !strings.Contains(err.Error(), "no-open") {
		t.Errorf("error = %q, want it to name --no-open", err)
	}
}
