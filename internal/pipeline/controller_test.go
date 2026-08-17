package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/worktree"
)

// TestRunTaskDeterministicNilRunner verifies that a nil Verifier.Runner
// returns an error instead of panicking.
func TestRunTaskDeterministicNilRunner(t *testing.T) {
	c := &Controller{
		Verifier: Verifier{Runner: nil},
	}
	_, err := c.runTaskDeterministic(context.Background(), TaskSpec{N: 1}, &TaskIR{}, "", t.TempDir(), "")
	if err == nil {
		t.Fatal("expected error for nil runner, got nil")
	}
	if !strings.Contains(err.Error(), "no runner") {
		t.Fatalf("expected 'no runner' error, got: %v", err)
	}
}

// reportPathRe matches the report-contract line in an implementer or fixer
// prompt, capturing the absolute path the subagent is told to write to.
var reportPathRe = regexp.MustCompile(`Write your full report to (\S+)`)

// scriptedDispatch returns a Dispatcher whose exec pops one canned output
// per call, and a pointer to the prompts it received. A real implementer or
// fixer writes its report to the path named in the prompt; the scripted
// dispatcher mirrors that so the reviewer's input preflight sees the file.
// @run/... artifact paths are resolved against ExecCtx.ArtifactRoot, which
// the controller sets to the run directory (Critical 1).
// scriptedDispatch returns a Dispatcher whose exec pops one canned output
// per call, and a pointer to the prompts it received. A real implementer or
// fixer writes its report to the path named in the prompt; the scripted
// dispatcher mirrors that so the reviewer's input preflight sees the file.
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
			if m := reportPathRe.FindStringSubmatch(prompt); m != nil {
				if err := os.WriteFile(m[1], []byte(out), 0o644); err != nil {
					return "", err
				}
			}
			return out, nil
		},
	}
	return d, prompts
}

// testController builds a Controller over fakes with a two-task plan. The
// controller's Run() sets Dispatcher.ExecCtx (Critical 1) to the run dir,
// so prompts now reference @run/... artifact paths. This wraps the given
// dispatcher's exec so @run/ resolves to the run directory, mirroring what
// the production RegistryFactory does for a real child registry.
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
	g.AbbrevRef = "main"
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
	if d.exec != nil {
		inner := d.exec
		c.Dispatch.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			// Resolve @run/... artifact references to the run directory,
			// as the production @run alias does.
			if c.Paths.Dir != "" {
				prompt = strings.Replace(prompt, "@run/", c.Paths.Dir+"/", -1)
			}
			return inner(ctx, role, scope, prompt)
		}
	}
	return c
}

