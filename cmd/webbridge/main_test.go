package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"marshal/internal/buildinfo"
	"marshal/web/bridge"
)

// TestMain lets this test binary stand in for webbridge: the restart test
// starts a copy of this executable and expects a daemon, not a test run. The
// variable is how the copy knows which it is.
func TestMain(m *testing.M) {
	if os.Getenv(testAsBinaryEnv) == "1" {
		// The copy's arguments are webbridge's, so main serves until it is
		// asked to stop or re-execute itself.
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestParseConfigDefaults(t *testing.T) {
	// Ensure env vars do not leak host state into the defaults test.
	isolateWebbridgeEnv(t)
	data := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", data)

	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	dir := filepath.Join(data, "daemon")
	if want := filepath.Join(dir, "marshal.sock"); cfg.socket != want {
		t.Errorf("socket = %q, want %q", cfg.socket, want)
	}
	if want := filepath.Join(dir, "state"); cfg.stateDir != want {
		t.Errorf("stateDir = %q, want %q", cfg.stateDir, want)
	}
	if want := filepath.Join(dir, "fleet.json"); cfg.workspace != want {
		t.Errorf("workspace = %q, want %q", cfg.workspace, want)
	}
	if cfg.egress != "off" {
		t.Errorf("egress = %q, want off", cfg.egress)
	}
	if cfg.stateVolume != "" {
		t.Errorf("stateVolume = %q, want empty: the daemon keeps its state on the host", cfg.stateVolume)
	}
	if cfg.cwdRoot != "" {
		t.Errorf("cwdRoot = %q, want empty: the daemon registers no working-directory project", cfg.cwdRoot)
	}
	if cfg.webListen != "" {
		t.Errorf("webListen = %q, want empty: no web listener by default", cfg.webListen)
	}
	if cfg.webGroup != "" {
		t.Errorf("webGroup = %q, want empty", cfg.webGroup)
	}
	if cfg.marshalBin != "marshal" {
		t.Errorf("marshalBin = %q, want marshal", cfg.marshalBin)
	}
}

func TestParseConfigEnvAndFlags(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("WEBBRIDGE_WEB_LISTEN", "127.0.0.1:9999")
	t.Setenv("WEBBRIDGE_CWD_ROOT", "/env/root")

	// Env applies when flags are absent.
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.webListen != "127.0.0.1:9999" || cfg.cwdRoot != "/env/root" {
		t.Errorf("env not applied: %+v", cfg)
	}

	// Flags win over env.
	cfg, err = parseConfig([]string{
		"--web-listen", "127.0.0.1:1234",
		"--marshal-bin", "/bin/custom",
		"--cwd-root", "/flag/root",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.webListen != "127.0.0.1:1234" || cfg.marshalBin != "/bin/custom" || cfg.cwdRoot != "/flag/root" {
		t.Errorf("cfg = %+v", cfg)
	}
}

// TestParseConfigReadsTheWebListener: --web-listen, --web-group and
// --preview-url reach the config, by flag and by variable; the listener's own
// syntax is the bridge's to check (D3).
func TestParseConfigReadsTheWebListener(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())

	cfg, err := parseConfig([]string{
		"--web-listen", "unix",
		"--preview-url", "https://previews.example",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.webListen != "unix" {
		t.Errorf("webListen = %q, want unix", cfg.webListen)
	}
	if cfg.previewURL != "https://previews.example" {
		t.Errorf("previewURL = %q, want https://previews.example", cfg.previewURL)
	}

	cfg, err = parseConfig([]string{"--web-listen", "/tmp/web.sock", "--web-group", "wheel"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig with a socket path and a group: %v", err)
	}
	if cfg.webListen != "/tmp/web.sock" || cfg.webGroup != "wheel" {
		t.Errorf("got webListen %q, webGroup %q", cfg.webListen, cfg.webGroup)
	}

	t.Setenv("WEBBRIDGE_WEB_LISTEN", "127.0.0.1:7800")
	t.Setenv("WEBBRIDGE_PREVIEW_URL", "http://previews.example:8080")
	cfg, err = parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig from the variables: %v", err)
	}
	if cfg.webListen != "127.0.0.1:7800" || cfg.previewURL != "http://previews.example:8080" {
		t.Errorf("variables not applied: %+v", cfg)
	}
}

// studioIndexBody is what writeStudioIndex puts in a Studio's index.html: a
// marker the tests look for in what GET / answers.
const studioIndexBody = "<!doctype html><title>Marshal Studio</title>\n"

// writeStudioIndex makes dir a Studio directory — one holding the index.html
// the daemon serves at / — and answers it.
func writeStudioIndex(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(studioIndexBody), 0o644); err != nil {
		t.Fatalf("write the Studio's index.html: %v", err)
	}
	return dir
}

// TestParseConfigReadsTheStudioDir: --studio-dir and WEBBRIDGE_STUDIO_DIR
// name the Studio's files, and a directory that holds no index.html is
// refused, naming the path it was given (D12).
func TestParseConfigReadsTheStudioDir(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	studio := writeStudioIndex(t, t.TempDir())

	cfg, err := parseConfig([]string{"--studio-dir", studio}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig --studio-dir: %v", err)
	}
	if cfg.studioDir != studio {
		t.Errorf("studioDir = %q, want %q", cfg.studioDir, studio)
	}

	t.Setenv("WEBBRIDGE_STUDIO_DIR", studio)
	cfg, err = parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig WEBBRIDGE_STUDIO_DIR: %v", err)
	}
	if cfg.studioDir != studio {
		t.Errorf("studioDir = %q from the variable, want %q", cfg.studioDir, studio)
	}

	// A directory with no index.html is not a Studio: it is refused now,
	// naming the path, rather than serving a 503 over later.
	empty := t.TempDir()
	if _, err := parseConfig([]string{"--studio-dir", empty}, io.Discard); err == nil {
		t.Fatalf("--studio-dir %s accepted with no index.html", empty)
	} else if !strings.Contains(err.Error(), empty) || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("error %q does not name %s and the index.html it lacks", err, empty)
	}

	// Nor is one whose index.html is not a regular file: a directory of that
	// name stats fine, and would be served as the Studio's front page.
	notFile := t.TempDir()
	if err := os.Mkdir(filepath.Join(notFile, "index.html"), 0o755); err != nil {
		t.Fatalf("make index.html a directory: %v", err)
	}
	if _, err := parseConfig([]string{"--studio-dir", notFile}, io.Discard); err == nil {
		t.Fatalf("--studio-dir %s accepted with a directory called index.html", notFile)
	} else if !strings.Contains(err.Error(), notFile) || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("error %q does not name %s and the index.html it refuses", err, notFile)
	}
}

// TestTheTokenAndTLSFlagsAreRefused: the token and TLS flags retired with the
// TCP listener are refused, whether the command line or the variable carried
// them, naming --web-listen and the proxy that goes in front of it (D2, D3).
func TestTheTokenAndTLSFlagsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"token flag", []string{"--token", "s3cret"}, nil},
		{"token variable", nil, map[string]string{"WEBBRIDGE_TOKEN": "s3cret"}},
		{"tls cert flag", []string{"--tls-cert", "/c.pem"}, nil},
		{"tls cert variable", nil, map[string]string{"WEBBRIDGE_TLS_CERT": "/c.pem"}},
		{"tls key flag", []string{"--tls-key", "/k.pem"}, nil},
		{"tls key variable", nil, map[string]string{"WEBBRIDGE_TLS_KEY": "/k.pem"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := parseConfig(tc.args, io.Discard)
			if err == nil {
				t.Fatalf("parseConfig(%v) accepted a retired flag", tc.args)
			}
			if !strings.Contains(err.Error(), "is retired") || !strings.Contains(err.Error(), "--web-listen") {
				t.Errorf("error %q does not retire the flag for --web-listen", err)
			}
		})
	}
}

