package session

import (
	"testing"
	"time"
)

func TestActivityResponsesWithEqualTimestampsHaveDistinctOwnership(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	run := s.BeginActivityRun(boundary)
	one := s.BeginActivityResponse()
	one = s.BindActivityNarration(one, "first")
	s.AddNarrationMessage(one, "first")
	two := s.BeginActivityResponse()
	two = s.BindActivityNarration(two, "second")
	s.AddNarrationMessage(two, "second")
	// Equal source timestamps ensure ownership comes from recorded identity,
	// never timestamp matching.
	s.mu.Lock()
	stamp := time.Unix(123, 0)
	for i := range s.messages {
		if s.messages[i].ContentType == ContentTypeNarration {
			s.messages[i].CreatedAt = stamp
		}
	}
	s.mu.Unlock()
	if one.ResponseID == two.ResponseID || one.NarrationID == two.NarrationID || one.RunID != run.RunID || two.RunID != run.RunID {
		t.Fatalf("response refs collided or lost ownership: first=%+v second=%+v", one, two)
	}
	snap := s.ActivitySnapshot()
	if len(snap.Narrations) != 2 || snap.Narrations[0].Sequence >= snap.Narrations[1].Sequence {
		t.Fatalf("narration records = %+v, want two distinct append-ordered records", snap.Narrations)
	}
	if n, ok := snap.NarrationForResponse(two.ResponseID); !ok || n.ID != two.NarrationID {
		t.Fatalf("latest response resolves to (%+v, %v), want narration %q", n, ok, two.NarrationID)
	}
}

func TestActivityIDsAreIndependentBetweenStates(t *testing.T) {
	a, b := newTestState(), newTestState()
	ra := a.BeginActivityRun(addActivityBoundary(a))
	rb := b.BeginActivityRun(addActivityBoundary(b))
	if ra.RunID == rb.RunID {
		t.Fatalf("independent states share run ID %q", ra.RunID)
	}
	if a.BeginActivityResponse().ResponseID == b.BeginActivityResponse().ResponseID {
		t.Fatal("independent states share response ID")
	}
}

func TestActivityResponseWithoutNarrationContinuesCurrentNarration(t *testing.T) {
	s := newTestState()
	s.BeginActivityRun(addActivityBoundary(s))
	first := s.BeginActivityResponse()
	first = s.BindActivityNarration(first, "working")
	s.AddNarrationMessage(first, "working")
	continued := s.BeginActivityResponse() // response has no new prose
	if continued.NarrationID != first.NarrationID {
		t.Fatalf("continued narration = %q, want %q", continued.NarrationID, first.NarrationID)
	}
	if n, ok := s.ActivitySnapshot().NarrationForResponse(continued.ResponseID); !ok || n.ID != first.NarrationID {
		t.Fatalf("response mapping = (%+v, %v), want current narration", n, ok)
	}
}

func TestVisibleUserBoundaryClearsActiveNarrationForFollowOnResponse(t *testing.T) {
	s := newTestState()
	firstBoundary := addActivityBoundary(s)
	s.BeginActivityRun(firstBoundary)
	first := s.BeginActivityResponse()
	first = s.BindActivityNarration(first, "working")
	s.AddNarrationMessage(first, "working")
	s.AddMessage(RoleUser, "steer", ContentTypeSteering)
	steeringFollowOn := s.BeginActivityResponse()
	if steeringFollowOn.NarrationID != first.NarrationID {
		t.Fatalf("steering reset active narration to %q, want %q", steeringFollowOn.NarrationID, first.NarrationID)
	}
	s.AddMessage(RoleUser, "internal report", ContentTypeSubagentReport)
	reportFollowOn := s.BeginActivityResponse()
	if reportFollowOn.NarrationID != first.NarrationID {
		t.Fatalf("internal report reset active narration to %q, want %q", reportFollowOn.NarrationID, first.NarrationID)
	}

	// A real visible user message within the run starts a new segment. Internal
	// steering/report message types are excluded by appendMessageActivity.
	s.AddMessage(RoleUser, "continue with this detail", ContentTypePlain)
	followOn := s.BeginActivityResponse()
	if followOn.NarrationID != "" {
		t.Fatalf("follow-on response inherited old narration %q", followOn.NarrationID)
	}
	if _, ok := s.ActivitySnapshot().NarrationForResponse(followOn.ResponseID); ok {
		t.Fatal("narration-free follow-on response resolved to a previous narration")
	}
}

func TestActivitySnapshotRequiresNarrationSourceOnActivePath(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	s.BeginActivityRun(boundary)
	r := s.BeginActivityResponse()
	r = s.BindActivityNarration(r, "branch narration")
	sourceID := s.AddNarrationMessage(r, "branch narration")
	if got := len(s.ActivitySnapshot().Narrations); got != 1 {
		t.Fatalf("snapshot before rewind has %d narrations, want 1", got)
	}
	s.Rewind(sourceID)
	if got := len(s.ActivitySnapshot().Narrations); got != 0 {
		t.Fatalf("snapshot after removing source message has %d narrations, want 0", got)
	}
}

func TestFreshActivityRunAfterCompletionDoesNotInheritNarration(t *testing.T) {
	s := newTestState()
	firstRun := s.BeginActivityRun(addActivityBoundary(s))
	response := s.BeginActivityResponse()
	response = s.BindActivityNarration(response, "done")
	s.AddNarrationMessage(response, "done")
	s.EndActivityRun(firstRun)
	secondRun := s.BeginActivityRun(addActivityBoundary(s))
	second := s.BeginActivityResponse()
	if firstRun.RunID == secondRun.RunID || second.NarrationID != "" {
		t.Fatalf("fresh run inherited ownership: first=%+v second=%+v", firstRun, second)
	}
}

func TestActivitySnapshotIsCopied(t *testing.T) {
	s := newTestState()
	s.BeginActivityRun(addActivityBoundary(s))
	r := s.BeginActivityResponse()
	r = s.BindActivityNarration(r, "original")
	s.AddNarrationMessage(r, "original")
	snap := s.ActivitySnapshot()
	snap.Narrations[0].Text = "mutated"
	snap.Narrations[0].ID = "other"
	snap.Response.NarrationID = "other"
	snap.ResponseNarrations[0].NarrationID = "other"
	live := s.ActivitySnapshot()
	if live.Narrations[0].Text != "original" || live.Narrations[0].ID != r.NarrationID {
		t.Fatalf("snapshot mutation reached live state: %+v", live)
	}
}

// AddBoundaryForActivityTest creates a real source boundary without making
// tests depend on message timestamps or presentation IDs.
func addActivityBoundary(s *State) int64 {
	s.AddMessage(RoleUser, "request", ContentTypePlain)
	return s.Messages()[0].ID
}
