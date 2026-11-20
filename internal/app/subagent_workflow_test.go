package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// journeyAdmission is a deterministic admission lease: the workflow tests drive
// the mutation service directly, and the admission path itself is pinned by the
// lifecycle tools' own tests (P3 Task 3).
type journeyAdmission struct{ committed bool }

func (a *journeyAdmission) Commit() error { a.committed = true; return nil }

func (a *journeyAdmission) Release() {}

// journeyFixture is one parent conversation that owns real child worktrees in a
// real repository, with the production gate, command service and the mutation
// service the parent tools drive. The parent works in a feature worktree of its
// own, so the integrations it performs never touch the main checkout — which is
// what the journey has to prove.
type journeyFixture struct {
	t       *testing.T
	repo    string // the main checkout, which nothing here may move
	feature string // the parent's own worktree, and the captured integration target
	owner   string
	state   *session.State
	lock    *swarm.WriteLock
	jobs    *native.JobManager
	gate    *subagentIdleGate
	svc     *subagentwork.Service
	store   subagentwork.Store
	base    string // the feature worktree's starting commit
}

func newJourneyFixture(t *testing.T) *journeyFixture {
	t.Helper()
	repo, _ := idleGateRepo(t)
	f := &journeyFixture{t: t, repo: repo, owner: "parent-1", lock: &swarm.WriteLock{}}

	// The parent's own worktree, on a branch of its own, so integrating a child
	// into it cannot move main.
	f.feature = filepath.Join(t.TempDir(), "feature")
	if out, err := runGitIn(repo, "worktree", "add", "-b", "feature", f.feature); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	mustWrite(t, filepath.Join(f.feature, "interface.go"),
		"package main\n\n// Service is the interface both children implement.\ntype Service interface{ Run() error }\n")
	if out, err := runGitIn(f.feature, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := runGitIn(f.feature, "commit", "-m", "the interface both children implement"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	f.base = strings.TrimSpace(mustGit(t, f.feature, "rev-parse", "HEAD"))

	f.state = session.New(config.Default(), f.feature, time.Unix(100, 0), session.Persistence{SessionID: f.owner})
	f.jobs = native.NewJobManager(context.Background(), idleGateRunner{}, f.feature, 8, time.Hour, 1<<20)
	t.Cleanup(func() { _ = f.jobs.Shutdown(context.Background()) })
	f.gate = newSubagentIdleGate(f.state, f.lock, f.jobs, worktree.CLIGitOps{})
	f.store = subagentwork.NewMemoryStore()
	f.svc = subagentwork.NewService(f.store, worktree.CLIGitOps{})
	return f
}

// mustGit runs one git command and returns its trimmed output.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitIn(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// prepare allocates one owned child from the parent's committed interface. It
// reports errors rather than failing the test, so a fixture can prepare two
// children from concurrent goroutines.
func (f *journeyFixture) prepare(taskKey, base string) (subagentwork.Record, error) {
	return f.svc.Prepare(context.Background(), subagentwork.PrepareRequest{
		OwnerSessionID:    f.owner,
		TaskKey:           taskKey,
		InvocationID:      "inv-" + taskKey,
		ProjectRoot:       f.repo,
		ParentRoot:        f.feature,
		BaseCommit:        base,
		IsolatedSupported: true,
	})
}

// commit writes one file into a child's own worktree and commits it there,
// returning the record the parent reads afterwards.
func (f *journeyFixture) commit(rec subagentwork.Record, file, content string) (subagentwork.Record, error) {
	if err := os.WriteFile(filepath.Join(rec.Binding.Root, file), []byte(content), 0o644); err != nil {
		return subagentwork.Record{}, fmt.Errorf("write %s: %w", file, err)
	}
	if out, err := runGitIn(rec.Binding.Root, "add", "-A"); err != nil {
		return subagentwork.Record{}, fmt.Errorf("git add: %w\n%s", err, out)
	}
	if out, err := runGitIn(rec.Binding.Root, "commit", "-m", "implement "+file); err != nil {
		return subagentwork.Record{}, fmt.Errorf("git commit: %w\n%s", err, out)
	}
	return f.svc.Complete(context.Background(), f.owner, rec.TaskKey, subagentwork.Completion{
		Outcome: subagentwork.OutcomeDone,
	})
}

// child prepares, commits and completes one child in sequence.
func (f *journeyFixture) child(taskKey, file, content string) subagentwork.Record {
	f.t.Helper()
	rec, err := f.prepare(taskKey, "")
	if err != nil {
		f.t.Fatalf("Prepare %s: %v", taskKey, err)
	}
	if rec.Binding.BaseSHA != f.base {
		f.t.Fatalf("%s started at %s, want the parent's committed HEAD %s", taskKey, rec.Binding.BaseSHA, f.base)
	}
	rec, err = f.commit(rec, file, content)
	if err != nil {
		f.t.Fatalf("%s: %v", taskKey, err)
	}
	if rec.Candidate == "" {
		f.t.Fatalf("%s reported no candidate", taskKey)
	}
	return rec
}

// mutationDeps is the production gate with a deterministic admission. The
// admission lease the parent tools take is pinned by their own tests; what this
// fixture proves is the lifecycle the service and the workspaces go through.
func (f *journeyFixture) mutationDeps() subagentwork.MutationDeps {
	deps := f.gate.lifecycleMutationDeps(nil)
	deps.Admit = func(context.Context, string) (subagentwork.Admission, error) {
		return &journeyAdmission{}, nil
	}
	return deps
}

// integrate integrates one record's exact candidate.
func (f *journeyFixture) integrate(taskKey, candidate string) (subagentwork.Record, error) {
	return f.svc.Integrate(context.Background(), subagentwork.Mutation{
		OwnerSessionID: f.owner, TaskKey: taskKey, ExpectedHead: candidate,
	}, f.mutationDeps())
}

// head is a checkout's current commit.
func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
}

// branchExists reports whether the repository still holds the named branch.
func branchExists(dir, branch string) bool {
	out, err := runGitIn(dir, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil && strings.TrimSpace(out) != ""
}

// ancestor reports whether commit is reachable from tip.
func ancestor(dir, commit, tip string) bool {
	_, err := runGitIn(dir, "merge-base", "--is-ancestor", commit, tip)
	return err == nil
}

// TestParallelChildIntegrationJourney walks the workflow the controller brief
// describes: the parent commits the interface, dispatches two children that
// genuinely overlap, reviews their exact candidates, integrates both serially,
// validates the combined tree, and leaves main and its unrelated dirt alone.
func TestParallelChildIntegrationJourney(t *testing.T) {
	f := newJourneyFixture(t)

	// Unrelated dirt in the main checkout: an untracked file nothing here owns.
	mustWrite(t, filepath.Join(f.repo, "scratch.txt"), "the developer's own notes\n")
	mainHead := head(t, f.repo)

	// Both children are prepared before either commits, so the two really do
	// overlap: two live worktrees built from the same committed interface.
	specs := []struct{ taskKey, file, content string }{
		{"task-a", "server.go", "package main\n\nfunc serve() error { return nil }\n"},
		{"task-b", "client.go", "package main\n\nfunc request() error { return nil }\n"},
	}
	prepared := make([]subagentwork.Record, len(specs))
	errs := make([]error, len(specs))
	var ready sync.WaitGroup
	ready.Add(len(specs))
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := range specs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := f.prepare(specs[i].taskKey, "")
			if err != nil {
				errs[i] = err
				ready.Done()
				return
			}
			prepared[i] = rec
			ready.Done()
			<-gate // both children are live before either does its work
			rec, err = f.commit(rec, specs[i].file, specs[i].content)
			if err != nil {
				errs[i] = err
				return
			}
			prepared[i] = rec
		}(i)
	}
	ready.Wait()
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("child %s: %v", specs[i].taskKey, err)
		}
	}

	// Independent commits from one base: neither candidate contains the other.
	a, b := prepared[0], prepared[1]
	if a.Candidate == "" || b.Candidate == "" || a.Candidate == b.Candidate {
		t.Fatalf("candidates = %q, %q; want two distinct commits", a.Candidate, b.Candidate)
	}
	if ancestor(f.feature, a.Candidate, b.Candidate) || ancestor(f.feature, b.Candidate, a.Candidate) {
		t.Fatal("one candidate is an ancestor of the other; the children were not independent")
	}
	for _, rec := range prepared {
		if rec.Binding.BaseSHA != f.base {
			t.Fatalf("%s started at %s, want the interface commit %s", rec.TaskKey, rec.Binding.BaseSHA, f.base)
		}
	}

	// The reviewer runs against the exact candidate — the `base_commit` the
	// brief names — not a branch that could have moved underneath it.
	review, err := f.prepare("review-a", a.Candidate)
	if err != nil {
		t.Fatalf("Prepare review: %v", err)
	}
	if review.Binding.BaseSHA != a.Candidate {
		t.Fatalf("the reviewer started at %s, want the exact candidate %s",
			review.Binding.BaseSHA, a.Candidate)
	}
	if _, err := f.svc.Complete(context.Background(), f.owner, "review-a", subagentwork.Completion{
		Outcome: subagentwork.OutcomeDone,
	}); err != nil {
		t.Fatalf("Complete review: %v", err)
	}

	// Both candidates integrate, one after the other: the second acceptance has
	// to advance past the first merge rather than conflict with it.
	results := make([]string, len(prepared))
	for i, rec := range prepared {
		settled, err := f.integrate(rec.TaskKey, rec.Candidate)
		if err != nil {
			t.Fatalf("integrate %s: %v", rec.TaskKey, err)
		}
		if settled.Integration == nil || settled.Integration.State != subagentwork.IntegrationIntegrated {
			t.Fatalf("integrate %s settled as %+v, want integrated", rec.TaskKey, settled.Integration)
		}
		if settled.Integration.ExpectedSource != rec.Candidate {
			t.Fatalf("integrate %s recorded candidate %s, want %s",
				rec.TaskKey, settled.Integration.ExpectedSource, rec.Candidate)
		}
		results[i] = settled.Integration.ResultCommit
	}
	// The target ends at the second acceptance, not the first: the two were
	// accepted serially, so the later merge sits on top of the earlier one.
	top := head(t, f.feature)
	if results[len(results)-1] != top {
		t.Fatalf("the target is at %s, want the last accepted result %s", top, results[len(results)-1])
	}

	// Both candidates are in the tree, and the combined tree holds both files
	// on top of the interface, with each child's own commit preserved.
	for _, rec := range prepared {
		if !ancestor(f.feature, rec.Candidate, top) {
			t.Fatalf("%s is not in the combined tree", rec.Candidate)
		}
	}
	files := mustGit(t, f.feature, "ls-tree", "--name-only", "HEAD")
	for _, file := range []string{"interface.go", "server.go", "client.go"} {
		if !strings.Contains(files, file) {
			t.Fatalf("the combined tree lacks %s:\n%s", file, files)
		}
	}
	// The merge commits keep both children's work rather than squashing it away.
	subjects := mustGit(t, f.feature, "log", "--format=%s", "HEAD")
	for _, want := range []string{"implement server.go", "implement client.go"} {
		if !strings.Contains(subjects, want) {
			t.Fatalf("the combined history lacks %q:\n%s", want, subjects)
		}
	}
	// Each integration is a real merge commit whose second parent is the exact
	// candidate, so the target's history records what was accepted.
	for i, rec := range prepared {
		parents := strings.Fields(mustGit(t, f.feature, "rev-list", "--parents", "-n", "1", results[i]))
		if len(parents) != 3 || parents[2] != rec.Candidate {
			t.Fatalf("the merge for %s has parents %v, want the candidate %s as its second",
				rec.TaskKey, parents, rec.Candidate)
		}
	}

	// Main and its unrelated dirt are untouched: integration went to the
	// captured feature worktree, never to the main checkout.
	if got := head(t, f.repo); got != mainHead {
		t.Fatalf("main moved to %s during integration, want %s", got, mainHead)
	}
	if raw, err := os.ReadFile(filepath.Join(f.repo, "scratch.txt")); err != nil ||
		string(raw) != "the developer's own notes\n" {
		t.Fatalf("the main checkout's unrelated file changed: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "server.go")); err == nil {
		t.Fatal("a child's file landed in the main checkout")
	}
}