// TestAPreviewAddrMustBeLoopback: previews are a loopback origin of their
// own, so an address that binds every interface is refused, and a loopback
// one (or "off") is not.
func TestAPreviewAddrMustBeLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:3000", ":3000", "192.168.1.5:3000"} {
		t.Run(addr, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
			_, err := parseConfig([]string{"--preview-addr", addr}, io.Discard)
			if err == nil {
				t.Fatalf("--preview-addr %s accepted; want a loopback refusal", addr)
			}
			if !strings.Contains(err.Error(), "--preview-addr") {
				t.Errorf("error %q does not name --preview-addr", err)
			}
		})
	}
	for _, addr := range []string{"127.0.0.1:3000", "localhost:3000", "[::1]:3000", "off"} {
		t.Run(addr, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
			if _, err := parseConfig([]string{"--preview-addr", addr}, io.Discard); err != nil {
				t.Errorf("--preview-addr %s refused: %v", addr, err)
			}
		})
	}
}

// TestWebGroupNeedsAWebListener: a group without a listener to share has
// nothing to apply to.
func TestWebGroupNeedsAWebListener(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	_, err := parseConfig([]string{"--web-group", "wheel"}, io.Discard)
	if err == nil {
		t.Fatal("--web-group without --web-listen was accepted")
	}
	if !strings.Contains(err.Error(), "--web-group") {
		t.Errorf("error %q does not name --web-group", err)
	}
}

