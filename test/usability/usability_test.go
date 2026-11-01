package usability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/test/usability/actor"
	"marshal/test/usability/harness"
	"marshal/test/usability/scenario"
	"marshal/test/usability/screen"
)

func binaryPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("USABILITY_MARSHAL_BINARY")
	if p == "" {
		p = "../../marshal" // assume built at repo root
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("resolve marshal binary path: %v", err)
	}
	return abs
}

// childEnv is one isolated child environment: the variables handed to the
// Marshal child, the temp home they point at, and the test-owned provider the
// child is configured to call.
type childEnv struct {
	// Env is the child's complete environment (harness.Config.Env replaces the
	// parent's environment rather than extending it).
	Env []string
	// HomeDir is the temp home every path override points into.
	HomeDir string
	// ServerURL is the fixture provider's URL; the child config's base_url is
	// ServerURL + "/v1".
	ServerURL string
	// Requests reports how many requests the fixture provider has received. It
	// is nil for environments built without a fixture server.
	Requests func() int
}

// pinnedChildVars are the variables buildChildEnv sets to a test-owned value,
// in the order it appends them.
var pinnedChildVars = []string{
	"HOME",
	"USERPROFILE",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"MARSHAL_CONFIG_DIR",
	"MARSHAL_DATA_DIR",
	"TERM",
}

// providerKeys are the credential variables a developer may legitimately have
// exported. None has a use in a scripted run against a test-owned provider, and
// inheriting one would let the child authenticate to a real endpoint.
var providerKeys = []string{
	"OLLAMA_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
	"OPENAI_API_KEY", "OPENCODE_API_KEY", "ANTHROPIC_API_KEY",
	"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY",
	"MISTRAL_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
	"XAI_API_KEY", "CEREBRAS_API_KEY",
}

// providerFixture starts a test-owned OpenAI-compatible endpoint. It records
// every request and always answers with a deterministic error, so a scripted
// UI scenario that reaches for the network shows up as a request count instead
// of silently talking to whatever happens to run on the developer's machine.
func providerFixture(t *testing.T) (url string, requests func() int) {
	t.Helper()
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"usability fixture provider does not serve completions","type":"fixture_error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { return int(count.Load()) }
}

// isolatedHomeEnv returns an isolated environment for the Marshal child: a temp
// home holding a minimal config, and a test-owned provider the child is
// configured to call. This keeps usability tests independent of the developer's
// real config, credentials, caches and running services, so the scripted
// scenarios see a predictable welcome screen.
func isolatedHomeEnv(t *testing.T) childEnv {
	t.Helper()
	serverURL, requests := providerFixture(t)
	homeDir := t.TempDir()
	return childEnv{
		Env:       buildChildEnv(t, homeDir, serverURL+"/v1"),
		HomeDir:   homeDir,
		ServerURL: serverURL,
		Requests:  requests,
	}
}

// llmChildEnv is the opt-in LLM scenario's environment. It is deliberately
// exploratory: the child's provider is the same endpoint the in-process LLM
// actor reads from USABILITY_LLM_URL, so the scenario is not bound to the
// deterministic fixture server and makes no request-count assertion.
func llmChildEnv(t *testing.T) []string {
	t.Helper()
	base := strings.TrimRight(os.Getenv("USABILITY_LLM_URL"), "/")
	if base == "" {
		base = "http://localhost:11434"
	}
	return buildChildEnv(t, t.TempDir(), base+"/v1")
}

