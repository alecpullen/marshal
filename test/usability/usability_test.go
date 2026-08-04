package usability

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

	env := os.Environ()
	for i, e := range env {
		if len(e) >= 5 && e[:5] == "HOME=" {
			env[i] = "HOME=" + homeDir
			break
		}
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
