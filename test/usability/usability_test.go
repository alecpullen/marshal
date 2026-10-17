package usability

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/test/usability/scenario"
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