// buildChildEnv writes the child's minimal Marshal config into homeDir and
// returns the environment to start it with. It reads the ambient environment
// only to carry over execution essentials (PATH, platform loader variables) and
// drops everything that could redirect the child outside the test-owned home or
// authenticate it against a real endpoint.
func buildChildEnv(t *testing.T, homeDir, baseURL string) []string {
	t.Helper()
	configDir := filepath.Join(homeDir, ".config", "marshal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create temp marshal config dir: %v", err)
	}
	// A local-only provider + preset + profile so Marshal starts on the main
	// TUI screen instead of opening the connect-provider picker. The provider is
	// the test-owned fixture endpoint, never an ambient one.
	config := fmt.Sprintf(`[providers.ollama]
type = "openai_compatible"
base_url = %q

[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
context_window = 32768
temperature = 0.0
local_only = true

[profile]
default = "local"

[agent_profiles.local]
implementer = "coder"

[postmortem]
on_exit = "never"
`, baseURL)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write temp marshal config: %v", err)
	}

	// HOME alone no longer isolates the config: Marshal honours
	// XDG_CONFIG_HOME / XDG_DATA_HOME / MARSHAL_*_DIR ahead of HOME
	// (config-project-identity refactor), and CI runners export the XDG
	// variables. Pin every config-location override into the temp home so the
	// app reads this file and nothing else. USERPROFILE covers platforms where
	// os.UserHomeDir reads it instead of HOME.
	overrides := map[string]string{
		"HOME":               homeDir,
		"USERPROFILE":        homeDir,
		"XDG_CONFIG_HOME":    filepath.Join(homeDir, ".config"),
		"XDG_DATA_HOME":      filepath.Join(homeDir, ".local", "share"),
		"XDG_CACHE_HOME":     filepath.Join(homeDir, ".cache"),
		"MARSHAL_CONFIG_DIR": filepath.Join(homeDir, ".config", "marshal"),
		"MARSHAL_DATA_DIR":   filepath.Join(homeDir, ".local", "share", "marshal"),
		// A deterministic terminal type: the scripted scenarios assert on
		// rendered text, and an ambient TERM would change the child's output.
		// The value is a real developer terminal so the child takes the same
		// renderer path a user would, including its erase-character repaints.
		"TERM": "xterm-256color",
	}

	// The developer's ambient credential variables are dropped rather than
	// inherited: the child would otherwise be able to authenticate to a real
	// endpoint with a real key. The live-keyring opt-in must not leak in either:
	// no scenario exercises the real OS keychain.
	dropped := map[string]bool{"MARSHAL_CREDENTIALS_LIVE_TEST": true}
	for _, key := range providerKeys {
		dropped[key] = true
	}

	env := make([]string, 0, len(os.Environ())+len(pinnedChildVars))
	for _, e := range os.Environ() {
		name, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		// An ambient value for a pinned variable is dropped, not merged: the
		// child gets exactly one effective value, the test-owned one appended
		// below.
		if _, pinned := overrides[name]; pinned {
			continue
		}
		if dropped[name] {
			continue
		}
		// No USABILITY_* variable configures the child: the binary path and the
		// opt-in actor knobs belong to the test process, and a later task's
		// variables must not leak in by default.
		if strings.HasPrefix(name, "USABILITY_") {
			continue
		}
		env = append(env, e)
	}
	for _, name := range pinnedChildVars {
		env = append(env, name+"="+overrides[name])
	}
	return env
}

// envValues returns every value the environment slice carries for name.
func envValues(env []string, name string) []string {
	prefix := name + "="
	var values []string
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			values = append(values, strings.TrimPrefix(e, prefix))
		}
	}
	return values
}

