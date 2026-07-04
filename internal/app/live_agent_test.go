//go:build integration

package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// newLiveAgentRunner builds an agent.Runner wired to the user's real config
// (provider, model, tools, policy). It is intended for manual integration
// testing against a local inference server.
func newLiveAgentRunner(t *testing.T) (*agent.Runner, *session.State, context.Context) {
	t.Helper()

	// Tests run from internal/app; the project root is two levels up.
	testWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectRoot, err := filepath.Abs(filepath.Join(testWd, "..", ".."))
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	cfg, err := config.Load(config.LoadOptions{WorkingDir: projectRoot})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Auto-approve shell commands so integration tests can exercise shell.run
	// and test.run without an interactive approval loop.
	cfg.Tools.Shell.AutoApprove = true

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	projectID, err := database.GetOrCreateProject(projectRoot, "live-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	state := session.New(cfg, projectRoot, time.Now(), session.Persistence{
		DB:        database,
		SessionID: "live-test",
		Logger:    slog.Default(),
	})

	ctx := context.Background()
	runner, _, err := buildAgentRunner(ctx, cfg, state, database, projectID)
	if err != nil {
		t.Fatalf("build agent runner: %v", err)
	}

	return runner, state, ctx
}

func finalAnswer(state *session.State) string {
	msgs := state.Messages()
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

func dumpTranscript(t *testing.T, state *session.State) {
	t.Helper()
	for i, m := range state.Messages() {
		content := m.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		t.Logf("msg[%d] %s: %s", i, m.Role, content)
	}
	for i, ev := range state.AuditLog() {
		t.Logf("audit[%d] %s approved=%s summary=%s", i, ev.ToolName, ev.Approval, ev.ResultSummary)
	}
}

func runLiveGoal(t *testing.T, runner *agent.Runner, state *session.State, ctx context.Context, timeout time.Duration, goal string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := runner.Run(ctx, goal)
	if err != nil {
		dumpTranscript(t, state)
		t.Fatalf("Run: %v", err)
	}
}

func TestLiveAgentReadsFile(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 2*time.Minute, "Use the file.read tool to read docs/README.md and tell me the first heading. Return the heading text in your final answer.")
	if !strings.Contains(strings.ToLower(finalAnswer(state)), "marshal") {
		t.Fatalf("final answer did not mention expected heading: %s", finalAnswer(state))
	}
}

func TestLiveAgentWritesPatch(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)

	testWd, _ := os.Getwd()
	projectRoot, _ := filepath.Abs(filepath.Join(testWd, "..", ".."))
	targetPath := filepath.Join(projectRoot, ".marshal", "_live_write_patch_target.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("target line\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(targetPath) })

	rel := ".marshal/_live_write_patch_target.txt"
	runLiveGoal(t, runner, state, ctx, 4*time.Minute, "Use the file.write_patch tool to add a second line 'added by model' to "+rel+". Use a search/replace patch block. Then confirm the change in your final answer.")

	data, rerr := os.ReadFile(targetPath)
	if rerr != nil {
		t.Fatalf("read patched file: %v", rerr)
	}
	if !strings.Contains(string(data), "added by model") {
		t.Fatalf("patch was not applied; final content: %q; final answer: %s", string(data), finalAnswer(state))
	}
}

func TestLiveAgentSearchesRepo(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 2*time.Minute, "Use the repo.search tool to search for 'DefaultMaxToolIterations' and report which file contains it.")
	if !strings.Contains(finalAnswer(state), "runner.go") {
		t.Fatalf("final answer did not mention runner.go: %s", finalAnswer(state))
	}
}

func TestLiveAgentGitStatus(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 2*time.Minute, "Use the git.status tool to check the working tree. Report whether it is clean and mention any changed files.")
	final := strings.ToLower(finalAnswer(state))
	if !strings.Contains(final, "live_agent_test.go") && !strings.Contains(final, "modified") && !strings.Contains(final, "untracked") && !strings.Contains(final, "not clean") {
		t.Fatalf("final answer did not report expected changes: %s", finalAnswer(state))
	}
}

func TestLiveAgentGitDiff(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 2*time.Minute, "Use the git.diff tool to show the current diff and summarize what changed.")
	final := strings.ToLower(finalAnswer(state))
	if !strings.Contains(final, "live_agent_test.go") && !strings.Contains(final, "diff") && !strings.Contains(final, "change") && !strings.Contains(final, "modified") && !strings.Contains(final, "added") {
		t.Fatalf("final answer did not summarize diff: %s", finalAnswer(state))
	}
}

func TestLiveAgentRunsShellCommand(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 2*time.Minute, "Use the shell.run tool to run the command 'echo hello-agent' and report the exact output in your final answer.")
	if !strings.Contains(finalAnswer(state), "hello-agent") {
		t.Fatalf("final answer did not contain expected output: %s", finalAnswer(state))
	}
}

func TestLiveAgentRunsTests(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 3*time.Minute, "Use the test.run tool with command override 'go test ./internal/agent -run TestRunnerDefaultsAreSensible' and report whether it passed.")
	if !strings.Contains(finalAnswer(state), "PASS") && !strings.Contains(finalAnswer(state), "ok") {
		t.Fatalf("final answer did not report a passing test run: %s", finalAnswer(state))
	}
}

func TestLiveAgentIndexesAndMapsRepo(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 4*time.Minute, "Run repo.index, then use repo.map and report one top-level directory or file you see.")
	final := strings.ToLower(finalAnswer(state))
	if !strings.Contains(final, "internal") && !strings.Contains(final, "go.mod") && !strings.Contains(final, "cmd") {
		t.Fatalf("final answer did not report a top-level entry: %s", finalAnswer(state))
	}
}

func TestLiveAgentIndexesAndFindsSymbols(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 4*time.Minute, "Run repo.index, then use symbols.find to locate the symbol 'DefaultMaxToolIterations' and report which file it is in.")
	if !strings.Contains(finalAnswer(state), "runner.go") {
		t.Fatalf("final answer did not mention runner.go: %s", finalAnswer(state))
	}
}

func TestLiveAgentRepoCard(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 4*time.Minute, "Run repo.index, then use repo.card and tell me the project's name or primary language.")
	final := strings.ToLower(finalAnswer(state))
	if !strings.Contains(final, "marshal") && !strings.Contains(final, "go") {
		t.Fatalf("final answer did not report project identity: %s", finalAnswer(state))
	}
}

func TestLiveAgentCountsGoFiles(t *testing.T) {
	runner, state, ctx := newLiveAgentRunner(t)
	runLiveGoal(t, runner, state, ctx, 4*time.Minute, "Use the available tools to count how many .go files are in the internal/agent directory and report the number in your final answer.")
	if !strings.ContainsAny(finalAnswer(state), "0123456789") {
		t.Fatalf("final answer did not contain a count: %s", finalAnswer(state))
	}
}
