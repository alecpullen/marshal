package diagnostics

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxDiagnosticsLines = 30

// Runner abstracts command execution so tests can stub it out.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Checker struct {
	commands map[string]string
	runner   Runner
}

func NewChecker(commands map[string]string) *Checker {
	return &Checker{commands: commands, runner: execRunner}
}

func (c *Checker) Check(files []string, language string) (string, error) {
	tmpl, ok := c.commands[language]
	if !ok {
		if language == "go" {
			tmpl = "go vet {package}"
		} else {
			return "", nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmdStr := strings.ReplaceAll(tmpl, "{package}", inferPackage(files))
	parts := strings.Fields(cmdStr)
	cmd := parts[0]
	cmdArgs := parts[1:]
	out, err := c.runner(ctx, cmd, cmdArgs...)
	if ctx.Err() == context.DeadlineExceeded {
		return "diagnostics skipped (timeout)", nil
	}
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("checker %s: %w", cmd, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "diagnostics: none", nil
	}
	if len(lines) > maxDiagnosticsLines {
		lines = lines[:maxDiagnosticsLines]
		lines = append(lines, "...")
	}
	return "diagnostics:\n" + strings.Join(lines, "\n"), nil
}

func inferPackage(files []string) string {
	if len(files) == 0 {
		return "./..."
	}
	dir := filepath.Dir(files[0])
	return "./" + dir
}