// TestIsolatedHomeEnv proves the child environment ignores ambient path
// overrides and credentials: every config-location variable resolves inside the
// temp home exactly once, no provider key survives, and the configured provider
// endpoint is the test-owned fixture server.
func TestIsolatedHomeEnv(t *testing.T) {
	// Poison everything the builder is supposed to replace. t.Setenv restores
	// the values when the test ends.
	const poison = "/tmp/marshal-usability-poison"
	for _, name := range []string{
		"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"XDG_CACHE_HOME", "MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR",
	} {
		t.Setenv(name, filepath.Join(poison, strings.ToLower(name)))
	}
	t.Setenv("TERM", "marshal-poison-term")
	for _, name := range providerKeys {
		t.Setenv(name, poison)
	}
	for _, name := range []string{
		"MARSHAL_CREDENTIALS_LIVE_TEST",
		"USABILITY_MARSHAL_BINARY", "USABILITY_REPORT_DIR",
		"USABILITY_REQUIRE_BINARY", "USABILITY_LLM_URL", "USABILITY_LLM_MODEL",
	} {
		t.Setenv(name, poison)
	}

	env := isolatedHomeEnv(t)

	// Exactly one effective value per pinned variable, inside the temp home.
	for _, name := range pinnedChildVars {
		values := envValues(env.Env, name)
		if len(values) != 1 {
			t.Fatalf("%s: child environment carries %d entries, want exactly 1", name, len(values))
		}
		if name == "TERM" {
			continue
		}
		if !strings.HasPrefix(values[0], env.HomeDir) {
			t.Fatalf("%s: child value is outside the temp home", name)
		}
	}
	if values := envValues(env.Env, "TERM"); len(values) != 1 || values[0] != "xterm-256color" {
		t.Fatalf("TERM: child environment does not carry exactly one deterministic value")
	}

	// No credential, keyring opt-in or harness knob reaches the child.
	for _, name := range append(append([]string{}, providerKeys...), "MARSHAL_CREDENTIALS_LIVE_TEST") {
		if len(envValues(env.Env, name)) != 0 {
			t.Fatalf("%s leaked into the child environment", name)
		}
	}
	for _, e := range env.Env {
		if strings.HasPrefix(e, "USABILITY_") {
			t.Fatalf("a USABILITY_* variable leaked into the child environment: %s", strings.SplitN(e, "=", 2)[0])
		}
	}

	// The configured provider is the fixture server, not an ambient endpoint.
	if !strings.HasPrefix(env.ServerURL, "http://127.0.0.1:") && !strings.HasPrefix(env.ServerURL, "http://[::1]:") {
		t.Fatalf("fixture provider is not a loopback httptest server")
	}
	cfg, err := os.ReadFile(filepath.Join(env.HomeDir, ".config", "marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read temp marshal config: %v", err)
	}
	want := `base_url = "` + env.ServerURL + `/v1"`
	if !strings.Contains(string(cfg), want) {
		t.Fatalf("temp marshal config does not point at the fixture server's /v1 endpoint")
	}

	// Building the environment must not itself reach the provider.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests while building the child environment, want 0", n)
	}
}

func TestScriptedHelpOpenClose(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}
	env := isolatedHomeEnv(t)
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    t.TempDir(),
		Env:        env.Env,
		ReportDir:  "/tmp/marshal-usability",
		// The /help cheatsheet printed by "?" is taller than the default
		// 40-row terminal; at 40 rows its "Keys" header scrolls above the
		// viewport before the harness's PTY buffer ever captures it.
		Height: 80,
	})
	defer r.WriteReport()
	// Bounded: a close that never takes effect must fail with the runner's
	// recorded result (and its screen_on_timeout viewport), not hang.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := r.Run(ctx, scenario.Scenario{
		Name:  "help_open_close",
		Actor: scenario.HelpOpenClose(),
	})
	if err != nil {
		t.Fatalf("Run: %v (result %+v)", err, res)
	}
	if !res.Success {
		t.Fatalf("scenario failed: %+v", res)
	}
	// Opening and closing Help needs no model: any request to the test-owned
	// provider means the child reached for the network on its own.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests, want 0: the scripted Help scenario must not call a model", n)
	}
}

