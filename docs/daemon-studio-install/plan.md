# Plan: `marshal daemon install --studio`

Spec: [design.md](design.md) (approved).
Base commit: `7e44d9d7` (2026-11-25), branch `main`.
Executed inline, one commit per task; Task 4 (documentation only) landed first
under the parallel-safe allowance below, then Tasks 1–3 in order.

Coordination: Tasks 1–3 are strictly sequential — they share
`cmd/marshal/daemon.go` and `cmd/marshal/daemon_test.go`, and each consumes what
the previous one added. Task 4 touches only documentation, so it is
**Parallel-safe with: Task 1** (disjoint files, and its text is already fixed by
this plan).

## Goal

`marshal daemon install --studio` sets `daemon.web_listen = "unix"` when no
listener is configured, and `--studio-url URL` sets `daemon.web_url`; both
validate before writing, print the listener and address the run ends with plus
the matching proxy snippet, and warn when the daemon has no Studio files. A
plain `marshal daemon install` behaves exactly as it does today.

## Non-goals

- No default port or default `web_listen`.
- No `--studio-listen`, loopback convenience form, or port picking.
- No proxy install, config, TLS, tokens, or browser authentication: D10/D13/D14
  stand unchanged.
- No change to `marshal studio`, `marshal daemon status`, `web/bridge`, or
  webbridge's flags.
- No remote/SSH form.

## Assumptions

- The command runs on the machine that holds the daemon; it writes that
  machine's user config and installs that machine's unit.
- `web_group` is left as the user set it, and `daemonWebListen` decides whether
  the pair is legal.
- The tests drive the darwin fake manager (`fakeDaemonManager`), so the unit
  under test is the launchd plist at
  `filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist")`.

## Contracts

New in `cmd/marshal/daemon.go`:

```go
// daemonStudioConfig is what --studio and --studio-url ask the [daemon]
// section to hold, and what it held before them.
type daemonStudioConfig struct {
	passed    bool          // --studio or --studio-url was passed
	cfg       config.Config // the merged user config carrying the intended values
	listen    string        // daemon.web_listen after this run
	listenWas string        // daemon.web_listen before it
	url       string        // daemon.web_url after this run
	urlWas    string        // daemon.web_url before it
}

// changed reports whether this run alters either value.
func (s daemonStudioConfig) changed() bool

// daemonPlanStudio computes the pair the flags ask for and validates it,
// without writing anything.
func daemonPlanStudio(home string, studio bool, studioURL string) (daemonStudioConfig, error)

// daemonWriteStudio writes s into the user config when it differs from what
// the file already holds.
func daemonWriteStudio(home string, s daemonStudioConfig) error

// daemonStudioLines prints the block install shows when a flag was passed.
func daemonStudioLines(home string, s daemonStudioConfig, stdout io.Writer)

// daemonWebSocketPath is where daemon.web_listen = "unix" puts the socket:
// web.sock beside the daemon's own.
func daemonWebSocketPath(home string) string

// daemonProxyTarget is what a proxy dials for the listener this run ends with.
func daemonProxyTarget(home, listen string) string

// daemonListenPort is the port of a loopback host:port, or "".
func daemonListenPort(addr string) string
```

New test helpers in `cmd/marshal/daemon_test.go`:

```go
func daemonInstallRun(t *testing.T, args ...string) (string, string, error)
func daemonInstallRunWebbridge(t *testing.T, webbridge string, args ...string) (string, string, error)
func daemonReadUserConfig(t *testing.T, home string) config.Config
func daemonTestWebbridgeWithStudio(t *testing.T) (webbridge, studio string) // Task 3
```

Reused, already present:

| Symbol | Where | Why it matters |
| --- | --- | --- |
| `runDaemonInstall(ctx, args, stdout, stderr)` | `cmd/marshal/daemon.go` | the command being extended |
| `daemonWebListen(listen, group)` | `cmd/marshal/daemon.go` | validates the listener and its group (D3) |
| `daemonPublicURL(key, setting)` | `cmd/marshal/daemon.go` | validates `web_url` as an http(s) origin |
| `daemonUnitSpec(webbridge, home)` | `cmd/marshal/daemon.go` | re-reads the user config; runs after the write |
| `daemonStudioLine(info daemon.Info)` | `cmd/marshal/daemon.go` | supplies the D12 wording verbatim |
| `config.SaveUserConfigSection(path, cfg, baseline...)` | `internal/app/config/save.go` | the user-config writer every `[daemon]` edit uses |
| `config.UserConfigPath(home)` | `internal/app/config/locations.go` | the path that writer takes |
| `bridge.FindStudioDir(exe) (dir string, looked []string)` | `web/bridge/assets.go` | the resolver webbridge runs (D12) |
| `daemon.PathsFor(home).Dir` | `internal/daemon/daemon.go` | where `web.sock` sits |
| `daemonTestEnv`, `daemonWriteUserConfig`, `daemonTestWebbridge`, `fakeDaemonManager`, `fakeDaemonSocket`, `fakeDaemonClient` | `cmd/marshal/daemon_test.go` | the install test harness |

---

## Task 1: The two flags, validated, and written to the user config

**Goal:** `--studio` writes `web_listen = "unix"` when none is set and
`--studio-url` writes `web_url`; a bad pair is refused before anything is
written, and the rendered unit carries whichever values this run ends with.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:**

1. In `cmd/marshal/daemon_test.go`, add `"marshal/internal/app/config"` to the
   import block, after `"marshal/internal/app"` (it is not imported today).

2. In `cmd/marshal/daemon.go`, extend the usage comment of
   `runDaemonInstall` from
   `// Usage: marshal daemon install [--webbridge PATH] [--no-linger]` to
   `// Usage: marshal daemon install [--webbridge PATH] [--no-linger] [--studio] [--studio-url URL]`.

3. In `runDaemonInstall`, add the two flags after `noLinger`:

```go
	studio := fs.Bool("studio", false, "set daemon.web_listen to \"unix\" when it is unset")
	studioURL := fs.String("studio-url", "", "set daemon.web_url to the address your proxy publishes")
```

4. In `runDaemonInstall`, between `daemonHome()` and `daemonManagerFor(home)`,
   insert the plan-then-write pair, so the config is settled before the unit is
   rendered:

```go
	studioSettings, err := daemonPlanStudio(home, *studio, *studioURL)
	if err != nil {
		return err
	}
	if err := daemonWriteStudio(home, studioSettings); err != nil {
		return err
	}
```

5. Add the helpers below to `cmd/marshal/daemon.go`, immediately after
   `runDaemonInstall` and before `daemonEnsureLinger`.

```go
// daemonStudioConfig is what --studio and --studio-url ask the [daemon]
// section to hold, and what it held before them.
type daemonStudioConfig struct {
	passed    bool          // --studio or --studio-url was passed
	cfg       config.Config // the merged user config carrying the intended values
	listen    string        // daemon.web_listen after this run
	listenWas string        // daemon.web_listen before it
	url       string        // daemon.web_url after this run
	urlWas    string        // daemon.web_url before it
}

// changed reports whether this run alters either value.
func (s daemonStudioConfig) changed() bool {
	return s.listen != s.listenWas || s.url != s.urlWas
}

// daemonPlanStudio computes the [daemon] pair the two Studio flags ask for and
// validates it without writing anything (D3, D10, D13). Validation comes
// first so a refused pair cannot leave half a configuration behind.
func daemonPlanStudio(home string, studio bool, studioURL string) (daemonStudioConfig, error) {
	out := daemonStudioConfig{passed: studio || strings.TrimSpace(studioURL) != ""}
	if !out.passed {
		return out, nil
	}
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		return out, fmt.Errorf("read the user config: %w", err)
	}
	out.listenWas = strings.TrimSpace(cfg.Daemon.WebListen)
	out.listen = out.listenWas
	out.urlWas = strings.TrimSpace(cfg.Daemon.WebURL)
	out.url = out.urlWas
	if url := strings.TrimSpace(studioURL); url != "" {
		out.url = url
	}
	if studio && out.listen == "" {
		// D10: "unix" is the listener form that needs no D13 warning, since
		// the daemon directory is private (D3).
		out.listen = "unix"
	}
	if out.url != "" && out.listen == "" {
		return out, errors.New("marshal daemon install: --studio-url needs a listener: pass --studio too, or set daemon.web_listen")
	}
	if _, err := daemonWebListen(out.listen, cfg.Daemon.WebGroup); err != nil {
		return out, err
	}
	if _, err := daemonPublicURL("daemon.web_url", out.url); err != nil {
		return out, err
	}
	cfg.Daemon.WebListen = out.listen
	cfg.Daemon.WebURL = out.url
	out.cfg = cfg
	return out, nil
}

// daemonWriteStudio writes the pair daemonPlanStudio answered, when it differs
// from what the file holds. It goes through the user-config writer every other
// [daemon] edit uses, so unrelated sections survive the write (D10).
func daemonWriteStudio(home string, s daemonStudioConfig) error {
	if !s.changed() {
		return nil
	}
	path := config.UserConfigPath(home)
	if err := config.SaveUserConfigSection(path, s.cfg); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
```

