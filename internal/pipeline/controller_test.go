package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/llm/provider"
	"marshal/internal/worktree"
)

// scriptedDispatch returns a Dispatcher whose exec pops one canned output
// per call, and a pointer to the prompts it received.
func scriptedDispatch(t *testing.T, outputs ...string) (Dispatcher, *[]string) {
	t.Helper()
	prompts := &[]string{}
	i := 0
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			*prompts = append(*prompts, prompt)
			if i >= len(outputs) {
				return "", errors.New("scripted dispatch exhausted")
			}
			out := outputs[i]
			i++
			return out, nil
		},
	}
	return d, prompts
}

// testController builds a Controller over fakes with a two-task plan.
func testController(t *testing.T, d Dispatcher, fakeCmd *FakeCommandRunner) *Controller {
	t.Helper()
	root := t.TempDir()
	planPath := filepath.Join(root, "2026-07-27-test-plan.md")
	body := "# Test Plan\n\n## Global Constraints\n\n- Constraint A.\n\n---\n\n" +
		"### Task 1: First\n\n**Interfaces:**\n- Produces: `func A() error`\n\nBody one.\n\n" +
		"### Task 2: Second\n\nBody two.\n"
	if err := os.WriteFile(planPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[root] = g.Refs["main"]
	g.Dirty = true

	c, err := NewController(ControllerOpts{
		PlanPath:     planPath,
		RepoRoot:     root,
		Git:          g,
		Dispatch:     d,
		Verifier:     Verifier{Build: "go build ./...", Timeout: time.Minute, Runner: fakeCmd},
		MaxFixRounds: 2,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return c
}

func TestRunTaskHappyPath(t *testing.T) {
	d, prompts := scriptedDispatch(t, "STATUS: DONE\nTESTS: go test ./... — 3/3 pass\n")
	cmd := NewFakeCommandRunner()
	c := testController(t, d, cmd)

	spec, _ := c.Plan.Task(1)
	res, err := c.runTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.Head == "" || res.Head == res.Base {
		t.Fatalf("result = %+v, want a new commit", res)
	}

	// The brief was written and named in the prompt; the plan file was not.
	briefPath := c.Paths.Brief(1)
	data, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if !strings.Contains(string(data), "Body one.") {
		t.Errorf("brief missing the task body:\n%s", data)
	}
	if len(*prompts) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], briefPath) {
		t.Errorf("prompt does not name the brief path:\n%s", (*prompts)[0])
	}
	if strings.Contains((*prompts)[0], c.Plan.Path) {
		t.Error("prompt names the whole plan file; the implementer gets only its brief")
	}

	// The controller committed, with the plan slug and task in the subject.
	g := c.Git.(*worktree.FakeGitOps)
	if len(g.Commits) != 1 {
		t.Fatalf("commits = %v, want 1", g.Commits)
	}
	if !strings.Contains(g.Commits[0], "2026-07-27-test-plan: task 1 — First") {
		t.Errorf("commit subject = %q", g.Commits[0])
	}
	if !strings.Contains(g.Commits[0], "3/3 pass") {
		t.Errorf("commit body dropped the test summary: %q", g.Commits[0])
	}
}

func TestRunTaskGateFailureDispatchesFixer(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: go test ./... — pass\n",
		"STATUS: DONE\nTESTS: go test ./... — pass after fix\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	// The gate fails once, then passes: the first pass needs a fixer, the
	// second reaches the commit.
	c.Verifier.Runner = &flakyRunner{
		failFirst: true,
		failOut:   "plan.go:12: undefined: foo",
	}

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if len(*prompts) != 2 {
		t.Fatalf("dispatches = %d, want implementer then fixer", len(*prompts))
	}
	if !strings.Contains((*prompts)[1], "plan.go:12: undefined: foo") {
		t.Errorf("fix prompt does not carry the build output:\n%s", (*prompts)[1])
	}
	g := c.Git.(*worktree.FakeGitOps)
	if len(g.Commits) != 1 {
		t.Errorf("commits = %v, want exactly one (nothing is committed while the gate fails)", g.Commits)
	}
}

func TestRunTaskGateExhaustionFails(t *testing.T) {
	d, _ := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: pass\n",
		"STATUS: DONE\nTESTS: pass\n",
		"STATUS: DONE\nTESTS: pass\n",
	)
	cmd := NewFakeCommandRunner()
	cmd.SetError("go build ./...", "still broken", errors.New("exit status 2"))
	c := testController(t, d, cmd)

	spec, _ := c.Plan.Task(1)
	_, err := c.runTask(context.Background(), spec)
	if err == nil {
		t.Fatal("gate never passed: want error, got nil")
	}
	if !strings.Contains(err.Error(), "fix rounds") {
		t.Errorf("error = %v, want it to name the exhausted fix budget", err)
	}
	g := c.Git.(*worktree.FakeGitOps)
	if len(g.Commits) != 0 {
		t.Errorf("commits = %v, want none", g.Commits)
	}
}