// TestHelpScriptDoesNotCompleteWithoutClose drives the real HelpOpenClose
// script against synthetic viewports. After a close that does nothing, the
// script's wait-only postcondition must keep yielding no-op; only a viewport
// that actually shows the prompt with no Help title lets it report done.
func TestHelpScriptDoesNotCompleteWithoutClose(t *testing.T) {
	ctx := context.Background()
	scr := scenario.HelpOpenClose()

	// Open: the prompt is already on screen, so the script types "?".
	act, err := scr.Act(ctx, screen.Screen{Content: promptMarker})
	if err != nil {
		t.Fatalf("Act (open): %v", err)
	}
	if act.Type != actor.ActionType || act.Text != "?" {
		t.Fatalf("open action = %+v, want type '?'", act)
	}

	// The panel is up, so the script sends esc.
	act, err = scr.Act(ctx, screen.Screen{Content: helpTitleMarker + "\n  /help cheatsheet"})
	if err != nil {
		t.Fatalf("Act (help open): %v", err)
	}
	if act.Type != actor.ActionKey || act.Key != "esc" {
		t.Fatalf("close action = %+v, want key esc", act)
	}

	// The close did nothing. Neither a viewport where the panel covers the
	// prompt nor one where the prompt peeks through is a completed close:
	// the postcondition holds conjunctively, so Help in the viewport keeps
	// the script unfinished no matter how often it is observed.
	stillOpen := []screen.Screen{
		{Content: helpTitleMarker + "\n  /help cheatsheet"},
		{Content: helpTitleMarker + "\n  /help cheatsheet\n" + promptMarker},
	}
	for i := 0; i < 5; i++ {
		for j, sc := range stillOpen {
			act, err = scr.Act(ctx, sc)
			if err != nil {
				t.Fatalf("Act (ineffective close, observation %d/%d): %v", i, j, err)
			}
			if act.Type != actor.ActionNoOp {
				t.Fatalf("observation %d/%d after ineffective close = %+v, want noop (never done)", i, j, act)
			}
		}
	}

	// The panel is gone: the wait-only step matches, reports no-op, and the
	// next observation completes the scenario.
	closed := screen.Screen{Content: promptMarker + "\n❯ "}
	act, err = scr.Act(ctx, closed)
	if err != nil {
		t.Fatalf("Act (closed): %v", err)
	}
	if act.Type != actor.ActionNoOp {
		t.Fatalf("action on the closed viewport = %+v, want noop", act)
	}
	act, err = scr.Act(ctx, closed)
	if err != nil {
		t.Fatalf("Act (done): %v", err)
	}
	if act.Type != actor.ActionDone || !act.Success {
		t.Fatalf("final action = %+v, want done success", act)
	}
}

func TestLLMSubtractFix(t *testing.T) {
	if os.Getenv("USABILITY_LLM_MODEL") == "" {
		t.Skip("set USABILITY_LLM_MODEL to run LLM scenarios")
	}
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found")
	}
	workDir := copyFixture(t, "go-calc-broken")
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    workDir,
		Env:        llmChildEnv(t),
		ReportDir:  "/tmp/marshal-usability",
	})
	defer r.WriteReport()
	res, err := r.Run(context.Background(), scenario.Scenario{
		Name:  "subtract_fix",
		Actor: scenario.SubtractFix(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("scenario failed: %+v", res)
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("fixtures", name)
	dst := filepath.Join(t.TempDir(), name)
	// simple recursive copy; or use cp -R via os/exec for brevity
	cmd := exec.Command("cp", "-R", src, dst)
	if err := cmd.Run(); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

// helpTitleMarker is the text the /help doc panel shows in its chrome title.
// It is matched case-sensitively so the lowercase "? help" footer hint cannot
// stand in for an open panel.
const helpTitleMarker = "Help"

// promptMarker is the welcome banner's call to action, which only the main
// screen shows: the help panel covers the transcript where it is drawn.
const promptMarker = "Type a question"

// waitForViewport waits until pred holds for two consecutive current
// viewports, failing the test on a terminal model error instead of treating a
// broken screen as a false result.
func waitForViewport(t *testing.T, s *harness.Session, timeout time.Duration, what string, pred func(screen.Screen) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var modelErr error
	err := s.WaitFor(ctx, func(snap harness.Snapshot) bool {
		scr, parseErr := screen.Parse(snap)
		if parseErr != nil {
			// End the wait early; the model error is fatal and reported below.
			modelErr = parseErr
			return true
		}
		return pred(scr)
	})
	if modelErr != nil {
		t.Fatalf("%s: terminal model error: %v", what, modelErr)
	}
	if err != nil {
		t.Fatalf("%s: %v\nscreen: %q", what, err, string(s.Snapshot().Content))
	}
}

// writeHelperScript writes an executable POSIX shell helper to a temp dir.
func writeHelperScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

// TestScriptedVisibleScreenRedraw proves the harness snapshot follows the
// visible screen across a real redraw: Help opens over the transcript and,
// once Escape closes it, the snapshot no longer contains the Help title while
// the prompt is back.
//
// This is deliberately separate from TestScriptedHelpOpenClose: that scenario
// asserts the same transition through the scripted actor's postcondition.
func TestScriptedVisibleScreenRedraw(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}

	env := isolatedHomeEnv(t)
	sess, err := harness.New(harness.Config{
		BinaryPath: binaryPath(t),
		Width:      120,
		Height:     80,
		WorkDir:    t.TempDir(),
		Env:        env.Env,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	defer sess.Close()

	waitForViewport(t, sess, 30*time.Second, "initial prompt", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, promptMarker)
	})

	if err := sess.Send("?"); err != nil {
		t.Fatalf("Send(?): %v", err)
	}
	waitForViewport(t, sess, 30*time.Second, "help open", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, helpTitleMarker)
	})

	if err := sess.SendKey("esc"); err != nil {
		t.Fatalf("SendKey(esc): %v", err)
	}
	waitForViewport(t, sess, 30*time.Second, "help closed and prompt restored", func(scr screen.Screen) bool {
		return !strings.Contains(scr.Content, helpTitleMarker) && strings.Contains(scr.Content, promptMarker)
	})

	// A redraw of the Help panel needs no model: any request to the test-owned
	// provider means the child reached for the network on its own.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests, want 0: the scripted redraw must not call a model", n)
	}
}