6. Add the install test helpers to `cmd/marshal/daemon_test.go`, immediately
   after `daemonInstallUnit`:

```go
// daemonInstallRun runs `marshal daemon install` with args against the fake
// service manager and answers what it printed, what it wrote to stderr, and its
// error.
func daemonInstallRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return daemonInstallRunWebbridge(t, daemonTestWebbridge(t), args...)
}

// daemonInstallRunWebbridge is daemonInstallRun over a named webbridge, for the
// tests that need the Studio's files beside it.
func daemonInstallRunWebbridge(t *testing.T, webbridge string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	// install waits, briefly, for the daemon to answer; nothing is listening
	// here, which is the "not answering yet" path.
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})
	var out, errOut bytes.Buffer
	err := runDaemon(context.Background(),
		append([]string{"install", "--webbridge", webbridge}, args...),
		strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

// daemonReadUserConfig is the user config as the next load reads it.
func daemonReadUserConfig(t *testing.T, home string) config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		t.Fatalf("read the user config: %v", err)
	}
	return cfg
}
```

7. Add these seven tests to `cmd/marshal/daemon_test.go`, after
   `TestDaemonInstallWritesTheWebListener`:

```go
// TestDaemonInstallStudioSetsTheListener: --studio writes daemon.web_listen =
// "unix" — the form that needs no D13 warning — and the unit the daemon runs
// carries it (D3, D10).
func TestDaemonInstallStudioSetsTheListener(t *testing.T) {
	home := daemonTestEnv(t)
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	if got := cfg.Daemon.WebListen; got != "unix" {
		t.Errorf("daemon.web_listen = %q, want unix", got)
	}
	if got := cfg.Daemon.WebURL; got != "" {
		t.Errorf("daemon.web_url = %q, want it left unset", got)
	}
	unit, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	if !strings.Contains(string(unit), "<string>--web-listen</string>\n\t\t<string>unix</string>") {
		t.Errorf("the unit does not carry --web-listen unix:\n%s", unit)
	}
}

// TestDaemonInstallStudioKeepsAnExistingListener: an install restarts the unit,
// so --studio must not move a listener the operator already points a proxy at.
func TestDaemonInstallStudioKeepsAnExistingListener(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_listen = \"127.0.0.1:7700\"\n")
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if got := daemonReadUserConfig(t, home).Daemon.WebListen; got != "127.0.0.1:7700" {
		t.Errorf("daemon.web_listen = %q, want the existing 127.0.0.1:7700", got)
	}
}

// TestDaemonInstallStudioURLNeedsAListener: an address with nothing listening is
// a meaningless pair, refused before anything is written.
func TestDaemonInstallStudioURLNeedsAListener(t *testing.T) {
	home := daemonTestEnv(t)
	_, _, err := daemonInstallRun(t, "--studio-url", "https://marshal.example.com")
	if err == nil {
		t.Fatal("install --studio-url with no listener did not fail")
	}
	if !strings.Contains(err.Error(), "--studio-url needs a listener") {
		t.Errorf("error = %v, want it to name the missing listener", err)
	}
	cfg := daemonReadUserConfig(t, home)
	if cfg.Daemon.WebListen != "" || cfg.Daemon.WebURL != "" {
		t.Errorf("a refused --studio-url wrote web_listen = %q, web_url = %q",
			cfg.Daemon.WebListen, cfg.Daemon.WebURL)
	}
}

// TestDaemonInstallStudioRefusesAGroupWithUnix: web_group needs a socket path of
// its own (D3), so --studio names that rather than writing a pair the daemon
// would refuse at start.
func TestDaemonInstallStudioRefusesAGroupWithUnix(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_group = \"marshal\"\n")
	_, _, err := daemonInstallRun(t, "--studio")
	if err == nil {
		t.Fatal("install --studio with daemon.web_group did not fail")
	}
	if !strings.Contains(err.Error(), "daemon.web_group") {
		t.Errorf("error = %v, want it to name daemon.web_group", err)
	}
	if got := daemonReadUserConfig(t, home).Daemon.WebListen; got != "" {
		t.Errorf("a refused --studio wrote web_listen = %q", got)
	}
}

// TestDaemonInstallStudioURLRefusesANonHTTPAddress: daemonPublicURL's rule is
// the only one this command applies, and a value it refuses is never written.
func TestDaemonInstallStudioURLRefusesANonHTTPAddress(t *testing.T) {
	home := daemonTestEnv(t)
	_, _, err := daemonInstallRun(t, "--studio", "--studio-url", "ftp://marshal.example.com")
	if err == nil {
		t.Fatal("install --studio-url ftp:// did not fail")
	}
	if !strings.Contains(err.Error(), "daemon.web_url") {
		t.Errorf("error = %v, want it to name daemon.web_url", err)
	}
	cfg := daemonReadUserConfig(t, home)
	if cfg.Daemon.WebListen != "" || cfg.Daemon.WebURL != "" {
		t.Errorf("a refused pair was written: web_listen = %q, web_url = %q",
			cfg.Daemon.WebListen, cfg.Daemon.WebURL)
	}
}

// TestDaemonInstallStudioURLWritesTheAddress: the address is a second run, which
// is why --studio-url exists apart from --studio, and the unit carries it as
// --public-url.
func TestDaemonInstallStudioURLWritesTheAddress(t *testing.T) {
	home := daemonTestEnv(t)
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if _, stderr, err := daemonInstallRun(t, "--studio-url", "https://marshal.example.com"); err != nil {
		t.Fatalf("install --studio-url: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	if got := cfg.Daemon.WebListen; got != "unix" {
		t.Errorf("daemon.web_listen = %q, want unix", got)
	}
	if got := cfg.Daemon.WebURL; got != "https://marshal.example.com" {
		t.Errorf("daemon.web_url = %q, want the address", got)
	}
	unit, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	if !strings.Contains(string(unit), "<string>--public-url</string>\n\t\t<string>https://marshal.example.com</string>") {
		t.Errorf("the unit does not carry --public-url:\n%s", unit)
	}
}

// TestDaemonInstallStudioKeepsUnrelatedSections: the write goes through the
// user-config writer, so a provider table set under it survives.
func TestDaemonInstallStudioKeepsUnrelatedSections(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[providers.local]\nbase_url = \"http://127.0.0.1:11434/v1\"\n")
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	p, ok := cfg.Providers["local"]
	if !ok {
		t.Fatalf("[providers.local] is gone: %+v", cfg.Providers)
	}
	if p.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("base_url = %q, want it kept", p.BaseURL)
	}
}
```

