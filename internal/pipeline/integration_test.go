//go:build integration

package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/worktree"
)

// TestPipelineWorkerWritesToWorktreeNotMainRepo builds a real git repo,
// runs a one-task pipeline, and verifies the worker's file write lands in
// the pipeline worktree — not the main checkout.
func TestPipelineWorkerWritesToWorktreeNotMainRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires git")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, filepath.Join(root, "README.md"), "# test repo\n")
	gitAddCommit(t, root, "initial")

	planPath := filepath.Join(root, "plan.md")
	writeFile(t, planPath, "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n## Task 1: Write a file\n\nWrite a file called `output.txt` with the content `hello`.\n")

	// Track what directory the "worker" tries to write to.
	var workerDir string
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			// Extract the WorkDir from the prompt and record it.
			if i := strings.Index(prompt, "Work from: "); i >= 0 {
				rest := prompt[i+len("Work from: "):]
				if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
					workerDir = strings.TrimSpace(rest[:nl])
				}
			}
			// Write the file in the worker dir.
			if workerDir != "" {
				_ = os.WriteFile(filepath.Join(workerDir, "output.txt"), []byte("hello\n"), 0o644)
			}
			// Write the report file the reviewer preflight requires, as a
			// real implementer would. The prompt line is
			// "Write your full report to <path> — what you changed...".
			if i := strings.Index(prompt, "Write your full report to "); i >= 0 {
				rest := prompt[i+len("Write your full report to "):]
				if j := strings.Index(rest, " — "); j >= 0 {
					_ = os.WriteFile(strings.TrimSpace(rest[:j]), []byte("report\n"), 0o644)
				}
			}
			// Return a valid review report for reviewer dispatches (per-task
			// and branch), and a valid implementer report otherwise.
			if strings.Contains(prompt, "You are reviewing one task's implementation") ||
				strings.Contains(prompt, "You are the final review before this branch merges") {
				return "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", nil
			}
			return "STATUS: DONE\nTESTS: echo ok — ok\n", nil
		},
	}

	c, err := NewController(ControllerOpts{
		PlanPath:     planPath,
		RepoRoot:     root,
		Git:          worktree.CLIGitOps{},
		Dispatch:     d,
		Verifier:     Verifier{Runner: &noopRunner{}, Timeout: time.Minute},
		MaxFixRounds: 1,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The worktree path must be under .marshal/pipeline/<slug>/worktrees/
	if !strings.Contains(workerDir, ".marshal/pipeline") || !strings.Contains(workerDir, "worktrees") {
		t.Errorf("workerDir = %q, should be in the pipeline worktree", workerDir)
	}

	// output.txt must exist in the worktree, not in the main checkout.
	if _, err := os.Stat(filepath.Join(workerDir, "output.txt")); err != nil {
		t.Errorf("output.txt missing from worktree %q: %v", workerDir, err)
	}
	if _, err := os.Stat(filepath.Join(root, "output.txt")); err == nil {
		t.Error("output.txt should NOT exist in the main checkout")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--initial-branch=main", dir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
}

func gitAddCommit(t *testing.T, dir, msg string) {
	t.Helper()
	exec.Command("git", "-C", dir, "add", ".").Run()
	if err := exec.Command("git", "-C", dir, "commit", "-m", msg).Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, dir, command string) (string, error) {
	return "ok\n", nil
}
