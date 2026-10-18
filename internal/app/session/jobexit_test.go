package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/progress"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

func TestJobExitAppearsInTranscript(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddJobExit(JobExit{ID: "job-1", Command: "go test", ExitCode: 0, Duration: time.Second})
	var found bool
	for _, item := range s.Transcript() {
		if item.Kind == KindJobExit {
			if item.JobExit == nil {
				t.Fatal("KindJobExit item has a nil JobExit payload")
			}
			if item.JobExit.ID != "job-1" {
				t.Fatalf("wrong job: %q", item.JobExit.ID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("job exit did not reach the transcript")
	}
}

func TestJobExitStampsTime(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddJobExit(JobExit{ID: "job-1"})
	items := s.Transcript()
	if items[0].Timestamp.IsZero() {
		t.Fatal("a zero At must be stamped, or transcript ordering breaks")
	}
}

func TestAddJobExitPublishesEvent(t *testing.T) {
	// ACP's stack projector is marked dirty only by session events; a job
	// exit that published nothing left an idle web client's card running.
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)
	s.AddJobExit(JobExit{ID: "job-2", ExitCode: 3})
	select {
	case ev := <-ch:
		if ev.Type != EventJobExit || ev.Payload.JobExit == nil || ev.Payload.JobExit.ID != "job-2" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event for a job exit")
	}
}

func TestAddJobExitKeepsOnlyTheOutputTail(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	out := strings.Repeat("x", JobExitOutputCap) + "the failure"
	s.AddJobExit(JobExit{ID: "job-1", Output: out})
	got := s.Transcript()[0].JobExit.Output
	if !strings.HasSuffix(got, "the failure") || !strings.HasPrefix(got, jobExitOutputDropped) {
		t.Fatalf("output = %q…, want the tail behind a dropped marker", got[:40])
	}
	if len(got) > JobExitOutputCap+len(jobExitOutputDropped) {
		t.Fatalf("output is %d bytes, over the cap", len(got))
	}
}

func jobStartAudit(step StepID, callID, jobID string, ts time.Time) registry.AuditEvent {
	return registry.AuditEvent{Timestamp: ts, ToolName: "shell.run", StepID: step, ToolCallID: callID,
		ResultSummary: JobStartPrefix + jobID}
}

// structuredJobStart builds the audit a managed background launch records: its
// display ID, its execution identity and the captured origin.
func structuredJobStart(t *testing.T, step StepID, callID, displayID, execID string, ts time.Time) registry.AuditEvent {
	t.Helper()
	notice, err := registry.CommandExecutionNotice(registry.CommandExecution{
		ExecutionID: execID, DisplayID: displayID,
		ConversationID: "conv", Actor: "implementer", TurnID: "turn-1", StepID: "1", ToolCallID: callID,
		Cwd: "/repo", Generation: 2, StartedAt: ts,
		State: "running", Attachment: registry.ExecutionAttachmentBackground,
	})
	if err != nil {
		t.Fatalf("CommandExecutionNotice: %v", err)
	}
	return registry.AuditEvent{Timestamp: ts, ToolName: "shell.run", StepID: step, ToolCallID: callID,
		ResultSummary: JobStartPrefix + displayID, Notice: notice}
}

// A foreground command's audit carries the same structured metadata, because the
// record describes the execution. Its attachment is what keeps it out of the
// background-card index: it has its own tool row.
func TestJobStartedRequiresBackgroundAttachment(t *testing.T) {
	foreground, err := registry.CommandExecutionNotice(registry.CommandExecution{
		ExecutionID: "e1", DisplayID: "job-1", Attachment: registry.ExecutionAttachmentForeground,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := registry.AuditEvent{Timestamp: time.Unix(100, 0), ToolName: "shell.run",
		ResultSummary: JobStartPrefix + "job-1", Notice: foreground}
	if js, ok := JobStarted(ev); ok {
		t.Fatalf("foreground metadata became a card: %+v", js)
	}

	// The legacy shape still works: no notice, only the summary text.
	legacy := registry.AuditEvent{Timestamp: time.Unix(100, 0), ToolName: "shell.run",
		ResultSummary: JobStartPrefix + "job-7"}
	js, ok := JobStarted(legacy)
	if !ok || js.DisplayID != "job-7" || js.Structured {
		t.Fatalf("legacy start = %+v, ok = %v", js, ok)
	}
	// A foreground call with no background metadata is not a start at all.
	if _, ok := JobStarted(registry.AuditEvent{ToolName: "shell.run", ResultSummary: "ok"}); ok {
		t.Fatal("a plain shell call became a card")
	}
}

// The structured exit carries the execution's identity, origin and outcome
// through persistence, so a resumed session can pair it with the call that
// started it without relying on timestamps.
func TestStructuredJobExitSurvivesResume(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "test it", ContentTypePlain)
	step := first.BeginStep(Actor{})
	t0 := time.Now().Truncate(time.Second)
	first.LogToolCall(structuredJobStart(t, step, "c1", "job-1", "exec-1", t0))
	first.EndStep(step)

	code := 2
	exit := JobExitFromExecution(registry.CommandExecution{
		ExecutionID: "exec-1", DisplayID: "job-1",
		ConversationID: "conv", Actor: "implementer", TurnID: "turn-1", StepID: "1", ToolCallID: "c1",
		Cwd: "/repo", Generation: 2, PID: 4242,
		StartedAt: t0, EndedAt: t0.Add(time.Minute),
		State: "failed", Outcome: "nonzero_exit", Wait: "10s",
		Attachment:   registry.ExecutionAttachmentBackground,
		DetachReason: "wait expired", DetachedAt: t0.Add(10 * time.Second),
		ExitCode: &code, Truncated: true,
	}, "go test", "FAIL", time.Minute, &code)
	if err := first.AddJobExit(exit); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}

	restored := jobExitsOf(persistedState(t, d, sid))
	if len(restored) != 1 {
		t.Fatalf("restored exits = %+v, want 1", restored)
	}
	got := restored[0]
	if got.Lost {
		t.Fatalf("a structured completion restored as lost: %+v", got)
	}
	if got.ExecutionID != "exec-1" || got.ID != "job-1" || got.ExitCode != 2 {
		t.Fatalf("identity/exit lost: %+v", got)
	}
	if got.Exit == nil || *got.Exit != 2 {
		t.Fatalf("Exit = %v, want the observed 2", got.Exit)
	}
	if got.ConversationID != "conv" || got.Actor != "implementer" || got.TurnID != "turn-1" ||
		got.StepID != "1" || got.ToolCallID != "c1" || got.Cwd != "/repo" || got.Generation != 2 || got.PID != 4242 {
		t.Fatalf("origin lost: %+v", got)
	}
	if got.State != "failed" || got.Outcome != "nonzero_exit" || got.Wait != "10s" {
		t.Fatalf("outcome evidence lost: %+v", got)
	}
	if got.Attachment != string(registry.ExecutionAttachmentBackground) ||
		got.DetachReason != "wait expired" || !got.DetachedAt.Equal(t0.Add(10*time.Second)) || !got.Truncated {
		t.Fatalf("attachment evidence lost: %+v", got)
	}
	if !got.StartedAt.Equal(t0) || !got.EndedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("times lost: %+v", got)
	}

	// The restored exit and its start audit pair by identity in either order.
	starts := []registry.AuditEvent{structuredJobStart(t, step, "c1", "job-1", "exec-1", t0)}
	if lost := lostJobs(starts, restored); len(lost) != 0 {
		t.Fatalf("a settled structured start was reported lost: %+v", lost)
	}
}

// An execution settles once. A repeated settlement — a retry, or a duplicate
// delivery — replaces its own record and announces nothing again.
func TestAddJobExitSettlesAnExecutionOnce(t *testing.T) {
	s := persistedState(t, nil, "")
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)

	exit := JobExit{ID: "job-1", ExecutionID: "exec-1", Command: "go test", ExitCode: 0, At: time.Unix(500, 0)}
	for range 2 {
		if err := s.AddJobExit(exit); err != nil {
			t.Fatalf("AddJobExit: %v", err)
		}
	}
	var events int
	for {
		select {
		case <-ch:
			events++
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	if events != 1 {
		t.Fatalf("events = %d, want one completion for one execution", events)
	}
	if got := jobExitsOf(s); len(got) != 1 {
		t.Fatalf("records = %+v, want one", got)
	}
}

// A completion that could not be stored is not announced: a client that
// re-projects on the event would otherwise show an outcome the next resume
// cannot reproduce. It stays in the transcript and pending for a retry.
func TestUndeclaredPersistenceIsNotAnnounced(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)

	// A closed database fails every write.
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := s.AddJobExit(JobExit{ID: "job-1", ExitCode: 0}); err == nil {
		t.Fatal("a failed durable write must be reported to the caller")
	}
	select {
	case ev := <-ch:
		t.Fatalf("an unpersisted completion was announced: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	if !s.jobExitSavesPending() {
		t.Fatal("the failed write was not retained for retry")
	}
	if got := jobExitsOf(s); len(got) != 1 {
		t.Fatalf("transcript records = %+v, want the outcome kept in memory", got)
	}
	// The retry reports the failure again rather than pretending it is durable.
	if err := s.RetryJobExitSaves(); err == nil {
		t.Fatal("a retry against a broken store must still report the failure")
	}
}

// A retry that succeeds announces the completion exactly once, and the record is
// then durable.
func TestRetryJobExitSavesAnnouncesOnceAfterSuccess(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)

	exit := JobExit{ID: "job-1", ExecutionID: "exec-1", Command: "go test", ExitCode: 3, At: time.Unix(500, 0)}
	s.mu.Lock()
	s.upsertJobExitLocked(exit)
	s.mu.Unlock()
	// Stand in for the state a failed write leaves behind.
	s.jobExitSaveMu.Lock()
	s.jobExitsPending = true
	s.jobExitsUnpublished = map[string]JobExit{exit.Identity(): exit}
	s.jobExitSaveMu.Unlock()

	if err := s.RetryJobExitSaves(); err != nil {
		t.Fatalf("RetryJobExitSaves: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != EventJobExit || ev.Payload.JobExit == nil || ev.Payload.JobExit.ID != "job-1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("a successful retry announced nothing")
	}
	select {
	case ev := <-ch:
		t.Fatalf("a retry announced the same completion twice: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	if s.jobExitSavesPending() {
		t.Fatal("a successful retry left the write pending")
	}
	if err := s.RetryJobExitSaves(); err != nil {
		t.Fatalf("a no-op retry must not touch the store: %v", err)
	}
	stored, err := d.LoadJobExits(sid)
	if err != nil {
		t.Fatalf("LoadJobExits: %v", err)
	}
	if len(stored) != 1 || stored[0].ExecutionID != "exec-1" || stored[0].ExitCode != 3 {
		t.Fatalf("stored exits = %+v, want the retried completion", stored)
	}
}

// Concurrent completions must not lose one another: the snapshot and the write
// are one serialized sequence, so the last write is the newest list rather than
// an older snapshot landing over a newer one.
func TestConcurrentJobExitSavesLoseNothing(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)

	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exit := JobExit{
				ID: fmt.Sprintf("job-%d", i), ExecutionID: fmt.Sprintf("exec-%d", i),
				Command: "go test", ExitCode: i, At: time.Unix(int64(500+i), 0),
			}
			if err := s.AddJobExit(exit); err != nil {
				t.Errorf("AddJobExit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	stored, err := d.LoadJobExits(sid)
	if err != nil {
		t.Fatalf("LoadJobExits: %v", err)
	}
	if len(stored) != n {
		t.Fatalf("stored %d of %d completions: %+v", len(stored), n, stored)
	}
	seen := map[string]bool{}
	for _, r := range stored {
		if seen[r.ExecutionID] {
			t.Fatalf("duplicate completion for %q", r.ExecutionID)
		}
		seen[r.ExecutionID] = true
	}
	if got := jobExitsOf(s); len(got) != n {
		t.Fatalf("transcript records = %d, want %d", len(got), n)
	}
}

// A structured start pairs with its exit by identity, whatever order the two
// reached the transcript in. A start with no exit of its own is still lost, and
// a lost record keeps the identity of the start it belongs to.
func TestLostJobsPairsStructuredStartsByIdentity(t *testing.T) {
	t0 := time.Unix(100, 0)
	settled := structuredJobStart(t, 1, "c1", "job-1", "exec-1", t0)
	never := structuredJobStart(t, 1, "c2", "job-2", "exec-2", t0.Add(time.Second))

	// exec-1's completion sits before its own start's audit row: only identity
	// pairing can settle it, and order-based pairing would wrongly report the
	// start lost.
	exits := []JobExit{
		{ID: "job-1", ExecutionID: "exec-1", ExitCode: 0, At: t0.Add(-time.Second)},
	}
	lost := lostJobs([]registry.AuditEvent{settled, never}, exits)
	if len(lost) != 1 {
		t.Fatalf("lost = %+v, want only job-2", lost)
	}
	if lost[0].ID != "job-2" || lost[0].ExecutionID != "exec-2" || !lost[0].Lost || lost[0].ExitCode != -1 {
		t.Fatalf("lost record = %+v, want job-2 with its identity and no exit", lost[0])
	}
	if lost[0].Attachment != string(registry.ExecutionAttachmentBackground) || lost[0].PID != 0 {
		t.Fatalf("lost record lost its captured metadata: %+v", lost[0])
	}
	if lost[0].At != never.Timestamp {
		t.Fatalf("lost record stamped %v, want its start %v", lost[0].At, never.Timestamp)
	}
}

// A completion carrying an identity settles only its own execution: it must not
// clear another process's still-running start that reused the display ID.
func TestStructuredExitDoesNotSettleAReusedDisplayID(t *testing.T) {
	t0 := time.Unix(100, 0)
	// The first process's job-1 had no identity recorded (it predates this
	// metadata); the second process's job-1 has one.
	first := jobStartAudit(1, "c1", "job-1", t0)
	second := structuredJobStart(t, 1, "c2", "job-1", "exec-2", t0.Add(time.Minute))
	exits := []JobExit{
		{ID: "job-1", ExecutionID: "exec-2", ExitCode: 0, At: t0.Add(2 * time.Minute)},
	}
	lost := lostJobs([]registry.AuditEvent{first, second}, exits)
	if len(lost) != 1 {
		t.Fatalf("lost = %+v, want the first process's unsettled job-1", lost)
	}
	if lost[0].ExecutionID != "" || !lost[0].At.Equal(t0) || lost[0].ID != "job-1" {
		t.Fatalf("lost record = %+v, want the identity-less first start", lost[0])
	}
}

func jobExitsOf(s *State) []JobExit {
	var out []JobExit
	for _, it := range s.Transcript() {
		if it.JobExit != nil {
			out = append(out, *it.JobExit)
		}
	}
	return out
}

func TestJobExitSurvivesResume(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "test it", ContentTypePlain)
	step := first.BeginStep(Actor{})
	t0 := time.Now()
	first.LogToolCall(jobStartAudit(step, "c1", "job-1", t0))
	first.AddJobExit(JobExit{ID: "job-1", Command: "go test", ExitCode: 2, Duration: 3 * time.Second, Output: "FAIL", At: t0.Add(time.Second)})
	first.EndStep(step)

	exits := jobExitsOf(persistedState(t, d, sid))
	if len(exits) != 1 || exits[0].Lost || exits[0].ExitCode != 2 || exits[0].Output != "FAIL" || exits[0].Duration != 3*time.Second {
		t.Fatalf("restored exits = %+v", exits)
	}
}

func TestResumeMarksUnfinishedJobsLost(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "test it", ContentTypePlain)
	step := first.BeginStep(Actor{})
	// Whole seconds: tool_calls stores timestamps at that precision.
	t0 := time.Now().Truncate(time.Second)
	// The process quits while job-1 runs.
	first.LogToolCall(jobStartAudit(step, "c1", "job-1", t0))
	first.EndStep(step)

	// Resumed: the new process numbers its jobs from job-1 again.
	second := persistedState(t, d, sid)
	exits := jobExitsOf(second)
	if len(exits) != 1 || !exits[0].Lost || exits[0].ID != "job-1" || !exits[0].At.Equal(t0) {
		t.Fatalf("exits after resume = %+v, want job-1 lost at its start", exits)
	}
	step2 := second.BeginStep(Actor{})
	second.LogToolCall(jobStartAudit(step2, "c2", "job-1", t0.Add(time.Minute)))
	second.AddJobExit(JobExit{ID: "job-1", ExitCode: 0, At: t0.Add(2 * time.Minute)})
	second.EndStep(step2)

	// A lost exit is never persisted, and is synthesized again on the next
	// resume for the first job only.
	exits = jobExitsOf(persistedState(t, d, sid))
	var lost, settled int
	for _, e := range exits {
		if e.Lost {
			lost++
			if !e.At.Equal(t0) {
				t.Errorf("lost exit stamped %v, want the first start %v", e.At, t0)
			}
		} else {
			settled++
		}
	}
	if lost != 1 || settled != 1 {
		t.Fatalf("exits = %+v, want one lost and one settled", exits)
	}
}

// The final progress a job observed is durable before its completion is
// announced: a client that reacts to the event reads the same progress back,
// and a retried save still carries it.
func TestJobExitFinalProgressBeforeAnnouncement(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)

	exit := JobExit{
		ID: "job-1", ExecutionID: "exec-1", Command: "go test", ExitCode: 0,
		At: time.Unix(500, 0), State: "completed", Outcome: "exit_zero",
		ResultRevision: "rev-1", SourceEpoch: "epoch-1",
		ProgressConfig: &progress.Config{Profile: "tests", Source: "none", Title: "go test"},
		Progress:       &progress.Snapshot{Frame: progress.Frame{Version: 1, Sequence: 9, Detail: "3 passed"}},
	}
	if err := s.AddJobExit(exit); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}
	select {
	case ev := <-ch:
		announced := ev.Payload.JobExit
		if ev.Type != EventJobExit || announced == nil {
			t.Fatalf("event = %+v", ev)
		}
		if announced.Progress == nil || announced.Progress.Detail != "3 passed" {
			t.Fatalf("announced exit lost its final progress: %+v", announced.Progress)
		}
		// The completion is announced only after it is durable, so the stored
		// record already carries the same final progress.
		stored, err := d.LoadJobExits(sid)
		if err != nil {
			t.Fatalf("LoadJobExits: %v", err)
		}
		if len(stored) != 1 || stored[0].Progress == nil || stored[0].Progress.Detail != "3 passed" {
			t.Fatalf("stored exit = %+v, want the final progress before the announcement", stored)
		}
		if stored[0].ProgressConfig == nil || stored[0].ProgressConfig.Title != "go test" {
			t.Fatalf("stored exit lost its progress config: %+v", stored[0].ProgressConfig)
		}
		if stored[0].ResultRevision != "rev-1" || stored[0].SourceEpoch != "epoch-1" {
			t.Fatalf("stored exit lost its revision/epoch: %+v", stored[0])
		}
	case <-time.After(time.Second):
		t.Fatal("no event for a job exit")
	}

	// A retried save carries the final progress too, not a stale snapshot.
	retried := JobExit{
		ID: "job-2", ExecutionID: "exec-2", Command: "go test", ExitCode: 3,
		At: time.Unix(600, 0), State: "failed", Outcome: "nonzero_exit",
		Progress: &progress.Snapshot{Frame: progress.Frame{Version: 1, Sequence: 3, Detail: "1 failed"}},
	}
	s.mu.Lock()
	s.upsertJobExitLocked(retried)
	s.mu.Unlock()
	s.jobExitSaveMu.Lock()
	s.jobExitsPending = true
	s.jobExitsUnpublished = map[string]JobExit{retried.Identity(): retried}
	s.jobExitSaveMu.Unlock()
	if err := s.RetryJobExitSaves(); err != nil {
		t.Fatalf("RetryJobExitSaves: %v", err)
	}
	stored, err := d.LoadJobExits(sid)
	if err != nil {
		t.Fatalf("LoadJobExits: %v", err)
	}
	for _, r := range stored {
		if r.ExecutionID == "exec-2" {
			if r.Progress == nil || r.Progress.Detail != "1 failed" {
				t.Fatalf("retried exit lost its final progress: %+v", r.Progress)
			}
			return
		}
	}
	t.Fatalf("retried exit not stored: %+v", stored)
}