**Verify:**

```bash
CGO_ENABLED=1 go test ./cmd/marshal/ -run 'TestDaemonInstallStudio' -count=1
CGO_ENABLED=1 go build ./cmd/marshal
```

All seven tests pass. `daemonStudioLines` is declared but not yet called, which
Go accepts: an unused function is not an error.

**Commit:** `studio install: task 1 — the flags, validated before they are written`

---

## Task 2: The block install prints

**Goal:** when either flag is passed, install prints the listener and address the
run ends with and a proxy snippet that matches that listener; a plain install
prints nothing new.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:**

1. In `runDaemonInstall`, immediately after
   `fmt.Fprintf(stdout, "socket: %s\n", spec.Paths.Socket)`, add:

```go
	daemonStudioLines(home, studioSettings, stdout)
```

2. Add the helpers below to `cmd/marshal/daemon.go`, after `daemonWriteStudio`:

```go
// daemonStudioLines is what install prints when a Studio flag was passed: the
// listener and address the run ends with, and a proxy snippet that matches that
// listener (D10, D13). A plain install prints none of it.
func daemonStudioLines(home string, s daemonStudioConfig, stdout io.Writer) {
	if !s.passed {
		return
	}
	listen := s.listen
	if listen == "unix" {
		listen = fmt.Sprintf("unix (%s)", daemonWebSocketPath(home))
	}
	suffix := ""
	if s.listenWas != "" && s.listen == s.listenWas {
		suffix = " (unchanged)"
	}
	fmt.Fprintf(stdout, "web_listen: %s%s\n", listen, suffix)

	if s.url == "" {
		fmt.Fprintln(stdout, "web_url: unset")
		fmt.Fprintf(stdout, "proxy: point your proxy at %s, then run\n", daemonProxyTarget(home, s.listen))
		fmt.Fprintln(stdout, "       marshal daemon install --studio-url https://marshal.example.com")
		return
	}
	if s.urlWas != "" && s.url != s.urlWas {
		fmt.Fprintf(stdout, "web_url: %s (was %s)\n", s.url, s.urlWas)
	} else {
		fmt.Fprintf(stdout, "web_url: %s\n", s.url)
	}
	switch {
	case s.listen == "unix":
		// D10/D13: the socket form is what this run wrote, so it is the
		// snippet; the document's port-shaped example is the alternative for a
		// proxy that cannot dial a socket.
		fmt.Fprintln(stdout, "proxy: Caddy, with whatever auth module you pick:")
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "\tmarshal.example.com {\n\t\treverse_proxy unix/%s\n\t}\n", daemonWebSocketPath(home))
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "a proxy that cannot dial a unix socket uses the loopback pair instead:")
		fmt.Fprintln(stdout, "set web_listen = \"127.0.0.1:7700\" and web_url = \"https://box.<tailnet>.ts.net\",")
		fmt.Fprintln(stdout, "then `tailscale serve --bg 7700` (docs/DEPLOYMENT.md)")
	case strings.HasPrefix(s.listen, "/"):
		fmt.Fprintf(stdout, "proxy: Caddy:  reverse_proxy unix/%s\n", s.listen)
	default:
		fmt.Fprintf(stdout, "proxy: Caddy:  reverse_proxy %s\n", s.listen)
		if port := daemonListenPort(s.listen); port != "" {
			fmt.Fprintf(stdout, "       Tailscale Serve:  tailscale serve --bg %s\n", port)
		}
		fmt.Fprintln(stdout, "       (docs/DEPLOYMENT.md)")
	}
	fmt.Fprintln(stdout, "/hooks/, /status/ and /mcp pass through without the proxy's login: each carries")
	fmt.Fprintln(stdout, "its own credential")
}

// daemonWebSocketPath is where daemon.web_listen = "unix" puts the web socket:
// web.sock beside the daemon's own socket.
func daemonWebSocketPath(home string) string {
	return filepath.Join(daemon.PathsFor(home).Dir, "web.sock")
}

// daemonProxyTarget is what a proxy in front of the daemon dials for the
// listener this run ends with: the socket's path for "unix", else the address.
func daemonProxyTarget(home, listen string) string {
	if listen == "unix" {
		return daemonWebSocketPath(home)
	}
	return listen
}

// daemonListenPort is the port of a loopback host:port, or "" when the address
// names none.
func daemonListenPort(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}
```

