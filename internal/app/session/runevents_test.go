package session

import (
	"testing"
	"time"
)

func TestAddAndListRunEvents(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 1, Title: "a3f9e21", Detail: "plan: task 1 — Scaffold"})
	s.AddRunEvent(RunEvent{Kind: RunEventVerifyFailed, TaskN: 2, Title: "go test ./...", Body: "FAIL"})

	got := s.RunEvents()
	if len(got) != 2 {
		t.Fatalf("got %d run events, want 2", len(got))
	}
	if got[0].Kind != RunEventCommit || got[0].TaskN != 1 {
		t.Errorf("first event = %#v", got[0])
	}
	if got[1].Body != "FAIL" {
		t.Errorf("second event Body = %q, want FAIL", got[1].Body)
	}
}

func TestAddRunEventStampsTime(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventConcern, TaskN: 1, Title: "no coverage"})
	if s.RunEvents()[0].At.IsZero() {
		t.Error("AddRunEvent must stamp At so the transcript can time-order it")
	}
}

func TestAddRunEventPreservesExplicitTime(t *testing.T) {
	s := newTestState()
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, At: at})
	if !s.RunEvents()[0].At.Equal(at) {
		t.Error("an explicit At must not be overwritten")
	}
}

func TestClearRunEvents(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit})
	s.ClearRunEvents()
	if len(s.RunEvents()) != 0 {
		t.Error("ClearRunEvents must empty the store")
	}
}

func TestRunEventsAppearInTranscript(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 1, Title: "a3f9e21"})

	var found bool
	for _, item := range s.Transcript() {
		if item.Kind == KindRunEvent {
			if item.RunEvent == nil {
				t.Fatal("KindRunEvent item has a nil RunEvent")
			}
			found = true
		}
	}
	if !found {
		t.Error("run events must appear in Transcript() so they interleave with subagent cards")
	}
}

func TestRunEventsReturnsACopy(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, Title: "original"})
	got := s.RunEvents()
	got[0].Title = "mutated"
	if s.RunEvents()[0].Title != "original" {
		t.Error("RunEvents must return a copy; callers must not mutate store state")
	}
}
