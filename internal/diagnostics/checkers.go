package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"marshal/internal/sandbox/envutil"
)

const maxDiagnosticsLines = 30

// Runner abstracts command execution so tests can stub it out.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
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

// LSPSource is an optional diagnostics source that is consulted before the
// configured command checkers. When it returns ok=true, its output is used
// directly; otherwise the Checker falls back to running configured commands.
type LSPSource interface {
	Diagnostics(lang, filePath string) (string, bool)
}

type Checker struct {
	commands map[string]string
	runner   Runner
	lsp      LSPSource
	mu       sync.Mutex
}

// SetLSPSource installs an LSP diagnostics source consulted before the
// configured command checkers.
func (c *Checker) SetLSPSource(src LSPSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lsp = src
}

func NewChecker(commands map[string]string) *Checker {
	return &Checker{commands: commands, runner: execRunner}
}

func (c *Checker) Check(files []string, language string) (string, error) {
	// Snapshot the LSP source under the lock, then release it before running
	// any command. This keeps the lock held only for the cheap reference read
	// rather than for the full (up to 15s) command execution, so concurrent
	// Check calls don't serialize and SetLSPSource isn't blocked for the
	// duration of a run.
	c.mu.Lock()
	lsp := c.lsp
	c.mu.Unlock()
	if lsp != nil && len(files) > 0 {
		if out, ok := lsp.Diagnostics(language, files[0]); ok {
			return out, nil
		}
	}
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