3. Add these five tests to `cmd/marshal/daemon_test.go`, after the Task 1 tests:

```go
// TestDaemonInstallWithoutStudioFlagsSaysNothingAboutIt: a plain install keeps
// its output, even on a daemon someone configured by hand, so the promise in
// the usage text holds.
func TestDaemonInstallWithoutStudioFlagsSaysNothingAboutIt(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\n"+
		"web_listen = \"unix\"\n"+
		"web_url = \"https://marshal.example.com\"\n")
	stdout, stderr, err := daemonInstallRun(t)
	if err != nil {
		t.Fatalf("install: %v\nstderr: %s", err, stderr)
	}
	for _, banned := range []string{"web_listen:", "web_url:", "proxy:", "studio:"} {
		if strings.Contains(stdout, banned) {
			t.Errorf("a plain install printed %q:\n%s", banned, stdout)
		}
	}
}

// TestDaemonInstallStudioPrintsTheSocketAndTheNextStep: with no address yet the
// block names the socket and the run that supplies one.
func TestDaemonInstallStudioPrintsTheSocketAndTheNextStep(t *testing.T) {
	home := daemonTestEnv(t)
	stdout, stderr, err := daemonInstallRun(t, "--studio")
	if err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	socket := filepath.Join(daemon.PathsFor(home).Dir, "web.sock")
	for _, want := range []string{
		"web_listen: unix (" + socket + ")",
		"web_url: unset",
		"point your proxy at " + socket,
		"marshal daemon install --studio-url https://marshal.example.com",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install --studio printed no %q:\n%s", want, stdout)
		}
	}
}

// TestDaemonInstallStudioPrintsTheProxySnippetForTheListenerItWrote: with unix
// written, the socket form is the snippet and the document's port-shaped
// Tailscale example appears only as the stated alternative (docs/DEPLOYMENT.md).
func TestDaemonInstallStudioPrintsTheProxySnippetForTheListenerItWrote(t *testing.T) {
	home := daemonTestEnv(t)
	stdout, stderr, err := daemonInstallRun(t, "--studio", "--studio-url", "https://marshal.example.com")
	if err != nil {
		t.Fatalf("install --studio --studio-url: %v\nstderr: %s", err, stderr)
	}
	socket := filepath.Join(daemon.PathsFor(home).Dir, "web.sock")
	for _, want := range []string{
		"web_url: https://marshal.example.com",
		"reverse_proxy unix/" + socket,
		"tailscale serve --bg 7700",
		"/hooks/, /status/ and /mcp pass through",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install printed no %q:\n%s", want, stdout)
		}
	}
}

// TestDaemonInstallStudioPrintsAnExistingLoopbackListener: a loopback listener
// keeps its port-shaped snippet and gets no socket block.
func TestDaemonInstallStudioPrintsAnExistingLoopbackListener(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_listen = \"127.0.0.1:7700\"\n")
	stdout, stderr, err := daemonInstallRun(t, "--studio", "--studio-url", "https://box.example.ts.net")
	if err != nil {
		t.Fatalf("install: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"web_listen: 127.0.0.1:7700 (unchanged)",
		"web_url: https://box.example.ts.net",
		"reverse_proxy 127.0.0.1:7700",
		"tailscale serve --bg 7700",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install printed no %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "reverse_proxy unix/") {
		t.Errorf("a loopback listener printed the socket snippet:\n%s", stdout)
	}
}

// TestDaemonInstallStudioURLReportsTheAddressItReplaced: changing the address
// says what it was, so a wrong --studio-url is visible.
func TestDaemonInstallStudioURLReportsTheAddressItReplaced(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\n"+
		"web_listen = \"unix\"\n"+
		"web_url = \"https://old.example.com\"\n")
	stdout, stderr, err := daemonInstallRun(t, "--studio-url", "https://new.example.com")
	if err != nil {
		t.Fatalf("install --studio-url: %v\nstderr: %s", err, stderr)
	}
	if want := "web_url: https://new.example.com (was https://old.example.com)"; !strings.Contains(stdout, want) {
		t.Errorf("install printed no %q:\n%s", want, stdout)
	}
}
```