func TestRunTaskCarriesEarlierInterfaces(t *testing.T) {
	d, prompts := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n")
	c := testController(t, d, NewFakeCommandRunner())

	spec, _ := c.Plan.Task(2)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !strings.Contains((*prompts)[0], "func A() error") {
		t.Errorf("task 2's prompt lost task 1's produced interface:\n%s", (*prompts)[0])
	}
}

func TestRunTaskSkippedGateIsNoted(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Verifier = Verifier{Runner: NewFakeCommandRunner()} // no commands

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	lines, err := c.Ledger.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "gate skipped") {
		t.Errorf("a skipped gate must be recorded, got:\n%s", joined)
	}
}

// flakyRunner fails its first call and succeeds on every call after it.
type flakyRunner struct {
	failFirst bool
	failOut   string
	calls     int
}

func (f *flakyRunner) Run(ctx context.Context, dir, command string) (string, error) {
	f.calls++
	if f.failFirst && f.calls == 1 {
		return f.failOut, errors.New("exit status 2")
	}
	return "", nil
}

func TestReviewTaskCleanFirstPass(t *testing.T) {
	d, prompts := scriptedDispatch(t, "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	spec, _ := c.Plan.Task(1)

	res, err := c.reviewTask(context.Background(), spec, taskResult{Base: "base", Head: "head"})
	if err != nil {
		t.Fatalf("reviewTask: %v", err)
	}
	if res.Head != "head" {
		t.Errorf("Head = %q, want the unchanged head", res.Head)
	}
	if len(*prompts) != 1 {
		t.Fatalf("dispatches = %d, want just the reviewer", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], "Constraint A.") {
		t.Errorf("review prompt lost the plan's global constraints:\n%s", (*prompts)[0])
	}
	if !strings.Contains((*prompts)[0], c.Paths.Package(1, 0)) {
		t.Errorf("review prompt does not name the review package:\n%s", (*prompts)[0])
	}
}

func TestReviewTaskOneFixDispatchForAllFindings(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		"SPEC: FAIL\nQUALITY: CHANGES_REQUESTED\nFINDINGS:\n- [Critical] missing progress reporting\n- [Important] magic number 100\n- [Minor] comment typo\n",
		"STATUS: DONE\nTESTS: go test ./... — pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	g := c.Git.(*worktree.FakeGitOps)
	g.Dirty = true
	spec, _ := c.Plan.Task(1)

	res, err := c.reviewTask(context.Background(), spec, taskResult{Base: "base", Head: "head"})
	if err != nil {
		t.Fatalf("reviewTask: %v", err)
	}
	if len(*prompts) != 3 {
		t.Fatalf("dispatches = %d, want review, one fixer, re-review", len(*prompts))
	}
	fix := (*prompts)[1]
	for _, want := range []string{"missing progress reporting", "magic number 100"} {
		if !strings.Contains(fix, want) {
			t.Errorf("fix dispatch missing %q:\n%s", want, fix)
		}
	}
	if len(g.Commits) != 1 {
		t.Fatalf("commits = %v, want one fix commit", g.Commits)
	}
	if !strings.Contains(g.Commits[0], "review fix (round 1)") {
		t.Errorf("fix commit subject = %q", g.Commits[0])
	}
	if res.Head == "head" {
		t.Error("result head was not advanced to the fix commit")
	}
	// The re-review reads a fresh package, not the first one.
	if !strings.Contains((*prompts)[2], c.Paths.Package(1, 1)) {
		t.Errorf("re-review reads a stale package:\n%s", (*prompts)[2])
	}
}

func TestReviewTaskRecordsMinorsWithoutBlocking(t *testing.T) {
	d, _ := scriptedDispatch(t, "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- [Minor] magic number 100\n")
	c := testController(t, d, NewFakeCommandRunner())
	spec, _ := c.Plan.Task(1)

	if _, err := c.reviewTask(context.Background(), spec, taskResult{Base: "base", Head: "head"}); err != nil {
		t.Fatalf("reviewTask: %v", err)
	}
	minors, err := c.Ledger.Minors()
	if err != nil {
		t.Fatalf("Minors: %v", err)
	}
	if len(minors) != 1 || !strings.Contains(minors[0], "magic number 100") {
		t.Errorf("Minors = %v, want the recorded minor finding", minors)
	}
}

func TestReviewTaskExhaustsFixRounds(t *testing.T) {
	failing := "SPEC: FAIL\nQUALITY: CHANGES_REQUESTED\nFINDINGS:\n- [Critical] still wrong\n"
	fixed := "STATUS: DONE\nTESTS: pass\n"
	d, _ := scriptedDispatch(t, failing, fixed, failing, fixed, failing)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	spec, _ := c.Plan.Task(1)

	_, err := c.reviewTask(context.Background(), spec, taskResult{Base: "base", Head: "head"})
	if err == nil {
		t.Fatal("review never came back clean: want error, got nil")
	}
	if !strings.Contains(err.Error(), "fix rounds") {
		t.Errorf("error = %v, want it to name the exhausted fix budget", err)
	}
}

// transientDispatch returns a Dispatcher whose exec fails with the given
// error for the first failCount calls, then returns out.
func transientDispatch(t *testing.T, failCount int, failErr error, out string) Dispatcher {
	t.Helper()
	calls := 0
	return Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			calls++
			if calls <= failCount {
				return "", failErr
			}
			return out, nil
		},
	}
}