// writeBrief writes task n's brief file, which the reviewer's input
// preflight requires. runTask normally writes it; tests that call
// reviewTask directly must create it themselves.
func writeBrief(t *testing.T, c *Controller, n int) {
	t.Helper()
	if err := os.WriteFile(c.Paths.Brief(n), []byte("brief"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
}

// writeReport writes task n's report file, which the reviewer's input
// preflight requires. The implementer normally writes it; tests that call
// reviewTask directly must create it themselves.
func writeReport(t *testing.T, c *Controller, n int) {
	t.Helper()
	if err := os.WriteFile(c.Paths.Report(n), []byte("report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
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

func (f *flakyRunner) Run(ctx context.Context, dir string, argv []string) (string, error) {
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
	writeBrief(t, c, 1)
	writeReport(t, c, 1)

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
	writeBrief(t, c, 1)
	writeReport(t, c, 1)

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
	writeBrief(t, c, 1)
	writeReport(t, c, 1)

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
	writeBrief(t, c, 1)
	writeReport(t, c, 1)

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

// recordingObserver captures every event a run emits so tests can assert on
// payloads without a TUI.
type recordingObserver struct{ events []Event }

func (r *recordingObserver) Event(ev Event) { r.events = append(r.events, ev) }

func (r *recordingObserver) payloads() []any {
	var out []any
	for _, ev := range r.events {
		if ev.Payload != nil {
			out = append(out, ev.Payload)
		}
	}
	return out
}

func TestEmitPayloadCarriesDataThrough(t *testing.T) {
	obs := &recordingObserver{}
	c := &Controller{Observer: obs, Plan: &Plan{Tasks: []TaskSpec{{N: 1}}}, MaxFixRounds: 3}

	c.emitPayload(1, 1, PhaseFixing, "go test ./...", VerifyFailedPayload{
		Command: "go test ./...", Output: "FAIL", Round: 1, MaxRounds: 3,
	})

	if len(obs.events) != 1 {
		t.Fatalf("got %d events, want 1", len(obs.events))
	}
	ev := obs.events[0]
	if ev.TaskN != 1 || ev.Phase != PhaseFixing {
		t.Errorf("event scalars = %d/%q, want 1/%q", ev.TaskN, ev.Phase, PhaseFixing)
	}
	p, ok := ev.Payload.(VerifyFailedPayload)
	if !ok {
		t.Fatalf("Payload = %T, want VerifyFailedPayload", ev.Payload)
	}
	if p.Output != "FAIL" {
		t.Errorf("Output = %q, want FAIL", p.Output)
	}
}

func TestEmitStillWorksWithoutPayload(t *testing.T) {
	obs := &recordingObserver{}
	c := &Controller{Observer: obs, Plan: &Plan{Tasks: []TaskSpec{{N: 1}}}}
	c.emit(1, 0, PhaseImplementing, "")
	if len(obs.events) != 1 {
		t.Fatalf("got %d events, want 1", len(obs.events))
	}
	if obs.events[0].Payload != nil {
		t.Errorf("bare emit must leave Payload nil, got %#v", obs.events[0].Payload)
	}
}

func TestNilObserverToleratesPayloadEmit(t *testing.T) {
	c := &Controller{Plan: &Plan{Tasks: []TaskSpec{{N: 1}}}}
	c.emitPayload(1, 0, PhaseDone, "", CommitPayload{SHA: "abc1234"}) // must not panic
}

// newTestControllerWithFailingGate builds a controller over the shared
// fake-git harness whose single gate command fails its first run (with out)
// and passes every later run. obs receives the emitted events.
func newTestControllerWithFailingGate(t *testing.T, obs Observer, cmd, out string) *Controller {
	t.Helper()
	d, _ := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: go test ./... — pass\n",           // task 1 implementer
		"STATUS: DONE\nTESTS: go test ./... — pass after fix\n", // task 1 gate fixer
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",    // task 1 review
		"STATUS: DONE\nTESTS: go test ./... — pass\n",           // task 2 implementer
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",    // task 2 review
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",    // branch review
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Observer = obs
	c.Verifier = Verifier{Build: cmd, Runner: &flakyRunner{failFirst: true, failOut: out}}
	return c
}

// newTestControllerWithSkippedGate builds a controller over the shared
// fake-git harness whose gate has no build or test command, so every gate
// run is skipped. obs receives the emitted events.
func newTestControllerWithSkippedGate(t *testing.T, obs Observer) *Controller {
	t.Helper()
	d, _ := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"STATUS: DONE\nTESTS: none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Observer = obs
	c.Verifier = Verifier{Runner: NewFakeCommandRunner()} // no commands -> skipped
	return c
}

func TestRunEmitsVerifyFailurePayloadWithOutput(t *testing.T) {
	// A gate that fails once then passes must emit exactly one
	// VerifyFailedPayload carrying the failing command's real output.
	obs := &recordingObserver{}
	c := newTestControllerWithFailingGate(t, obs, "go test ./...", "--- FAIL: TestFoo\n  foo_test.go:9: boom")

	_ = c.Run(t.Context())

	var found *VerifyFailedPayload
	for _, p := range obs.payloads() {
		if v, ok := p.(VerifyFailedPayload); ok {
			found = &v
			break
		}
	}
	if found == nil {
		t.Fatal("no VerifyFailedPayload emitted; the gate failure is invisible to the UI")
	}
	if !strings.Contains(found.Output, "foo_test.go:9: boom") {
		t.Errorf("payload Output = %q, want the verifier's real output", found.Output)
	}
	if found.Command != "go test ./..." {
		t.Errorf("Command = %q, want the failing command", found.Command)
	}
}

func TestRunEmitsGateSkippedPayload(t *testing.T) {
	obs := &recordingObserver{}
	c := newTestControllerWithSkippedGate(t, obs)

	_ = c.Run(t.Context())

	for _, p := range obs.payloads() {
		if _, ok := p.(GateSkippedPayload); ok {
			return
		}
	}
	t.Fatal("no GateSkippedPayload emitted; a skipped gate is indistinguishable from a pass")
}

func TestOpenGateRecordsTaskTitleAndReport(t *testing.T) {
	c := &Controller{Plan: &Plan{Tasks: []TaskSpec{{N: 1, Title: "Add the retry helper"}}}}

	c.openGateWithContext(1, "Reuse the existing backoff?", ImplementerReport{
		Status:   StatusNeedsContext,
		Question: "Reuse the existing backoff?",
		Raw:      "STATUS: NEEDS_CONTEXT\nQUESTION: Reuse the existing backoff?",
	})

	if got := c.Question(); got != "Reuse the existing backoff?" {
		t.Errorf("Question() = %q", got)
	}
	title, report := c.QuestionContext()
	if title != "Add the retry helper" {
		t.Errorf("task title = %q, want the plan task's title", title)
	}
	if !strings.Contains(report, "NEEDS_CONTEXT") {
		t.Errorf("report = %q, want the implementer's raw report", report)
	}
}

func TestOpenGateContextIsEmptyForBranchLevelGates(t *testing.T) {
	c := &Controller{Plan: &Plan{Tasks: []TaskSpec{{N: 1, Title: "Only task"}}}}
	c.openGateWithContext(0, "branch-level question", ImplementerReport{Raw: "raw"})
	title, _ := c.QuestionContext()
	if title != "" {
		t.Errorf("task 0 is not a plan task; title = %q, want empty", title)
	}
}

func TestRunWritesCheckpointOnTaskStart(t *testing.T) {
	d, _ := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rs := NewRunStore(c.Paths)
	last, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !ok {
		t.Fatal("no checkpoint written")
	}
	if last.Phase != PhaseDone && last.Phase != "run_finished" {
		t.Errorf("last checkpoint phase = %q, want done or run_finished", last.Phase)
	}
}

func TestRunRecoversCommittedTaskWithoutRedispatch(t *testing.T) {
	d, prompts := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	g := c.Git.(*worktree.FakeGitOps)
	g.Dirty = true

	// Simulate a prior run that committed task 1 but crashed before
	// writing the completion ledger line.
	c.RunStore = NewRunStore(c.Paths)
	_ = c.RunStore.CreateManifest(Manifest{
		RunID: "crashed-run", PlanPath: c.Plan.Path,
		RepoRoot: c.RepoRoot, PipelineBranch: "pipeline/test-plan",
	})
	// Task 1 was committed: record the checkpoint and the commit.
	_ = c.RunStore.AppendCheckpoint(Checkpoint{Seq: 1, RunID: "crashed-run", Phase: "task_completed", TaskN: 1, BaseSHA: "111111", HeadSHA: "aaaaaaa"})
	g.Refs["HEAD"] = "aaaaaaa"
	g.Heads[c.workDir()] = "aaaaaaa"

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Task 1 should NOT have been redispatched: only task 2 + branch review.
	if len(*prompts) != 3 {
		t.Fatalf("dispatches = %d, want 3 (task 2 impl + task 2 review + branch review), got prompts:\n%v", len(*prompts), *prompts)
	}
}

func TestRunRestoresGateAfterCrash(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: NEEDS_CONTEXT\nQUESTION: which level?\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.RunStore = NewRunStore(c.Paths)
	_ = c.RunStore.CreateManifest(Manifest{RunID: "crashed-run", PlanPath: c.Plan.Path, RepoRoot: c.RepoRoot, PipelineBranch: "pipeline/test-plan"})
	_ = c.RunStore.AppendCheckpoint(Checkpoint{Seq: 1, RunID: "crashed-run", Phase: PhaseBlocked, TaskN: 1, GateQuestion: "which level?"})

	err := c.Run(context.Background())
	if !errors.Is(err, ErrHumanGateRequired) {
		t.Fatalf("Run = %v, want ErrHumanGateRequired", err)
	}
	if c.Question() != "which level?" {
		t.Errorf("Question() = %q, want %q", c.Question(), "which level?")
	}
}

func TestReviewBlockedInputsStopsWithoutFixer(t *testing.T) {
	// Reviewer reports inputs are blocked.
	d, prompts := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: pass\n", // task 1 implementer
		"INPUTS: BLOCKED\nINPUT_ERROR: could not read @run/task-1-brief.md\nSPEC: FAIL\nQUALITY: CHANGES_REQUESTED\nFINDINGS:\n- [Critical] cannot review without inputs\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	// The preflight requires the brief and report to exist as regular files
	// before the reviewer is dispatched. The scripted implementer returns a
	// canned string without writing the report, so create both here to let
	// the reviewer run and report the blocked inputs.
	if err := os.WriteFile(c.Paths.Brief(1), []byte("brief"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := os.WriteFile(c.Paths.Report(1), []byte("report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	err := c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail when reviewer inputs are blocked")
	}
	if !strings.Contains(err.Error(), "inaccessible") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should mention inaccessible/blocked inputs: %v", err)
	}
	// Only 2 dispatches: implementer + reviewer. No fixer.
	if len(*prompts) != 2 {
		t.Fatalf("dispatches = %d, want 2 (impl + review, no fixer for blocked inputs)", len(*prompts))
	}
}

func TestPreflightCatchesMissingReport(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	// Run task 1 to completion, then delete the report file to simulate
	// a crash that lost it.
	spec, _ := c.Plan.Task(1)
	_, err := c.runTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	os.Remove(c.Paths.Report(1))

	err = c.preflightReviewInputs(spec, c.Paths.Package(1, 0), c.Paths.Package(1, 0)+"-verdict.md")
	if err == nil {
		t.Fatal("preflight should catch missing report file")
	}
}

func TestRunStopsOnTokenBudgetExhaustion(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.MaxTokensCfg = 100 // very low budget
	c.UsageTokens = 0

	// Wire OnTokens to increment UsageTokens.
	c.Dispatch.OnTokens = func(n int) {
		c.UsageTokens += n
	}

	// Simulate the first dispatch consuming all the budget. Mirror the
	// scripted dispatcher's report-writing so the reviewer's input preflight
	// sees the file if the run were to proceed past the budget check.
	c.Dispatch.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		c.UsageTokens += 150 // exceeds budget
		if m := reportPathRe.FindStringSubmatch(prompt); m != nil {
			p := m[1]
			if c.Paths.Dir != "" {
				p = strings.Replace(p, "@run/", c.Paths.Dir+"/", 1)
			}
			if err := os.WriteFile(p, []byte("STATUS: DONE\nTESTS: pass\n"), 0o644); err != nil {
				return "", err
			}
		}
		return "STATUS: DONE\nTESTS: pass\n", nil
	}

	err := c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should stop when budget is exhausted")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error should mention budget: %v", err)
	}
}

// TestRunTokenBudgetEnforcedAutomatically verifies that Run() wires the
// Dispatcher.OnTokens callback itself, so the token budget is enforced
// without the caller manually wiring it.
func TestRunTokenBudgetEnforcedAutomatically(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.MaxTokensCfg = 100

	// Override exec to consume tokens beyond the budget through the
	// OnTokens callback, mirroring how the real dispatch path reports
	// usage. If Run() does not wire OnTokens, UsageTokens stays 0 and the
	// run completes without a budget error.
	c.Dispatch.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		if c.Dispatch.OnTokens != nil {
			c.Dispatch.OnTokens(150) // exceeds the 100-token budget
		}
		if m := reportPathRe.FindStringSubmatch(prompt); m != nil {
			p := m[1]
			if c.Paths.Dir != "" {
				p = strings.Replace(p, "@run/", c.Paths.Dir+"/", 1)
			}
			if err := os.WriteFile(p, []byte("STATUS: DONE\nTESTS: pass\n"), 0o644); err != nil {
				return "", err
			}
		}
		return "STATUS: DONE\nTESTS: pass\n", nil
	}

	err := c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should stop when budget is exhausted")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error should mention budget: %v", err)
	}
}

// TestRunChainsPriorOnTokens verifies that Run() preserves a pre-existing
// Dispatcher.OnTokens callback (e.g. the app's TUI token display) by
// chaining it rather than clobbering it, while still enforcing the budget.
func TestRunChainsPriorOnTokens(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.MaxTokensCfg = 100
	// The budget cancel may land on a dispatch that fails report parsing,
	// which would otherwise trigger a real 5s retry backoff. Elide it.
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	// A pre-existing observer installed before Run, as the app layer does.
	observed := 0
	c.Dispatch.OnTokens = func(n int) { observed += n }

	c.Dispatch.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		if c.Dispatch.OnTokens != nil {
			c.Dispatch.OnTokens(40) // under budget on first dispatch
		}
		if m := reportPathRe.FindStringSubmatch(prompt); m != nil {
			p := m[1]
			if c.Paths.Dir != "" {
				p = strings.Replace(p, "@run/", c.Paths.Dir+"/", 1)
			}
			if err := os.WriteFile(p, []byte("STATUS: DONE\nTESTS: pass\n"), 0o644); err != nil {
				return "", err
			}
		}
		return "STATUS: DONE\nTESTS: pass\n", nil
	}

	_ = c.Run(context.Background())

	if observed == 0 {
		t.Fatal("pre-existing OnTokens callback was never invoked through the chain")
	}
	if c.UsageTokens == 0 {
		t.Fatal("Run did not track token usage via the chained callback")
	}
}