// TestScriptedGracefulQuit requires a normal exit, not merely a process that
// disappeared: after /quit the child's own Wait returns nil before its deadline,
// so a forced kill at cleanup cannot stand in for the quit. The isolated config
// pins [postmortem] on_exit = "never", which removes the interactive postmortem
// question from the exit path.
func TestScriptedGracefulQuit(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}

	env := isolatedHomeEnv(t)
	sess, err := harness.New(harness.Config{
		BinaryPath: binaryPath(t),
		Width:      120,
		Height:     80,
		WorkDir:    t.TempDir(),
		Env:        env.Env,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	// Close is cleanup only. The assertion below is that the child exited on its
	// own, not that it was removed.
	defer sess.Close()

	waitForViewport(t, sess, 30*time.Second, "initial prompt", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, promptMarker)
	})

	if err := sess.Send("/quit"); err != nil {
		t.Fatalf("Send(/quit): %v", err)
	}
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter): %v", err)
	}

	// A forced kill would surface here as a non-nil *exec.ExitError, so nil is
	// the assertion that the child exited normally before its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.Wait(ctx); err != nil {
		t.Fatalf("Wait after /quit = %v, want nil for a normal exit\nscreen: %q", err, string(sess.Snapshot().Content))
	}

	// Quitting needs no model: any request to the test-owned provider means the
	// child reached for the network on its own.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests, want 0: the scripted quit must not call a model", n)
	}
}

// TestVisibleScreenAbsencePredicateNeedsTheClose is the deterministic negative
// case for the redraw test: with the close action suppressed, a helper that
// keeps drawing the Help title never satisfies the absence predicate, so that
// predicate cannot pass on a screen that still shows Help.
func TestVisibleScreenAbsencePredicateNeedsTheClose(t *testing.T) {
	bin := writeHelperScript(t, `
printf 'Help\r\n  /help cheatsheet\r\n'
printf 'Type a question\r\n'
sleep 10
`)
	sess, err := harness.New(harness.Config{BinaryPath: bin, Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	defer sess.Close()

	waitForViewport(t, sess, 10*time.Second, "help drawn", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, helpTitleMarker)
	})

	// The close action is suppressed: the absence predicate used by the redraw
	// test must keep failing for as long as the helper draws Help.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = sess.WaitFor(ctx, func(snap harness.Snapshot) bool {
		scr, parseErr := screen.Parse(snap)
		return parseErr == nil &&
			!strings.Contains(scr.Content, helpTitleMarker) &&
			strings.Contains(scr.Content, promptMarker)
	})
	if err == nil {
		t.Fatalf("absence predicate held while Help was still on screen")
	}
	if !strings.Contains(string(sess.Snapshot().Content), helpTitleMarker) {
		t.Fatalf("help marker disappeared from the viewport without a close action")
	}
}
