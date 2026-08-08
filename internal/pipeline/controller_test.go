package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/llm/provider"
	"marshal/internal/worktree"
)

// reportPathRe matches the report-contract line in an implementer or fixer
// prompt, capturing the absolute path the subagent is told to write to.
var reportPathRe = regexp.MustCompile(`Write your full report to (\S+)`)

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
