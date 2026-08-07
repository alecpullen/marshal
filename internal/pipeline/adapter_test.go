package pipeline

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// newAdapterTestState builds a fresh session state for adapter tests.
func newAdapterTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
}

func TestAdapterClearsProgressAfterRun(t *testing.T) {
	d, _ := scriptedDispatch(t, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	if err := a.Run(context.Background(), c.Plan.Path); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p := st.SDDProgress(); p.Active {
		t.Error("progress still active after a completed run — the live-strip spinner never stops")
	}
}

func TestAdapterKeepsProgressLiveAtGate(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	err := a.Run(context.Background(), c.Plan.Path)
	if err == nil {
		t.Fatal("Run: want the gate error, got nil")
	}
	p := st.SDDProgress()
	if !p.Active {
		t.Error("progress cleared at a human gate — the run resumes after AnswerGate")
	}
	if p.Finished {
		t.Error("run marked finished at a human gate — it resumes after AnswerGate")
	}
}

func TestAdapterSurfacesGateAndAnswer(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	err := a.Run(context.Background(), c.Plan.Path)
	if err == nil {
		t.Fatal("Run: want the gate error, got nil")
	}
	gate := st.SDDGate()
	if gate.Question != "user level or system level?" {
		t.Errorf("gate = %+v, want the subagent's question", gate)
	}
	a.AnswerGate("User level.")
	if st.SDDGate().Question != "" {
		t.Error("gate not cleared after AnswerGate")
	}
	if err := a.Run(context.Background(), c.Plan.Path); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
}

// TestAdapterMirrorsEventFields pins the controller's session mirroring:
// while progress is live (gate-pending), PlanName / TotalTasks / Phase must
// all reflect what the controller has reported. After Task 5 the success
// path clears progress, so the snapshot is taken at the gate — the only
// window where these fields are guaranteed visible to the TUI.
func TestAdapterMirrorsEventFields(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	if err := a.Run(context.Background(), c.Plan.Path); err == nil {
		t.Fatal("Run: want the gate error, got nil")
	}
	p := st.SDDProgress()
	if p.PlanName == "" {
		t.Error("PlanName not populated while progress is live")
	}
	if p.TotalTasks == 0 {
		t.Error("TotalTasks not populated while progress is live")
	}
	if p.Phase == "" {
		t.Error("Phase not populated while progress is live")
	}
}

func TestAdapterFinishesRunWithSummary(t *testing.T) {
	d, _ := scriptedDispatch(t, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	if err := a.Run(context.Background(), c.Plan.Path); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p := st.SDDProgress()
	if p.Active {
		t.Error("progress still active after a completed run")
	}
	if !p.Finished || !p.Succeeded {
		t.Errorf("Finished=%v Succeeded=%v, want true/true", p.Finished, p.Succeeded)
	}
	if len(p.Tasks) != len(c.Plan.Tasks) {
		t.Fatalf("Tasks = %d titles, want %d", len(p.Tasks), len(c.Plan.Tasks))
	}
	for i, title := range p.Tasks {
		if title != c.Plan.Tasks[i].Title {
			t.Errorf("Tasks[%d] = %q, want %q", i, title, c.Plan.Tasks[i].Title)
		}
	}
	if p.StartedAt.IsZero() || p.EndedAt.IsZero() {
		t.Error("StartedAt/EndedAt not recorded")
	}
}

func TestAdapterSatisfiesTheRunnerContract(t *testing.T) {
	// Compile-time shape check: the methods the TUI's AgentRunner needs.
	var a any = &ControllerAdapter{}
	if _, ok := a.(interface {
		Run(context.Context, string) error
		SetForceClass(string)
		SetPolicyRules([]config.PermissionRule)
		AnswerGate(string)
	}); !ok {
		t.Fatal("ControllerAdapter does not satisfy the TUI runner contract")
	}
}

func TestAdapterMapsVerifyFailureToRunEvent(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}

	a.Event(Event{TaskN: 3, Phase: PhaseFixing, Payload: VerifyFailedPayload{
		Command: "go test ./...", Output: "--- FAIL: TestFoo", Round: 1, MaxRounds: 3,
	}})

	evs := st.RunEvents()
	if len(evs) != 1 {
		t.Fatalf("got %d run events, want 1", len(evs))
	}
	if evs[0].Kind != session.RunEventVerifyFailed {
		t.Errorf("Kind = %v, want RunEventVerifyFailed", evs[0].Kind)
	}
	if evs[0].Title != "go test ./..." {
		t.Errorf("Title = %q, want the failing command", evs[0].Title)
	}
	if evs[0].Body != "--- FAIL: TestFoo" {
		t.Errorf("Body = %q, want the verifier output", evs[0].Body)
	}
	if evs[0].TaskN != 3 {
		t.Errorf("TaskN = %d, want 3", evs[0].TaskN)
	}
}

func TestAdapterMapsFindingsToOneEventEach(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}

	a.Event(Event{TaskN: 2, Phase: PhaseReviewing, Payload: ReviewPayload{
		Findings: []Finding{
			{Severity: SeverityCritical, Text: "nil deref on empty input"},
			{Severity: SeverityMinor, Text: "stale comment"},
		},
	}})

	evs := st.RunEvents()
	if len(evs) != 2 {
		t.Fatalf("got %d run events, want one per finding", len(evs))
	}
	if evs[0].Severity != string(SeverityCritical) || evs[0].Title != "nil deref on empty input" {
		t.Errorf("first finding = %#v", evs[0])
	}
	if evs[1].Severity != string(SeverityMinor) {
		t.Errorf("second finding severity = %q, want Minor", evs[1].Severity)
	}
}