// TestParseConfigReadsTheTunnelsFlag: --tunnels and WEBBRIDGE_TUNNELS turn
// the tunnels helper on, and a variable that does not parse is an error
// naming it.
func TestParseConfigReadsTheTunnelsFlag(t *testing.T) {
	for _, k := range []string{"WEBBRIDGE_TUNNELS", "WEBBRIDGE_ADDR", "WEBBRIDGE_SOCKET"} {
		t.Setenv(k, "")
	}
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.tunnels {
		t.Error("tunnels = true with neither the flag nor the variable; want false")
	}

	cfg, err = parseConfig([]string{"--tunnels"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig --tunnels: %v", err)
	}
	if !cfg.tunnels {
		t.Error("tunnels = false with --tunnels; want true")
	}

	t.Setenv("WEBBRIDGE_TUNNELS", "1")
	cfg, err = parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig WEBBRIDGE_TUNNELS=1: %v", err)
	}
	if !cfg.tunnels {
		t.Error("tunnels = false with WEBBRIDGE_TUNNELS=1; want true")
	}

	t.Setenv("WEBBRIDGE_TUNNELS", "maybe")
	if _, err := parseConfig(nil, io.Discard); err == nil {
		t.Fatal("WEBBRIDGE_TUNNELS=maybe: expected error, got nil")
	} else if !strings.Contains(err.Error(), "WEBBRIDGE_TUNNELS") {
		t.Errorf("error %q does not name WEBBRIDGE_TUNNELS", err)
	}
}

