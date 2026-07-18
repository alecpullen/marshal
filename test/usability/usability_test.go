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

func TestScriptedHelpOpenClose(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    t.TempDir(),
		ReportDir:  "/tmp/marshal-usability",
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