func TestAdapterMapsCleanReviewToOneEvent(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}
	a.Event(Event{TaskN: 2, Phase: PhaseReviewing, Payload: ReviewPayload{Clean: true}})

	evs := st.RunEvents()
	if len(evs) != 1 || evs[0].Detail != "clean" {
		t.Fatalf("a clean review must record one 'clean' event, got %#v", evs)
	}
}

func TestAdapterMapsCommitRetryConcernAndSkip(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}

	a.Event(Event{TaskN: 1, Payload: CommitPayload{SHA: "a3f9e21", Subject: "task 1 — Scaffold"}})
	a.Event(Event{TaskN: 1, Payload: RetryPayload{Attempt: 2, MaxAttempts: 3, Err: "connection reset"}})
	a.Event(Event{TaskN: 1, Payload: ConcernPayload{Text: "no coverage for overflow"}})
	a.Event(Event{TaskN: 1, Payload: GateSkippedPayload{Reason: "no build or test command configured"}})

	evs := st.RunEvents()
	if len(evs) != 4 {
		t.Fatalf("got %d run events, want 4", len(evs))
	}
	wantKinds := []session.RunEventKind{
		session.RunEventCommit, session.RunEventRetry,
		session.RunEventConcern, session.RunEventGateSkipped,
	}
	for i, want := range wantKinds {
		if evs[i].Kind != want {
			t.Errorf("event %d kind = %v, want %v", i, evs[i].Kind, want)
		}
	}
	if evs[0].Title != "a3f9e21" {
		t.Errorf("commit Title = %q, want the short SHA", evs[0].Title)
	}
}

func TestAdapterIgnoresNilAndUnknownPayloads(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}
	a.Event(Event{TaskN: 1, Phase: PhaseImplementing})              // nil payload
	a.Event(Event{TaskN: 1, Phase: PhaseImplementing, Payload: 42}) // unknown type
	if len(st.RunEvents()) != 0 {
		t.Error("nil and unrecognised payloads must produce no run events, not a panic or a placeholder")
	}
}

func TestAdapterStillUpdatesProgressScalars(t *testing.T) {
	st := newAdapterTestState(t)
	a := &ControllerAdapter{state: st}
	a.Event(Event{TaskN: 4, TotalTasks: 7, Phase: PhaseImplementing, FixRound: 1, MaxFixRounds: 3})
	p := st.SDDProgress()
	if p.CurrentTask != 4 || p.TotalTasks != 7 || p.Phase != PhaseImplementing || p.FixRound != 1 {
		t.Errorf("progress scalars regressed: %#v", p)
	}
}