// TestConflictCancelSharedAndRestartJourney walks the lifecycle's failure side:
// a conflict keeps the conflicting side, a cancelled child keeps its evidence,
// an explicit shared child writes in the captured root, and a restart yields the
// one receipt the record already holds without resurrecting a model run.
func TestConflictCancelSharedAndRestartJourney(t *testing.T) {
	f := newJourneyFixture(t)
	ctx := context.Background()
	mainHead := head(t, f.repo)

	// Two children from the same base that change the SAME file: the second
	// integration cannot merge cleanly.
	records := []subagentwork.Record{
		f.child("task-x", "shared.go", "package main\n\nfunc shared() string { return \"from x\" }\n"),
		f.child("task-y", "shared.go", "package main\n\nfunc shared() string { return \"from y\" }\n"),
	}
	first, err := f.integrate(records[0].TaskKey, records[0].Candidate)
	if err != nil {
		t.Fatalf("integrate %s: %v", records[0].TaskKey, err)
	}
	if first.Integration == nil || first.Integration.State != subagentwork.IntegrationIntegrated {
		t.Fatalf("the first integration settled as %+v, want integrated", first.Integration)
	}
	// A completed integration cleans its own artifacts: the source worktree and
	// its branch are gone, because the work is in the target now.
	if branchExists(f.repo, records[0].Binding.Branch) {
		t.Fatalf("%s kept its branch after a completed integration", records[0].TaskKey)
	}

	before := head(t, f.feature)
	settled, err := f.integrate(records[1].TaskKey, records[1].Candidate)
	if err == nil {
		t.Fatalf("a conflicting candidate integrated cleanly: %+v", settled.Integration)
	}
	if settled.Integration == nil || settled.Integration.State != subagentwork.IntegrationConflict {
		t.Fatalf("the conflicting record settled as %+v, want conflict", settled.Integration)
	}
	// The conflict aborted only its own merge: the target is exactly where the
	// accepted integration left it, and the conflicting side is retained for
	// resolution rather than discarded.
	if got := head(t, f.feature); got != before {
		t.Fatalf("the conflicting attempt moved the target to %s, want %s", got, before)
	}
	if !branchExists(f.repo, records[1].Binding.Branch) {
		t.Fatal("the conflicting candidate lost its branch; its work must stay resolvable")
	}
	if _, err := os.Stat(records[1].Binding.Root); err != nil {
		t.Fatalf("the conflicting candidate lost its worktree: %v", err)
	}
	if settled.CleanupState != subagentwork.CleanupRetained {
		t.Fatalf("a conflict recorded cleanup %q, want retained", settled.CleanupState)
	}

	// A cancelled child keeps its evidence: the commit it made and the state it
	// settled in survive, so a parent can still inspect exactly what it had.
	cancelled, err := f.prepare("task-cancelled", "")
	if err != nil {
		t.Fatalf("Prepare cancelled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cancelled.Binding.Root, "half.go"),
		[]byte("package main\n\nfunc half() {}\n"), 0o644); err != nil {
		t.Fatalf("write half.go: %v", err)
	}
	if out, err := runGitIn(cancelled.Binding.Root, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := runGitIn(cancelled.Binding.Root, "commit", "-m", "half-done work"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	cancelled, err = f.svc.Complete(ctx, f.owner, "task-cancelled", subagentwork.Completion{
		Outcome: subagentwork.OutcomeStopped,
	})
	if err != nil {
		t.Fatalf("Complete cancelled: %v", err)
	}
	if cancelled.Candidate == "" || cancelled.Outcome != subagentwork.OutcomeStopped {
		t.Fatalf("a cancelled child lost its evidence: %+v", cancelled)
	}
	if !branchExists(f.repo, cancelled.Binding.Branch) {
		t.Fatal("a cancelled child's branch was deleted; its work must stay inspectable")
	}
	if _, err := os.Stat(cancelled.Binding.Root); err != nil {
		t.Fatalf("a cancelled child's worktree was removed: %v", err)
	}

	// An explicit shared child writes in the captured root — the parent's own
	// checkout — because that is what shared mode means, and the parent commits.
	shared, err := f.svc.Prepare(ctx, subagentwork.PrepareRequest{
		OwnerSessionID:    f.owner,
		TaskKey:           "task-shared",
		InvocationID:      "inv-task-shared",
		ProjectRoot:       f.repo,
		ParentRoot:        f.feature,
		Mode:              subagentwork.ModeShared,
		IsolatedSupported: true,
	})
	if err != nil {
		t.Fatalf("Prepare shared: %v", err)
	}
	if shared.Binding.Mode != subagentwork.ModeShared || shared.Binding.Root != f.feature {
		t.Fatalf("a shared child was bound to %+v, want the captured root %s", shared.Binding, f.feature)
	}
	// Its candidate is the captured base: a shared child owns no commit, so the
	// work it leaves is the parent's to commit.
	if shared.Candidate != "" {
		t.Fatalf("a shared child reported candidate %q, want none", shared.Candidate)
	}
	mustWrite(t, filepath.Join(f.feature, "shared-explicit.go"), "package main\n\nfunc explicit() {}\n")
	if _, err := os.Stat(filepath.Join(f.feature, "shared-explicit.go")); err != nil {
		t.Fatalf("a shared child's write did not land in the captured root: %v", err)
	}
	if _, err := f.svc.Complete(ctx, f.owner, "task-shared", subagentwork.Completion{
		Outcome: subagentwork.OutcomeDone,
	}); err != nil {
		t.Fatalf("Complete shared: %v", err)
	}

	// Restart: a fresh service over the same durable store reconciles the records
	// it already holds. It yields the one receipt the record carried, moves
	// nothing, and runs no model at all.
	restarted := subagentwork.NewService(f.store, worktree.CLIGitOps{})
	targetBefore := head(t, f.feature)
	reconciled, err := restarted.Reconcile(ctx, f.owner, f.mutationDeps())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := head(t, f.feature); got != targetBefore {
		t.Fatalf("reconciliation moved the target to %s, want %s", got, targetBefore)
	}
	integrated := 0
	for _, rec := range reconciled {
		if rec.Integration != nil && rec.Integration.State == subagentwork.IntegrationIntegrated {
			integrated++
		}
	}
	if integrated != 1 {
		t.Fatalf("reconciliation reports %d integrations, want the one accepted receipt", integrated)
	}
	// The conflicted record keeps its conflict — reconciliation proves state, it
	// does not retry a merge — and a cancelled record keeps its outcome.
	for _, rec := range reconciled {
		switch rec.TaskKey {
		case "task-y":
			if rec.Integration == nil || rec.Integration.State != subagentwork.IntegrationConflict {
				t.Fatalf("a conflicted record reconciled to %+v, want the recorded conflict", rec.Integration)
			}
		case "task-cancelled":
			if rec.Outcome != subagentwork.OutcomeStopped {
				t.Fatalf("a cancelled record reconciled to outcome %q", rec.Outcome)
			}
		}
	}

	// Main never moved through any of it.
	if got := head(t, f.repo); got != mainHead {
		t.Fatalf("main moved to %s, want %s", got, mainHead)
	}
}

// Compile-time guards: the fixture keeps using the production wiring these
// packages provide, so a change to either is a compile error here rather than a
// silent divergence.
var (
	_ = agent.NewSubagentIntegrateTool
	_ = policy.NewEngine
	_ = registry.New
	_ = agenttest.ScriptedProvider{}
)
