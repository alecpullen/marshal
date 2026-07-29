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
	g := NewFakeGitOps()
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
	g := c.Git.(*FakeGitOps)
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
	g := c.Git.(*FakeGitOps)
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
	g := c.Git.(*FakeGitOps)
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