**Verify:**

```bash
CGO_ENABLED=1 go test ./cmd/marshal/ -run 'TestDaemonInstall(Studio|Without)' -count=1
```

All twelve install tests pass (seven from Task 1, five from Task 2 — the Task 1
tests still pass because the added block only appends lines).

**Commit:** `studio install: task 2 — the block install prints, matching the listener it wrote`

---

## Task 3: Name the Studio the daemon will serve (D12)

**Goal:** a run with a flag says which directory the daemon serves the Studio
from, or that it found none — the same wording `daemon status` uses.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:**

1. In `runDaemonInstall`, after the `daemonStudioLines(home, studioSettings, stdout)`
   call added in Task 2, add:

```go
	// D12: a daemon with no Studio files serves a page that says so and runs
	// on. That is worth naming here only because this run turned the listener
	// on; daemonStudioLine supplies the wording status uses.
	if studioSettings.passed {
		dir, _ := bridge.FindStudioDir(spec.Webbridge)
		fmt.Fprintln(stdout, daemonStudioLine(daemon.Info{StudioDir: dir}))
	}
```

2. Add the helper and two tests to `cmd/marshal/daemon_test.go`, after
   `daemonTestWebbridge` and after the Task 2 tests respectively:

```go
// daemonTestWebbridgeWithStudio is a webbridge with the Studio's files beside
// it, as a release install has them.
func daemonTestWebbridgeWithStudio(t *testing.T) (webbridge, studio string) {
	t.Helper()
	dir := t.TempDir()
	webbridge = filepath.Join(dir, "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	studio = filepath.Join(dir, "share", "marshal", "studio")
	if err := os.MkdirAll(studio, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(studio, "index.html"), []byte("<!doctype html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return webbridge, studio
}

// TestDaemonInstallStudioNamesMissingStudioFiles: --studio on a box whose
// webbridge has no Studio files says so, because the listener would serve the
// no-files page (D12).
func TestDaemonInstallStudioNamesMissingStudioFiles(t *testing.T) {
	daemonTestEnv(t)
	stdout, stderr, err := daemonInstallRun(t, "--studio")
	if err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if want := "studio: no files found"; !strings.Contains(stdout, want) {
		t.Errorf("install printed no %q:\n%s", want, stdout)
	}
}

// TestDaemonInstallStudioNamesTheStudioDir: with files beside the webbridge, the
// block names a directory instead of reporting none.
func TestDaemonInstallStudioNamesTheStudioDir(t *testing.T) {
	daemonTestEnv(t)
	webbridge, _ := daemonTestWebbridgeWithStudio(t)
	stdout, stderr, err := daemonInstallRunWebbridge(t, webbridge, "--studio")
	if err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stdout, "no files found") {
		t.Errorf("install found no Studio files beside a webbridge that has them:\n%s", stdout)
	}
	// The path is the resolved one, so compare the shape, not the temp path.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "studio: ") && strings.HasSuffix(line, filepath.Join("share", "marshal", "studio")) {
			return
		}
	}
	t.Errorf("install named no Studio directory:\n%s", stdout)
}
```