// TestCheckpointLogsError verifies that a checkpoint write failure is
// logged via slog.Error rather than silently discarded.
func TestCheckpointLogsError(t *testing.T) {
	// Capture slog output.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	old := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(old)

	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	// The checkpoint failure leaves the scripted dispatcher exhausted on a
	// later dispatch, which would otherwise trigger a real dispatch-retry
	// backoff (5s). This test only asserts the checkpoint error is logged,
	// so elide the backoff to keep it fast.
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	// Inject a RunStore that fails on AppendCheckpoint.
	c.RunStore = &RunStore{paths: c.Paths}
	// Write a manifest so Run() doesn't try to create one.
	_ = c.RunStore.CreateManifest(Manifest{
		RunID: "test-run", PlanPath: c.Plan.Path, RepoRoot: c.RepoRoot,
		PipelineBranch: "pipeline/test-plan",
	})

	// Corrupt the checkpoint path so AppendCheckpoint fails.
	// Create a directory where the checkpoint file should go.
	os.MkdirAll(c.RunStore.checkpointPath(), 0o755)

	err := c.Run(context.Background())
	// The run may succeed or fail depending on other factors, but the
	// checkpoint error must be logged.
	_ = err

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to write checkpoint") {
		t.Fatalf("expected 'failed to write checkpoint' in log output, got: %s", logOutput)
	}
}