func TestRunTaskRetriesTransientDispatchFailure(t *testing.T) {
	err := context.DeadlineExceeded
	d := transientDispatch(t, 2, err, "STATUS: DONE\nTESTS: go test ./... — pass\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.MaxDispatchRetries = 3
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	lines, _ := c.Ledger.Tail(10)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "retry 1/3") || !strings.Contains(joined, "retry 2/3") {
		t.Errorf("ledger missing retry notes:\n%s", joined)
	}
}

func TestRunTaskDoesNotRetryPermanentFailure(t *testing.T) {
	d := transientDispatch(t, 1, &provider.ProviderError{StatusCode: 400}, "STATUS: DONE\nTESTS: pass\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.MaxDispatchRetries = 3
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	spec, _ := c.Plan.Task(1)
	_, err := c.runTask(context.Background(), spec)
	if err == nil {
		t.Fatal("want permanent error, got nil")
	}
	lines, _ := c.Ledger.Tail(10)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "retry") {
		t.Errorf("permanent failure must not be retried:\n%s", joined)
	}
}

func TestRunTaskAbortsOnCtxDoneDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			cancel()
			return "", context.DeadlineExceeded
		},
	}
	c := testController(t, d, NewFakeCommandRunner())
	c.MaxDispatchRetries = 3
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}

	spec, _ := c.Plan.Task(1)
	_, err := c.runTask(ctx, spec)
	// The original dispatch error is returned, but because the ctx is done
	// no retry is attempted.
	if errors.Is(err, context.Canceled) {
		t.Fatal("should not retry after ctx is done")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	lines, _ := c.Ledger.Tail(10)
	if strings.Join(lines, "\n") != "" {
		t.Errorf("no retry should be recorded, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRunTaskRetriesUnparseableReport(t *testing.T) {
	// First call succeeds but returns unparseable output; the dispatcher
	// returns it as a parse error, which is not transient, but the plan
	// explicitly says unparseable reports should be retried. Our
	// dispatcher returns parse errors as non-transient, so simulate an
	// unparseable output that is transient by making the exec fail twice
	// with a transient error after the first attempt produced garbage.
	calls := 0
	d2 := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			calls++
			if calls == 1 {
				return "garbage output", nil
			}
			if calls <= 3 {
				return "", context.DeadlineExceeded
			}
			return "STATUS: DONE\nTESTS: pass\n", nil
		},
	}
	c := testController(t, d2, NewFakeCommandRunner())
	c.MaxDispatchRetries = 3
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if calls < 4 {
		t.Errorf("calls = %d, want retries after unparseable report", calls)
	}
}