// A failed or unknown command result is never stored in the work view, so its
// acknowledgment is validated against the durable exit instead: the canonical
// command key at the exit's own current revision is accepted.
func TestWorkAckAcceptsFailedCommandResult(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	exit := JobExit{
		ID: "job-1", ExecutionID: "exec-1", Command: "go test", ExitCode: 2,
		At: time.Unix(500, 0), State: "failed", Outcome: "nonzero_exit",
		SourceEpoch: "epoch-1", ResultRevision: "rev-1",
	}
	if err := s.AddJobExit(exit); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}

	key := progress.WorkKey(sid, "epoch-1", "command", "exec-1")
	if err := s.AcknowledgeWork(key, "rev-1"); err != nil {
		t.Fatalf("AcknowledgeWork(%q, rev-1): %v", key, err)
	}
	view := s.WorkView()
	if len(view.Acknowledged) != 1 || view.Acknowledged[0].TaskKey != key ||
		view.Acknowledged[0].ResultRevision != "rev-1" {
		t.Fatalf("command acknowledgment not stored: %+v", view.Acknowledged)
	}

	// A display job ID is still no task key, and a stale revision still fails.
	if err := s.AcknowledgeWork("job-1", "rev-1"); err == nil {
		t.Fatal("a display job ID was accepted as a task key")
	}
	if err := s.AcknowledgeWork(key, "rev-2"); err == nil {
		t.Fatal("a stale revision acknowledged a command result")
	}
}