// TestRecoveryPrunesLostCommits verifies that a task marked complete in the
// ledger but whose commit no longer exists is pruned from the done set so it
// is re-run rather than skipped.
func TestRecoveryPrunesLostCommits(t *testing.T) {
	d, prompts := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	g := c.Git.(*worktree.FakeGitOps)
	g.Dirty = true

	// Mark task 1 as complete in the ledger with a commit that doesn't exist.
	_ = c.Ledger.MarkComplete(1, "nonexist1", "nonexist2")

	done, _ := c.Ledger.CompletedTasks()
	if !done[1] {
		t.Fatal("task 1 should be complete before Run")
	}

	// Simulate the commit being lost: LogOneline errors (commit missing)
	// and the head is not the branch tip.
	g.LogErr = errors.New("unknown revision")
	g.Heads[c.Paths.WorktreesDir()+"/pipeline-test-plan"] = "someotherhead"

	err := c.Run(context.Background())
	_ = err

	// The pruned task must be re-dispatched (not skipped). Without pruning,
	// task 1 would be skipped and no implementer dispatch would occur.
	if len(*prompts) == 0 {
		t.Error("task 1 should have been re-dispatched after its commit was pruned")
	}
}

// TestRecoveryKeepsTipCommitWithShortSHA verifies that a ledger commit whose
// LogOneline errors is still kept when its short SHA matches the branch tip.
// The ledger stores short SHAs while HEAD is full-length, so the comparison
// must normalize to the short form or a tip commit is wrongly pruned.
func TestRecoveryKeepsTipCommitWithShortSHA(t *testing.T) {
	d, prompts := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	g := c.Git.(*worktree.FakeGitOps)
	g.Dirty = true

	// The branch tip is a full SHA; the ledger records its 7-char short form.
	tip := "abcdef0123456789abcdef0123456789abcdef01"
	_ = c.Ledger.MarkComplete(1, "0000000", short(tip))

	// Pre-register the run's worktree so EnsureWorktree reuses it (rather
	// than creating one pointed at main). Its HEAD is the recorded tip.
	wtPath := c.Paths.WorktreesDir() + "/pipeline-" + c.Plan.Slug
	g.Branches["pipeline/"+c.Plan.Slug] = true
	g.Worktrees = append(g.Worktrees, wtPath)
	g.WorktreeBranches[wtPath] = "pipeline/" + c.Plan.Slug
	g.Refs["pipeline/"+c.Plan.Slug] = tip
	g.Heads[wtPath] = tip

	// LogOneline errors (as fakes without a populated log do), but HEAD
	// equals the recorded commit — it must NOT be pruned.
	g.LogErr = errors.New("unknown revision")

	err := c.Run(context.Background())
	_ = err

	// Task 1 was already complete and its commit is the tip: it must not be
	// re-dispatched. Pruning it would cause an implementer dispatch for
	// task 1. Task 2 legitimately dispatches regardless, so only fail on
	// prompts that name task 1.
	for _, p := range *prompts {
		if strings.Contains(p, "Task 1:") {
			t.Fatalf("task 1 was re-dispatched; tip commit should have been kept (prompt: %.60s)", p)
		}
	}
}