**Verify:**

```bash
CGO_ENABLED=1 go test ./cmd/marshal/ -run 'TestDaemonInstallStudio' -count=1
```

All install tests pass, including Task 2's
`TestDaemonInstallWithoutStudioFlagsSaysNothingAboutIt`, which still bans
`studio:` from a plain install.

**Commit:** `studio install: task 3 — name the Studio the daemon will serve, or that it found none`

---

## Task 4: Documentation

**Goal:** a reader of the deployment guide and the changelog finds the one-command
path, and the spec says it is implemented.

**Files:** `docs/DEPLOYMENT.md`, `CHANGELOG.md`, `docs/daemon-studio-install/design.md`.

**Steps:**

1. In `docs/DEPLOYMENT.md`, insert this paragraph as the **first** paragraph of
   the `## A browser` section: immediately after the heading line
   `## A browser`, and before the line that begins
   `The daemon serves the Studio and its API,`. It goes there rather than before
   `In \`~/.config/marshal/config.toml\`:` because that sentence is a
   continuation of the paragraph above it, and splitting it would leave a
   dangling clause.

```markdown
`marshal daemon install --studio` sets `web_listen = "unix"` for you, when it is
not already set, and prints the proxy line for the socket it wrote.
`marshal daemon install --studio-url https://marshal.example.com` sets `web_url`
once your proxy has an address, and running it again changes it. A plain
`marshal daemon install` says nothing about the Studio and leaves both keys
alone, so hand-edited values stay in charge.
```

2. In `CHANGELOG.md`, under `## [Unreleased]` → `### Changed` → `**One daemon**`,
   add as a new bullet immediately after the bullet that begins
   `- Every machine runs the same daemon:` (the bullet ends `…so your unit is
   rewritten.`):

