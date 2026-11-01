package usability

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// isolatedHomeEnv returns a copy of the current environment with HOME set to
// a temp directory containing a minimal Marshal config. This keeps usability
// tests independent of the developer's real config (autoloaded skills, chosen
// provider, etc.) so the scripted scenarios see a predictable welcome screen.
func isolatedHomeEnv(t *testing.T) []string {
	t.Helper()
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, ".config", "marshal")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create temp marshal config dir: %v", err)
	}
	// A local-only provider + preset + profile so Marshal starts on the main
	// TUI screen instead of opening the connect-provider picker.
	config := `[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"

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
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0644); err != nil {
		t.Fatalf("write temp marshal config: %v", err)
	}

	// HOME alone no longer isolates the config: Marshal honours
	// XDG_CONFIG_HOME / XDG_DATA_HOME / MARSHAL_*_DIR ahead of HOME
	// (config-project-identity refactor), and CI runners export the XDG
	// variables. Pin every config-location override into the temp home so
	// the app reads this file and nothing else.
	//
	// The developer's ambient credential variables are dropped rather than
	// inherited: the child would otherwise be able to authenticate to a real
	// endpoint with a real key. The scripted scenarios run entirely against
	// the test-owned local provider written above, so none of these has a
	// legitimate use in the child's environment.
	overrides := map[string]string{
		"HOME":               homeDir,
		"XDG_CONFIG_HOME":    filepath.Join(homeDir, ".config"),
		"XDG_DATA_HOME":      filepath.Join(homeDir, ".local", "share"),
		"MARSHAL_CONFIG_DIR": filepath.Join(homeDir, ".config", "marshal"),
		"MARSHAL_DATA_DIR":   filepath.Join(homeDir, ".local", "share", "marshal"),
	}
	dropped := map[string]bool{}
	for _, key := range []string{
		"OLLAMA_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
		"OPENAI_API_KEY", "OPENCODE_API_KEY", "ANTHROPIC_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY",
		"MISTRAL_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
		"XAI_API_KEY", "CEREBRAS_API_KEY",
		// The live-keyring opt-in must not leak into the child either: its
		// scenarios never exercise the real OS keychain.
		"MARSHAL_CREDENTIALS_LIVE_TEST",
	} {
		dropped[key] = true
	}
	env := os.Environ()[:0]
	for _, e := range os.Environ() {
		eq := strings.Index(e, "=")
		if eq > 0 {
			name := e[:eq]
			if _, pinned := overrides[name]; pinned {
				continue
			}
			if dropped[name] {
				continue
			}
		}
		env = append(env, e)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func TestScriptedHelpOpenClose(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    t.TempDir(),
		Env:        isolatedHomeEnv(t),
		ReportDir:  "/tmp/marshal-usability",
		// The /help cheatsheet printed by "?" is taller than the default
		// 40-row terminal; at 40 rows its "Keys" header scrolls above the
		// viewport before the harness's PTY buffer ever captures it.
		Height: 80,
	})
	defer r.WriteReport()
	res, err := r.Run(context.Background(), scenario.Scenario{
		Name:  "help_open_close",
		Actor: scenario.HelpOpenClose(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("scenario failed: %+v", res)
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
		Env:        isolatedHomeEnv(t),
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
// keeps its own (unsatisfiable) postcondition contract for a later P3 plan.
func TestScriptedVisibleScreenRedraw(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}

	sess, err := harness.New(harness.Config{
		BinaryPath: binaryPath(t),
		Width:      120,
		Height:     80,
		WorkDir:    t.TempDir(),
		Env:        isolatedHomeEnv(t),
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
