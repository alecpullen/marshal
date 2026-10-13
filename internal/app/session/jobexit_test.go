package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
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