```markdown
- `marshal daemon install --studio` turns the web Studio on: it writes
  `daemon.web_listen = "unix"` when no listener is set, prints the proxy line
  for that socket, and warns when the daemon has no Studio files.
  `--studio-url` sets `daemon.web_url` once the proxy has an address.
```

3. In `docs/daemon-studio-install/design.md`, change the status line from
   `Status: approved design, awaiting plan.` to `Status: implemented.`

**Verify:**

```bash
grep -c "daemon install --studio" docs/DEPLOYMENT.md CHANGELOG.md
grep -n "^Status: implemented." docs/daemon-studio-install/design.md
```

The first prints a non-zero count for each file; the second prints one line.

**Commit:** `studio install: task 4 — document the one-command path`

Note: `AGENTS.md` says specs are committed with the work they describe, so
`docs/daemon-studio-install/design.md` and this plan were committed ahead of
Task 1 (`4b29fd45`): an isolated task starts from committed HEAD, so a spec left
uncommitted would not exist in its worktree.

---

## Final verification

```bash
CGO_ENABLED=1 go build ./cmd/marshal
CGO_ENABLED=1 go vet ./cmd/marshal/
gofmt -l cmd/marshal          # prints nothing
CGO_ENABLED=1 go test ./cmd/marshal/ -count=1
scripts/test.sh quick         # whole suite, -short, via the repo's entry point
```

All must pass. Then a manual sanity check on a disposable `HOME`:

```bash
export HOME=$(mktemp -d) MARSHAL_CONFIG_DIR= MARSHAL_DATA_DIR=
./marshal daemon status            # says the listener is not set
./marshal daemon install --studio  # writes web_listen = "unix", prints the socket and next step
sed -n '/\[daemon\]/,/^\[/p' "$HOME/.config/marshal/config.toml"
```

## Integration notes

- `config.SaveUserConfigSection` writes the whole `[daemon]` section through
  `writeSections`, so the file gains every field at its current value (for
  example `idle_session_timeout = "30m0s"`) alongside `web_listen`. That is how
  every existing `[daemon]` write behaves (`TestDaemonWebSettingsRoundTripThroughASave`),
  so the tests assert on the reloaded values rather than the file's text.
- The write happens before `daemonUnitSpec`, which is what makes the rendered
  unit carry the new `--web-listen` and `--public-url`; the daemon is restarted
  by `m.Install` on the same run.
- A refused pair writes nothing and returns before `daemonManagerFor`, so no
  unit is rendered and no service command runs.
- `--studio` never touches `web_group`; a group that contradicts `unix` is
  refused by `daemonWebListen` with its existing D3 message.
- Nothing here changes `marshal studio` or `daemon status`: they keep reading
  `web_listen` and `web_url` from the same config the write settles.