func TestParseConfigProjects(t *testing.T) {
	cfg, err := parseConfig([]string{"--project", "/tmp/project"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.projects) != 1 || cfg.projects[0] != "/tmp/project" {
		t.Fatalf("projects = %#v", cfg.projects)
	}
}

func TestParseProjectMountRejectsMalformed(t *testing.T) {
	if _, err := parseProjectMounts([]string{"no-colon"}); err == nil {
		t.Fatal("accepted a mapping with no colon")
	}
	got, err := parseProjectMounts([]string{"/h:/c"})
	if err != nil || len(got) != 1 || got[0].Host != "/h" || got[0].Container != "/c" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestParseConfigRejectsArgs(t *testing.T) {
	if _, err := parseConfig([]string{"bogus"}, io.Discard); err == nil {
		t.Fatal("positional arg: expected error, got nil")
	}
	if _, err := parseConfig([]string{"--nope"}, io.Discard); err == nil {
		t.Fatal("unknown flag: expected error, got nil")
	}
}

func TestAskpassAnswersUsernameAndPassword(t *testing.T) {
	t.Setenv("MARSHAL_ASKPASS_USER", "x-access-token")
	t.Setenv("MARSHAL_ASKPASS_SECRET", "sk-secret")

	if got := askpassResponse("Username for 'https://github.com': "); got != "x-access-token" {
		t.Errorf("username prompt answered %q", got)
	}
	if got := askpassResponse("Password for 'https://github.com': "); got != "sk-secret" {
		t.Errorf("password prompt answered %q", got)
	}
}

func TestAskpassModeShortCircuitsRun(t *testing.T) {
	t.Setenv("MARSHAL_ASKPASS", "1")
	t.Setenv("MARSHAL_ASKPASS_SECRET", "sk-secret")

	var out strings.Builder
	err := run(context.Background(),
		[]string{"Password for 'https://github.com': "},
		&out, io.Discard)
	if err != nil {
		t.Fatalf("run in askpass mode: %v", err)
	}
	if strings.TrimSpace(out.String()) != "sk-secret" {
		t.Fatalf("askpass printed %q, want the secret", out.String())
	}
}

func TestParseConfigStateVolume(t *testing.T) {
	t.Setenv("WEBBRIDGE_STATE_VOLUME", "")
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stateVolume != "" {
		t.Fatalf("default = %q, want empty: the daemon keeps its state on the host", cfg.stateVolume)
	}
	cfg, err = parseConfig([]string{"--state-volume", "prod-state"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stateVolume != "prod-state" {
		t.Fatalf("got %q", cfg.stateVolume)
	}
}

func TestParseConfigLimitFlags(t *testing.T) {
	cfg, err := parseConfig([]string{"--max-concurrent", "9", "--max-disk-mb", "100", "--max-clone-mb", "50"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConcurrent != 9 || cfg.maxDiskMB != 100 || cfg.maxCloneMB != 50 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestWebbridgeReportsItsVersion(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"--version"},
		&out, io.Discard); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if !strings.Contains(out.String(), "webbridge") {
		t.Fatalf("--version output does not contain 'webbridge': %q", out.String())
	}
}

// TestDaemonVersionResolvesASourceBuild pins task 3's first step: the
// bridge's own version — which GET /api/daemon reports and the fleet carries
// as its buildVersion — is buildinfo.Version(version), so a source build
// names its commit instead of falling back to plain dev (decision 7).
func TestDaemonVersionResolvesASourceBuild(t *testing.T) {
	oldVersion, oldRead := version, buildinfo.ReadBuildInfo
	t.Cleanup(func() { version, buildinfo.ReadBuildInfo = oldVersion, oldRead })

	version = ""
	buildinfo.ReadBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
		}}, true
	}

	if got, want := daemonVersion(), "dev+0123456789ab"; got != want {
		t.Fatalf("daemonVersion() = %q, want %q", got, want)
	}
}

// TestTheBridgesSourceRuleMatchesBuildinfo pins the copy: web/bridge cannot
// import internal/buildinfo (TestWebIsStdlibOnly), so bridge.SourceBuild
// repeats the rule and this package, which may import both, holds them
// together (decision 3).
func TestTheBridgesSourceRuleMatchesBuildinfo(t *testing.T) {
	for _, tc := range []struct {
		version string
		source  bool
	}{
		{"", false},
		{"dev", true},
		{"dev+abc123def456", true},
		{"dev+abc123def456+dirty", true},
		{"v1.2.3", false},
		{"v1.2.3+dirty", false},
		{"devel", false},
		{"devx", false},
	} {
		if got, want := bridge.SourceBuild(tc.version), buildinfo.IsSource(tc.version); got != want {
			t.Errorf("SourceBuild(%q) = %v, buildinfo.IsSource = %v", tc.version, got, want)
		}
		if got := bridge.SourceBuild(tc.version); got != tc.source {
			t.Errorf("SourceBuild(%q) = %v, want %v", tc.version, got, tc.source)
		}
		if got := buildinfo.IsSource(tc.version); got != tc.source {
			t.Errorf("buildinfo.IsSource(%q) = %v, want %v", tc.version, got, tc.source)
		}
	}
}

func TestParseConfigSecretsBackends(t *testing.T) {
	for _, k := range []string{"WEBBRIDGE_SECRETS", "WEBBRIDGE_SECRETS_KEY_FILE", "WEBBRIDGE_BAO_ADDR", "WEBBRIDGE_BAO_MOUNT"} {
		t.Setenv(k, "")
	}
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil || cfg.secrets != "env" {
		t.Fatalf("default: %v, %q", err, cfg.secrets)
	}
	cfg, err = parseConfig([]string{"--secrets", "local", "--secrets-key-file", "/k"}, io.Discard)
	if err != nil || cfg.secrets != "local" || cfg.secretsKeyFile != "/k" {
		t.Fatalf("local: %v, %+v", err, cfg)
	}
	if _, err := parseConfig([]string{"--secrets", "local"}, io.Discard); err == nil {
		t.Error("local without a key file accepted")
	}
	cfg, err = parseConfig([]string{"--secrets", "openbao", "--bao-addr", "https://b:8200",
		"--bao-role-id-file", "/r", "--bao-secret-id-file", "/s", "--bao-ca-file", "/c"}, io.Discard)
	if err != nil || cfg.baoMount != "secret" || cfg.baoAddr != "https://b:8200" || cfg.baoCAFile != "/c" {
		t.Fatalf("openbao: %v, %+v", err, cfg)
	}
	if _, err := parseConfig([]string{"--secrets", "openbao", "--bao-addr", "x"}, io.Discard); err == nil {
		t.Error("openbao without id files accepted")
	}
	if _, err := parseConfig([]string{"--secrets", "vault"}, io.Discard); err == nil {
		t.Error("unknown backend accepted")
	}
	t.Setenv("WEBBRIDGE_SECRETS", "local")
	t.Setenv("WEBBRIDGE_SECRETS_KEY_FILE", "/envkey")
	cfg, err = parseConfig(nil, io.Discard)
	if err != nil || cfg.secrets != "local" || cfg.secretsKeyFile != "/envkey" {
		t.Fatalf("env fallback: %v, %+v", err, cfg)
	}
}

func TestSecretsInitKeySubcommand(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	var out strings.Builder
	if err := run(context.Background(), []string{"secrets", "init-key", key}, &out, io.Discard); err != nil {
		t.Fatalf("init-key: %v", err)
	}
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("key file: %v %v", info, err)
	}
	if err := run(context.Background(), []string{"secrets", "init-key", key}, &out, io.Discard); err == nil {
		t.Error("overwrote existing key")
	}
	if err := run(context.Background(), []string{"secrets", "bogus"}, &out, io.Discard); err == nil {
		t.Error("bad subcommand accepted")
	}
}

func TestBuildSecretProvider(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err := run(context.Background(), []string{"secrets", "init-key", key}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	p, err := buildSecretProvider(config{secrets: "local", secretsKeyFile: key, stateDir: filepath.Join(root, "state")})
	if err != nil || p.Name() != "local" {
		t.Fatalf("local: %v", err)
	}
	if _, err := buildSecretProvider(config{secrets: "local", secretsKeyFile: key, stateDir: root}); err == nil {
		t.Error("key inside state dir accepted")
	}
	p, err = buildSecretProvider(config{secrets: "env"})
	if err != nil || p.Name() != "env" {
		t.Fatalf("env: %v", err)
	}
}

func TestListenPreviewDefaultsToALoopbackPort(t *testing.T) {
	ln, err := listenPreview("")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 || !addr.IP.IsLoopback() {
		t.Fatalf("preview address = %v, want a bound loopback port", ln.Addr())
	}
}

// TestPrintEnvWritesTheMarkerAndTheEnvironment: the subcommand prints the
// marker and then the environment, NUL-terminated, and nothing else, so the
// login shell that runs it can parse the lot (P4 D7).
func TestPrintEnvWritesTheMarkerAndTheEnvironment(t *testing.T) {
	t.Setenv("P6_PRINTED", "yes")

	var out strings.Builder
	handled, err := runSubcommand(context.Background(), []string{"print-env"}, &out)
	if err != nil || !handled {
		t.Fatalf("print-env: handled = %v, err = %v", handled, err)
	}
	got := out.String()
	at := strings.Index(got, bridge.EnvMarker)
	if at < 0 {
		t.Fatalf("output = %q, want the environment marker", got)
	}
	want := "P6_PRINTED=yes\x00"
	if rest := got[at+len(bridge.EnvMarker):]; !strings.Contains(rest, want) {
		t.Fatalf("output after the marker holds no %q", want)
	}
	if !strings.HasSuffix(got, "\x00") {
		t.Fatalf("output does not end with a NUL: %q", got)
	}

	if handled, err := runSubcommand(context.Background(), []string{"print-env", "extra"}, io.Discard); !handled || err == nil {
		t.Fatalf("print-env extra: handled = %v, err = %v, want a refusal", handled, err)
	}
}

// TestTheLocalResolverPrintsWithThisBinary: the daemon's resolver runs
// this executable with print-env, so a project child prints the environment
// its own bridge sees.
func TestTheLocalResolverPrintsWithThisBinary(t *testing.T) {
	env, err := localProjectEnv()
	if err != nil {
		t.Fatalf("localProjectEnv: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	want := []string{exe, "print-env"}
	if len(env.PrintEnv) != len(want) || env.PrintEnv[0] != want[0] || env.PrintEnv[1] != want[1] {
		t.Fatalf("PrintEnv = %q, want %q", env.PrintEnv, want)
	}
}