// TestRunFailsWhenAnotherRunHoldsTheLock asserts a second Run on the same
// paths fails when a live lock exists (a different run owns it).
func TestRunFailsWhenAnotherRunHoldsTheLock(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.RunStore = NewRunStore(c.Paths)
	// Pre-create a manifest + lock owned by a different run, as a live
	// concurrent process would.
	if err := c.RunStore.CreateManifest(Manifest{
		RunID: "other-run", PlanPath: c.Plan.Path, RepoRoot: c.RepoRoot, PipelineBranch: "pipeline/test-plan",
	}); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	if err := c.RunStore.AcquireLock(RunLock{RunID: "other-run", PID: 99999, Host: "other-host", AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	err := c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail when another run holds the lock")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should mention the lock: %v", err)
	}
	// The other run's lock must still be in place.
	if l, ok, _ := c.RunStore.Lock(); !ok || l.RunID != "other-run" {
		t.Errorf("lock = %+v (ok=%v), want the other run's lock preserved", l, ok)
	}
}

// TestRunTakesOverStaleLockFromDifferentRun asserts a Run takes over a lock
// that belongs to a different (stale) run and proceeds.
func TestRunTakesOverStaleLockFromDifferentRun(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.RunStore = NewRunStore(c.Paths)
	// Manifest claims this controller's run; the lock is stale from a
	// different (older) run, so Run must take it over.
	if err := c.RunStore.CreateManifest(Manifest{
		RunID: "current-run", PlanPath: c.Plan.Path, RepoRoot: c.RepoRoot, PipelineBranch: "pipeline/test-plan",
	}); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	if err := c.RunStore.AcquireLock(RunLock{RunID: "stale-run", PID: 11111, Host: "old-host", AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The stale lock was taken over and released on completion.
	if _, ok, _ := c.RunStore.Lock(); ok {
		t.Error("lock should be released after a successful run")
	}
	if len(*prompts) != 5 {
		t.Fatalf("dispatches = %d, want the full run (2 tasks + branch review)", len(*prompts))
	}
}

func TestRunAdaptiveFullyDeterministicTaskZeroDispatches(t *testing.T) {
	// Build a plan with one task that has a marshal.file block creating a
	// new file, then a marshal.assert file.exists on that file.
	dir := t.TempDir()
	planContent := "# Det Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Create file\n\n" +
		"```marshal.file path=\"output.txt\"\nhello\n```\n\n" +
		"```marshal.assert\nkind = \"file.exists\"\nfile = \"output.txt\"\n```\n"
	planPath := filepath.Join(dir, "det-plan.md")
	os.WriteFile(planPath, []byte(planContent), 0o644)

	// The dispatch should never be called.
	d, prompts := scriptedDispatch(t)
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	g.Dirty = true
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyAdaptive,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	// Compile the plan into the IR.
	ir, diags, err := CompilePlan(c.Plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	c.PlanIR = ir

	// Preflight: the file doesn't exist yet, so the file op is executable
	// (create new), and the assert is executable (it will check after).
	preflightDiags := preflightOps(c.workDir(), ir.Tasks[0].Operations)
	if len(preflightDiags) > 0 {
		t.Fatalf("preflight diagnostics: %v", preflightDiags)
	}

	spec, _ := c.Plan.Task(1)
	res, err := c.runTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if len(*prompts) != 0 {
		t.Fatalf("dispatches = %d, want 0 for fully deterministic task", len(*prompts))
	}
	if res.Head == "" {
		t.Fatal("expected a commit")
	}
	// Verify the file was created in the worktree.
	if _, err := os.Stat(filepath.Join(c.workDir(), "output.txt")); err != nil {
		t.Errorf("output.txt not created: %v", err)
	}
}

func TestRunStrictRejectsAgentOp(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Strict Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Has agent op\n\n" +
		"```marshal.agent\nallowed = true\nscope = [\"internal/foo\"]\nreason = \"needs model\"\n```\n"
	planPath := filepath.Join(dir, "strict-plan.md")
	os.WriteFile(planPath, []byte(planContent), 0o644)

	d, _ := scriptedDispatch(t)
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyStrict,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	ir, _, err := CompilePlan(c.Plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	c.PlanIR = ir

	// The controller should reject the plan before running.
	spec, _ := c.Plan.Task(1)
	_, err = c.runTask(context.Background(), spec)
	if err == nil {
		t.Fatal("runTask should fail in strict mode with an AgentOp")
	}
	if !strings.Contains(err.Error(), "strict") {
		t.Errorf("error should mention strict: %v", err)
	}
}

func TestRunAdaptiveLegacyPlanBehavesLikeAgent(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"STATUS: DONE\nTESTS: pass\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
		"SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n",
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.Strategy = StrategyAdaptive

	// No PlanIR set — legacy plan.
	err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Should have dispatched implementer + reviewer for each task + branch review.
	if len(*prompts) == 0 {
		t.Fatal("expected dispatches for legacy plan under adaptive")
	}
}

func TestReviewSkippedForDeterministicTaskInAdaptive(t *testing.T) {
	d, prompts := scriptedDispatch(t, "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Strategy = StrategyAdaptive
	writeBrief(t, c, 1)

	spec, _ := c.Plan.Task(1)
	res := taskResult{
		Base:     "base123",
		Head:     "head456",
		ExecType: ExecDeterministic,
	}
	_, err := c.reviewTask(context.Background(), spec, res)
	if err != nil {
		t.Fatalf("reviewTask: %v", err)
	}
	// No reviewer should have been dispatched.
	if len(*prompts) != 0 {
		t.Fatalf("dispatches = %d, want 0 (review skipped for deterministic task)", len(*prompts))
	}
}

func TestRunStrictFailsBeforeWorktreeOnBlockedTask(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Strict Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Blocked\n\n" +
		"```marshal.patch file=\"nonexistent.go\"\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n```\n"
	planPath := filepath.Join(dir, "strict-blocked.md")
	os.WriteFile(planPath, []byte(planContent), 0o644)

	d, _ := scriptedDispatch(t)
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyStrict,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	err = c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail for strict plan with blocked operations")
	}
	if !strings.Contains(err.Error(), "strict") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should mention strict or blocked: %v", err)
	}
	if len(g.Added) != 0 {
		t.Errorf("strict blocked plan must not create a worktree, got %v", g.Added)
	}
}

func TestRunStrictFailsBeforeWorktreeOnProseAndAgentTasks(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Strict Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Resolve\n\n" +
		"```marshal.agent\nscope = [\"internal/app\"]\nreason = \"needs design judgment\"\n```\n\n" +
		"## Task 2: Legacy\n\nProse only.\n"
	planPath := filepath.Join(dir, "strict-prose.md")
	os.WriteFile(planPath, []byte(planContent), 0o644)

	d, _ := scriptedDispatch(t)
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyStrict,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	err = c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail for strict plan with agent and prose-only tasks")
	}
	if len(g.Added) != 0 {
		t.Errorf("strict plan with unresolved work must not create a worktree, got %v", g.Added)
	}
}

func TestReviewRunsForAgentFallbackTaskInAdaptive(t *testing.T) {
	d, prompts := scriptedDispatch(t, "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Strategy = StrategyAdaptive
	writeBrief(t, c, 1)
	writeReport(t, c, 1)

	spec, _ := c.Plan.Task(1)
	res := taskResult{
		Base:     "base123",
		Head:     "head456",
		ExecType: ExecMixed,
	}
	_, err := c.reviewTask(context.Background(), spec, res)
	if err != nil {
		t.Fatalf("reviewTask: %v", err)
	}
	if len(*prompts) != 1 {
		t.Fatalf("dispatches = %d, want 1 (review runs for agent fallback task)", len(*prompts))
	}
}

// A brief that exists but sits outside the artifact root is unreachable:
// the reviewer addresses its inputs only as @run/<basename>. os.Stat alone
// cannot see this, which is how a run reached the reviewer and paid for a
// dispatch that could only answer INPUTS: BLOCKED.
func TestPreflightReviewInputsRejectsInputsOutsideArtifactRoot(t *testing.T) {
	d, _ := scriptedDispatch(t, "STATUS: DONE\nTESTS: pass\n", "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	spec, _ := c.Plan.Task(1)
	if _, err := c.runTask(context.Background(), spec); err != nil {
		t.Fatalf("runTask: %v", err)
	}

	pkg := c.Paths.Package(1, 0)
	verdict := pkg + "-verdict.md"
	if err := os.WriteFile(pkg, []byte("# review package\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// With the artifact root matching the run directory, preflight passes.
	c.Dispatch.ExecCtx = ExecutionContext{ArtifactRoot: c.Paths.Dir, ArtifactAlias: "@run"}
	if err := c.preflightReviewInputs(spec, pkg, verdict); err != nil {
		t.Fatalf("preflight with matching artifact root: %v", err)
	}

	// Point the artifact root somewhere else: the files still exist, but
	// the reviewer can no longer address them.
	c.Dispatch.ExecCtx = ExecutionContext{ArtifactRoot: t.TempDir(), ArtifactAlias: "@run"}
	err := c.preflightReviewInputs(spec, pkg, verdict)
	if err == nil {
		t.Fatal("preflight should reject inputs outside the artifact root")
	}
	if !strings.Contains(err.Error(), "outside the artifact root") {
		t.Fatalf("error = %v, want it to name the artifact root", err)
	}
}

// TestAdaptiveFallbackRejectsEmptyScope asserts that an adaptive task
// whose only operations are an AgentOp without a declared scope cannot
// dispatch a fallback that would otherwise rewrite the worktree at
// large. The task fails with an explicit error before any dispatcher
// call so the controller never opens a registry factory for an
// unbounded scope.
func TestAdaptiveFallbackRejectsEmptyScope(t *testing.T) {
	dir := t.TempDir()
	// An AgentOp with no `scope` line compiles to AgentOp whose
	// AllowedFiles is the empty slice.
	planContent := "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: No scope\n\n" +
		"```marshal.agent\nallowed = true\nreason = \"no scope declared\"\n```\n"
	planPath := filepath.Join(dir, "no-scope.md")
	if err := os.WriteFile(planPath, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	d, prompts := scriptedDispatch(t)
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	g.AbbrevRef = "main"
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyAdaptive,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	err = c.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail for an adaptive task whose only fallback is unscoped")
	}
	if !strings.Contains(err.Error(), "no marshal.agent scope") {
		t.Errorf("error should mention the missing scope: %v", err)
	}
	if len(*prompts) != 0 {
		t.Errorf("expected zero dispatcher calls; got %d (the controller must not dispatch an unbounded fallback)", len(*prompts))
	}
}

// TestAdaptiveFallbackSetsAllowedFilesOnDispatcher asserts that an
// adaptive task with a declared scope stashes the scope on the
// dispatcher before the implementer call so the registry factory can
// narrow file.write_patch. The scripted dispatcher records the value
// the controller stashed on the session.
func TestAdaptiveFallbackSetsAllowedFilesOnDispatcher(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Scoped fallback\n\n" +
		"```marshal.agent\nallowed = true\nscope = [\"internal/foo\"]\nreason = \"scoped\"\n```\n"
	planPath := filepath.Join(dir, "scoped.md")
	if err := os.WriteFile(planPath, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	st := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	var seen []string
	d := Dispatcher{State: st}
	d.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		seen = st.SDDFallbackAllowedFiles()
		return "STATUS: DONE\nTESTS: ok\n", nil
	}
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	g.Dirty = true
	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: dir,
		Git:      g,
		Dispatch: d,
		Verifier: Verifier{Runner: NewFakeCommandRunner()},
		Strategy: StrategyAdaptive,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := c.Inspect(); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	spec, _ := c.Plan.Task(1)
	res, err := c.runTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.ExecType != ExecMixed {
		t.Errorf("ExecType = %q, want %q (agent fallback should set Mixed)", res.ExecType, ExecMixed)
	}
	if len(seen) != 1 || seen[0] != "internal/foo" {
		t.Fatalf("FallbackAllowedFiles at dispatch = %v, want [internal/foo]", seen)
	}
	if got := st.SDDFallbackAllowedFiles(); got != nil {
		t.Errorf("SDDFallbackAllowedFiles must be cleared after dispatch; got %v", got)
	}
}

// TestAdaptiveFallbackScopePersistsThroughGateFixer asserts that the
// marshal.agent scope stashed for the fallback agent is still active when
// the verification gate fails and a fixer is dispatched.
func TestAdaptiveFallbackScopePersistsThroughGateFixer(t *testing.T) {
	dir := t.TempDir()
	// A blocked patch forces needsAgent=true; the fallback must be scoped.
	planContent := "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: Scoped fallback with fixer\n\n" +
		"```marshal.patch file=\"internal/foo/foo.go\"\n" +
		"<<<<<<< SEARCH\n" +
		"this text does not exist\n" +
		"=======\n" +
		"replacement\n" +
		">>>>>>> REPLACE\n" +
		"```\n\n" +
		"```marshal.agent\nallowed = true\nscope = [\"internal/foo\"]\nreason = \"scoped\"\n```\n"
	planPath := filepath.Join(dir, "scoped-fixer.md")
	if err := os.WriteFile(planPath, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	st := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	var seen [][]string
	d := Dispatcher{State: st}
	d.exec = func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
		seen = append(seen, st.SDDFallbackAllowedFiles())
		return "STATUS: DONE\nTESTS: ok\n", nil
	}
	g := worktree.NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Heads[dir] = g.Refs["main"]
	g.Dirty = true
	c, err := NewController(ControllerOpts{
		PlanPath:     planPath,
		RepoRoot:     dir,
		Git:          g,
		Dispatch:     d,
		Verifier:     Verifier{Build: "go test ./...", Runner: &flakyRunner{failFirst: true, failOut: "FAIL"}, Timeout: time.Minute},
		MaxFixRounds: 2,
		Strategy:     StrategyAdaptive,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := c.Inspect(); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	spec, _ := c.Plan.Task(1)
	res, err := c.runTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if res.ExecType != ExecMixed {
		t.Errorf("ExecType = %q, want %q", res.ExecType, ExecMixed)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 dispatches (fallback + fixer), got %d", len(seen))
	}
	for i, got := range seen {
		if len(got) != 1 || got[0] != "internal/foo" {
			t.Errorf("dispatch %d scope = %v, want [internal/foo]", i+1, got)
		}
	}
	if got := st.SDDFallbackAllowedFiles(); got != nil {
		t.Errorf("SDDFallbackAllowedFiles must be cleared after verify gate; got %v", got)
	}
}
