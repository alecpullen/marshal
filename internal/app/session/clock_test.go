package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

// TestStateStampsTranscriptTimesFromItsClock drives every place the state
// stamps a transcript time and checks it read the clock WithClock installed
// rather than the wall clock. A state on a fixed clock must build the same
// transcript every time.
func TestStateStampsTranscriptTimesFromItsClock(t *testing.T) {
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{}, WithClock(now))

	advance := func() { clock = clock.Add(time.Second) }
	advanceOne := func() time.Time {
		advance()
		return clock
	}

	// A message and its CreatedAt.
	want := advanceOne()
	s.AddMessage(RoleUser, "go", ContentTypePlain)
	msgs := s.Messages()
	if len(msgs) == 0 {
		t.Fatal("AddMessage left no message")
	}
	if got := msgs[len(msgs)-1].CreatedAt; !got.Equal(want) {
		t.Errorf("message CreatedAt = %v, want %v (the state's clock)", got, want)
	}

	// A step's start and end.
	want = advanceOne()
	id := s.BeginStep(Actor{})
	st, ok := s.Step(id)
	if !ok {
		t.Fatalf("Step(%d) not found after BeginStep", id)
	}
	if !st.StartedAt.Equal(want) {
		t.Errorf("step StartedAt = %v, want %v (the state's clock)", st.StartedAt, want)
	}
	want = advanceOne()
	s.EndStep(id)
	st, ok = s.Step(id)
	if !ok {
		t.Fatalf("Step(%d) not found after EndStep", id)
	}
	if !st.EndedAt.Equal(want) {
		t.Errorf("step EndedAt = %v, want %v (the state's clock)", st.EndedAt, want)
	}

	// The in-progress reasoning, and the thinking time it yields.
	want = advanceOne()
	s.BeginStreaming()
	if got := s.InProgress().StartedAt; !got.Equal(want) {
		t.Errorf("in-progress StartedAt = %v, want %v (the state's clock)", got, want)
	}
	s.AppendThinking("x")
	clock = clock.Add(3 * time.Second)
	s.AddMessage(RoleAssistant, "ok", ContentTypePlain)
	msgs = s.Messages()
	if got := msgs[len(msgs)-1].ThinkDuration; got != 3*time.Second {
		t.Errorf("message ThinkDuration = %v, want 3s (measured on the state's clock)", got)
	}

	// A tool call's audit timestamp.
	want = advanceOne()
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read"})
	found := false
	for _, item := range s.Transcript() {
		if item.Audit == nil {
			continue
		}
		found = true
		if got := item.Audit.Timestamp; !got.Equal(want) {
			t.Errorf("audit Timestamp = %v, want %v (the state's clock)", got, want)
		}
	}
	if !found {
		t.Fatal("Transcript() has no audit item after LogToolCall")
	}

	// A run event's At.
	want = advanceOne()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 1, Title: "abc123"})
	events := s.RunEvents()
	if len(events) == 0 {
		t.Fatal("AddRunEvent appended no event")
	}
	if got := events[len(events)-1].At; !got.Equal(want) {
		t.Errorf("run event At = %v, want %v (the state's clock)", got, want)
	}

	// A subagent's start and end.
	want = advanceOne()
	view := s.RegisterSubagent("probe", nil)
	if !view.StartedAt.Equal(want) {
		t.Errorf("subagent StartedAt = %v, want %v (the state's clock)", view.StartedAt, want)
	}
	want = advanceOne()
	s.FinishSubagent(view.ID, "done", nil)
	finished, ok := s.Subagent(view.ID)
	if !ok {
		t.Fatalf("Subagent(%d) not found after FinishSubagent", view.ID)
	}
	if !finished.EndedAt.Equal(want) {
		t.Errorf("subagent EndedAt = %v, want %v (the state's clock)", finished.EndedAt, want)
	}

	// A job exit's At, read back from the transcript card.
	want = advanceOne()
	if err := s.AddJobExit(JobExit{ID: "job-1", Command: "true"}); err != nil {
		t.Fatalf("AddJobExit = %v, want nil", err)
	}
	found = false
	for _, item := range s.Transcript() {
		if item.Kind != KindJobExit || item.JobExit == nil {
			continue
		}
		found = true
		if got := item.JobExit.At; !got.Equal(want) {
			t.Errorf("job exit At = %v, want %v (the state's clock)", got, want)
		}
	}
	if !found {
		t.Fatal("Transcript() has no job-exit item after AddJobExit")
	}
}

// TestStateWithoutAClockStampsTheWallClock guards the fallback: a State that
// New did not build has no clock, and must still be able to stamp a time.
func TestStateWithoutAClockStampsTheWallClock(t *testing.T) {
	s := &State{}
	got := s.clockNow()
	if d := time.Since(got); d < 0 || d > time.Second {
		t.Errorf("clockNow() = %v, want within a second of time.Now()", got)
	}
}

// TestNowReadsTheStatesClock pins the exported clock the in-process adapter
// builds the transcript's tree at: the time WithClock installed, or the wall
// clock for a State that New did not build.
func TestNowReadsTheStatesClock(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	s := New(config.Default(), t.TempDir(), fixed, Persistence{}, WithClock(func() time.Time { return fixed }))
	if got := s.Now(); !got.Equal(fixed) {
		t.Errorf("Now() = %v, want %v (the state's clock)", got, fixed)
	}

	empty := &State{}
	if got := empty.Now(); time.Since(got) < 0 || time.Since(got) > time.Second {
		t.Errorf("(&State{}).Now() = %v, want within a second of time.Now()", got)
	}
}
