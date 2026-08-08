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
	"marshal/internal/agent/agenttest"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
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
	// The run directory is where @run/... artifact paths resolve (Critical 1
	// binds ArtifactRoot to c.Paths.Dir). Plan slug = "plan".
	runDir := filepath.Join(root, ".marshal", "pipeline", "plan")
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
			// real implementer would. The prompt line is now an @run/...
			// basename reference; resolve it against the run directory.
			if i := strings.Index(prompt, "Write your full report to "); i >= 0 {
				rest := prompt[i+len("Write your full report to "):]
				if j := strings.Index(rest, " — "); j >= 0 {
					path := strings.TrimSpace(rest[:j])
					if strings.HasPrefix(path, "@run/") {
						path = filepath.Join(runDir, strings.TrimPrefix(path, "@run/"))
					}
					_ = os.WriteFile(path, []byte("report\n"), 0o644)
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

// TestPipelineRunExecResolvesArtifactsUnderRunRoot drives a real runExec
// with a RegistryFactory and asserts the child runner's native
// file.write_patch resolves the @run/... artifact path under the artifact
// root (the run directory), not the worktree or the main checkout. This
// proves the isolation path — ExecCtx + RegistryFactory — is live.
func TestPipelineRunExecResolvesArtifactsUnderRunRoot(t *testing.T) {
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

	// Artifact root stands in for the run's .marshal/pipeline/<slug>/ dir.
	artifactRoot := t.TempDir()
	execCtx := ExecutionContext{
		RepoRoot:      root,
		WorkspaceRoot: root,
		ArtifactRoot:  artifactRoot,
		ArtifactAlias: "@run",
	}
	regFactory := func(ctx ExecutionContext, scope RegistryScope) (*registry.Registry, error) {
		childState := session.New(config.Default(), ctx.WorkspaceRoot, time.Now(), session.Persistence{})
		childReg := registry.New()
		if err := native.RegisterAll(childReg, native.Options{
			WorkspaceRoot: ctx.WorkspaceRoot,
			CommandRunner: &nativeCmdRunner{},
			SessionState:  childState,
			Config:        config.Default(),
			NamedRoots:    ctx.NamedRoots(),
		}); err != nil {
			return nil, err
		}
		return registry.ArtifactWriterView(childReg, ctx.ArtifactAlias), nil
	}

	d := Dispatcher{
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			p := &agenttest.ScriptedProvider{Responses: []string{
				`{"rationale":"write brief","action":{"type":"tool_call","tool":"file.write_patch","args":{"patch":"File: @run/task-1-brief.md\n<<<<<<< SEARCH\n=======\nhello\n>>>>>>> REPLACE"}}}`,
				`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
			}}
			pol := policy.NewEngine(&config.Config{}, nil)
			pol.SetApprovalMode(policy.ModeAuto)
			runner := agent.NewRunner(p, registry.New(), pol, session.New(config.Default(), execCtx.WorkspaceRoot, time.Now(), session.Persistence{}), "test-model")
			runner.Role = role
			return runner, nil
		},
		ExecCtx:         execCtx,
		RegistryFactory: regFactory,
	}

	if _, err := d.runExec(context.Background(), agent.RoleReviewer, swarm.ScopeArtifactWriter, "review", "write the verdict"); err != nil {
		t.Fatalf("runExec: %v", err)
	}

	// The artifact must actually exist on disk under the artifact root.
	if _, err := os.Stat(filepath.Join(artifactRoot, "task-1-brief.md")); err != nil {
		t.Errorf("artifact not written under artifact root: %v", err)
	}
	// And it must NOT be under the main checkout.
	if _, err := os.Stat(filepath.Join(root, "task-1-brief.md")); err == nil {
		t.Error("artifact leaked into the main checkout")
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

// nativeCmdRunner implements native.CommandRunner for the isolation-path
// integration test; commands are never actually executed.
type nativeCmdRunner struct{}

func (nativeCmdRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	return native.CommandResult{Stdout: "ok", ExitCode: 0}, nil
}

// TestInspectionDrivesAdaptiveAndStrictExecution builds three plans — fully
// deterministic, mixed (deterministic + agent), and prose-only legacy — and
// verifies the shared inspection classifies them and the controller executes
// them with the right dispatch counts and strict-mode blocking.
func TestInspectionDrivesAdaptiveAndStrictExecution(t *testing.T) {
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

	detPlan := filepath.Join(root, "det.md")
	writeFile(t, detPlan, "# Plan\n\n## Task 1: Add file\n\n"+
		"```marshal.file path=\"created.txt\"\nhello\n```\n\n"+
		"```marshal.assert\nkind = \"file.exists\"\nfile = \"created.txt\"\n```\n")

	mixedPlan := filepath.Join(root, "mixed.md")
	writeFile(t, mixedPlan, "# Plan\n\n## Task 1: Add file\n\n"+
		"```marshal.file path=\"created.txt\"\nhello\n```\n\n"+
		"## Task 2: Resolve\n\n"+
		"```marshal.agent\nscope = [\"internal/app\"]\nreason = \"needs design judgment\"\n```\n")

	legacyPlan := filepath.Join(root, "legacy.md")
	writeFile(t, legacyPlan, "# Plan\n\n## Task 1: Explain\n\nProse only.\n")

	// Auto inspection: adaptive for the two executable plans, agent for legacy.
	det, err := InspectPlan(detPlan, root, StrategyAuto)
	if err != nil {
		t.Fatalf("InspectPlan(det): %v", err)
	}
	if det.EffectiveStrategy != StrategyAdaptive {
		t.Fatalf("det EffectiveStrategy = %q, want adaptive", det.EffectiveStrategy)
	}
	mixed, err := InspectPlan(mixedPlan, root, StrategyAuto)
	if err != nil {
		t.Fatalf("InspectPlan(mixed): %v", err)
	}
	if mixed.EffectiveStrategy != StrategyAdaptive {
		t.Fatalf("mixed EffectiveStrategy = %q, want adaptive", mixed.EffectiveStrategy)
	}
	legacy, err := InspectPlan(legacyPlan, root, StrategyAuto)
	if err != nil {
		t.Fatalf("InspectPlan(legacy): %v", err)
	}
	if legacy.EffectiveStrategy != StrategyAgent {
		t.Fatalf("legacy EffectiveStrategy = %q, want agent", legacy.EffectiveStrategy)
	}

	// Strict inspection blocks the mixed and legacy plans before worktree.
	mixedStrict, err := InspectPlan(mixedPlan, root, StrategyStrict)
	if err != nil {
		t.Fatalf("InspectPlan(mixed, strict): %v", err)
	}
	if !mixedStrict.StrictBlocked {
		t.Fatal("strict inspection must block a mixed plan")
	}
	legacyStrict, err := InspectPlan(legacyPlan, root, StrategyStrict)
	if err != nil {
		t.Fatalf("InspectPlan(legacy, strict): %v", err)
	}
	if !legacyStrict.StrictBlocked {
		t.Fatal("strict inspection must block a prose-only plan")
	}

	// Fully deterministic adaptive task performs zero implementer dispatches.
	detDispatches := 0
	detD := Dispatcher{exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		detDispatches++
		return "STATUS: DONE\nTESTS: ok\n", nil
	}}
	detC, err := NewController(ControllerOpts{
		PlanPath: detPlan, RepoRoot: root, Git: worktree.CLIGitOps{},
		Dispatch: detD, Verifier: Verifier{Runner: &noopRunner{}, Timeout: time.Minute},
		Strategy: StrategyAdaptive, MaxFixRounds: 1,
	})
	if err != nil {
		t.Fatalf("NewController(det): %v", err)
	}
	if err := detC.Run(context.Background()); err != nil {
		t.Fatalf("det Run: %v", err)
	}
	if detDispatches != 0 {
		t.Fatalf("fully deterministic adaptive task made %d implementer dispatches, want 0", detDispatches)
	}

	// Mixed adaptive task dispatches only the fallback task.
	mixedDispatches := 0
	mixedD := Dispatcher{exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		mixedDispatches++
		return "STATUS: DONE\nTESTS: ok\n", nil
	}}
	mixedC, err := NewController(ControllerOpts{
		PlanPath: mixedPlan, RepoRoot: root, Git: worktree.CLIGitOps{},
		Dispatch: mixedD, Verifier: Verifier{Runner: &noopRunner{}, Timeout: time.Minute},
		Strategy: StrategyAdaptive, MaxFixRounds: 1,
	})
	if err != nil {
		t.Fatalf("NewController(mixed): %v", err)
	}
	if err := mixedC.Run(context.Background()); err != nil {
		t.Fatalf("mixed Run: %v", err)
	}
	if mixedDispatches != 1 {
		t.Fatalf("mixed adaptive task made %d dispatches, want 1 (only the fallback task)", mixedDispatches)
	}

	// Strict blocks before EnsureWorktree for the mixed plan.
	strictD := Dispatcher{exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		return "STATUS: DONE\nTESTS: ok\n", nil
	}}
	strictC, err := NewController(ControllerOpts{
		PlanPath: mixedPlan, RepoRoot: root, Git: worktree.CLIGitOps{},
		Dispatch: strictD, Verifier: Verifier{Runner: &noopRunner{}, Timeout: time.Minute},
		Strategy: StrategyStrict, MaxFixRounds: 1,
	})
	if err != nil {
		t.Fatalf("NewController(strict): %v", err)
	}
	if err := strictC.Run(context.Background()); err == nil {
		t.Fatal("strict run must fail for a mixed plan")
	}
	if strictC.Worktree.Path != "" {
		t.Fatal("strict run must not create a worktree for a blocked plan")
	}
}