func TestRunTaskPreviousAttemptNoteOnDirtyTreeWithReport(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n")
	c := testController(t, d, NewFakeCommandRunner())
	// Dirty tree and an existing report file signal a resumed task.
	c.Git.(*worktree.FakeGitOps).Dirty = true
	_ = os.WriteFile(c.Paths.Report(1), []byte("partial report"), 0o644)

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	brief, _ := os.ReadFile(c.Paths.Brief(1))
	if !strings.Contains(string(brief), "Previous attempt") {
		t.Errorf("brief missing previous-attempt note:\n%s", brief)
	}
}

func TestRunTaskNoPreviousAttemptNoteWithoutReport(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n")
	c := testController(t, d, NewFakeCommandRunner())
	// Dirty tree alone (no existing report) means this is the first
	// attempt, not a resumed one.
	c.Git.(*worktree.FakeGitOps).Dirty = true

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	brief, _ := os.ReadFile(c.Paths.Brief(1))
	if strings.Contains(string(brief), "Previous attempt") {
		t.Errorf("first attempt should not add previous-attempt note:\n%s", brief)
	}
}

func TestControllerCompletedCount(t *testing.T) {
	d := Dispatcher{}
	c := testController(t, d, NewFakeCommandRunner())
	_ = c.Ledger.MarkComplete(1, "base1", "head1")
	_ = c.Ledger.MarkComplete(3, "base3", "head3")

	n, err := c.CompletedCount()
	if err != nil {
		t.Fatalf("CompletedCount: %v", err)
	}
	if n != 2 {
		t.Errorf("CompletedCount = %d, want 2", n)
	}
}

func TestCleanupWorktreesRemovesAndPrunes(t *testing.T) {
	c := &Controller{
		RepoRoot: "/repo",
		Git:      worktree.NewFakeGitOps(),
		Worktree: worktree.Worktree{Path: "/repo/.marshal/pipeline/slug/worktrees/slug", Branch: "pipeline/slug"},
	}
	fake := c.Git.(*worktree.FakeGitOps)
	orig := c.Worktree.Path
	if err := c.CleanupWorktrees(); err != nil {
		t.Fatalf("CleanupWorktrees: %v", err)
	}
	if len(fake.Removed) != 1 || fake.Removed[0] != orig {
		t.Fatalf("Removed = %v, want [%s]", fake.Removed, orig)
	}
	if !fake.Pruned {
		t.Fatal("expected prune after remove")
	}
	if c.Worktree.Path != "" {
		t.Fatal("Worktree.Path should be cleared after cleanup")
	}
	// Idempotent: second call is a no-op.
	if err := c.CleanupWorktrees(); err != nil {
		t.Fatalf("second CleanupWorktrees: %v", err)
	}
	if len(fake.Removed) != 1 {
		t.Fatalf("second call removed again: %v", fake.Removed)
	}
}

func TestCleanupWorktreesKeepsDirtyWorktree(t *testing.T) {
	fake := worktree.NewFakeGitOps()
	fake.RemoveErr = errors.New("fatal: 'x' contains modified or untracked files")
	c := &Controller{
		RepoRoot: "/repo",
		Git:      fake,
		Worktree: worktree.Worktree{Path: "/wt", Branch: "pipeline/slug"},
	}
	if err := c.CleanupWorktrees(); err == nil {
		t.Fatal("expected the git refusal to propagate")
	}
	if fake.Pruned {
		t.Fatal("prune must not run when remove fails")
	}
	if c.Worktree.Path != "/wt" {
		t.Fatal("dirty worktree path must be kept for resume")
	}
}

func TestCleanupWorktreesNoWorktree(t *testing.T) {
	c := &Controller{Git: worktree.NewFakeGitOps()}
	if err := c.CleanupWorktrees(); err != nil {
		t.Fatalf("no-worktree cleanup should be a no-op, got %v", err)
	}
}
