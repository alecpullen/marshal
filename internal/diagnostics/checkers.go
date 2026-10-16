package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/sandbox/envutil"
)

const maxDiagnosticsLines = 30

// Runner abstracts command execution so tests can stub it out.
type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = scrubDiagnosticsEnv(os.Environ())
	return cmd.CombinedOutput()
}

// scrubDiagnosticsEnv returns a copy of parentEnv with secret-bearing and
// dynamic-loader variables removed. Diagnostics commands auto-run after file
// edits, so they must not inherit credentials or hijack the loader.
func scrubDiagnosticsEnv(parentEnv []string) []string {
	out := make([]string, 0, len(parentEnv))
	for _, kv := range parentEnv {
		key := envutil.EnvKey(kv)
		if envutil.IsSecretKey(key) || envutil.IsDangerousKey(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Checker runs the configured per-language command checkers over the given
// files. The commands run in dir — the active root, so a session inside a
// worktree is checked against the worktree's copy, not the project's.
type Checker struct {
	commands map[string]string
	runner   Runner
}

func NewChecker(commands map[string]string) *Checker {
	return &Checker{commands: commands, runner: execRunner}
}

func (c *Checker) Check(dir string, files []string, language string) (string, error) {
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

	pkgs := inferPackages(files)
	if !strings.Contains(tmpl, "{package}") {
		// A placeholder-free template is package-agnostic; running it once
		// per dir would only duplicate output.
		pkgs = pkgs[:1]
	}
	var outputs []string
	for _, pkg := range pkgs {
		cmdStr := strings.ReplaceAll(tmpl, "{package}", pkg)
		parts := strings.Fields(cmdStr)
		out, err := c.runner(ctx, dir, parts[0], parts[1:]...)
		if ctx.Err() == context.DeadlineExceeded {
			return "diagnostics skipped (timeout)", nil
		}
		if err != nil && len(out) == 0 {
			return "", fmt.Errorf("checker %s: %w", parts[0], err)
		}
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			outputs = append(outputs, trimmed)
		}
	}
	if len(outputs) == 0 {
		return "diagnostics: none", nil
	}
	lines := strings.Split(strings.Join(outputs, "\n"), "\n")
	if len(lines) > maxDiagnosticsLines {
		lines = lines[:maxDiagnosticsLines]
		lines = append(lines, "...")
	}
	return "diagnostics:\n" + strings.Join(lines, "\n"), nil
}

// inferPackages returns one "./<dir>" per distinct directory in files,
// order-preserving. Empty input checks the whole project, preserving the
// original inferPackage fallback.
func inferPackages(files []string) []string {
	if len(files) == 0 {
		return []string{"./..."}
	}
	seen := make(map[string]bool, len(files))
	var pkgs []string
	for _, f := range files {
		pkg := "./" + filepath.Dir(f)
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}
