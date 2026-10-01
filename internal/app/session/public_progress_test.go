package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/pubsub"
)

func strptr(s string) *string                                               { return &s }
func sectionsPtr(v ...activity.ProgressSection) *[]activity.ProgressSection { return &v }
func applyProgress(t *testing.T, s *State, r activity.Ref, u activity.ProgressUpdate) PublicProgressReceipt {
	t.Helper()
	got, err := s.ApplyPublicProgress(r, u)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPublicProgressBeginReviseClearAndContinue(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	run := s.BeginActivityRun(boundary)
	r := s.BeginActivityResponse()
	first := applyProgress(t, s, r, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("Investigate"), Body: strptr("Starting"), Sections: sectionsPtr(activity.ProgressSection{Kind: activity.SectionChange, Text: "initial"})})
	if first.Revision != 1 || first.Owner.NarrationID == "" {
		t.Fatalf("begin receipt = %+v", first)
	}
	r2 := s.BeginActivityResponse()
	second := applyProgress(t, s, r2, activity.ProgressUpdate{Mode: activity.ProgressRevise, Body: strptr("Found the cause"), Sections: sectionsPtr()})
	if second.Revision != 2 || second.Owner.NarrationID != first.Owner.NarrationID {
		t.Fatalf("revise receipt = %+v", second)
	}
	r3 := s.BeginActivityResponse()
	noop := applyProgress(t, s, r3, activity.ProgressUpdate{Mode: activity.ProgressRevise, Body: strptr("Found the cause")})
	if noop.Revision != 2 || len(s.ActivitySnapshot().ProgressRevisions) != 2 {
		t.Fatalf("unchanged revise created state: receipt=%+v revisions=%+v", noop, s.ActivitySnapshot().ProgressRevisions)
	}
	if noop.Owner.NarrationID != first.Owner.NarrationID {
		t.Fatalf("unchanged revise receipt owner = %+v, want narration %q", noop.Owner, first.Owner.NarrationID)
	}
	noopReplay := applyProgress(t, s, r3, activity.ProgressUpdate{Mode: activity.ProgressRevise, Body: strptr("Found the cause")})
	if noopReplay.Owner.NarrationID != first.Owner.NarrationID {
		t.Fatalf("idempotent no-op replay lost bound owner: %+v", noopReplay.Owner)
	}
	snap := s.ActivitySnapshot()
	if len(snap.ProgressRevisions) != 2 || len(snap.ProgressRevisions[1].Sections) != 0 || snap.ProgressRevisions[1].Body != "Found the cause" {
		t.Fatalf("revisions = %+v", snap.ProgressRevisions)
	}
	continued := s.BeginActivityResponse()
	if continued.NarrationID != first.Owner.NarrationID {
		t.Fatalf("tool-only continuation lost narration: %+v", continued)
	}
	newResponse := s.BeginActivityResponse()
	third := applyProgress(t, s, newResponse, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("Separate objective")})
	if third.Owner.NarrationID == first.Owner.NarrationID || third.Revision != 1 {
		t.Fatalf("new begin reused narration: %+v", third)
	}
	if !snap.Narrations[0].Closed && s.ActivitySnapshot().Narrations[0].ID == third.Owner.NarrationID {
		t.Fatal("prior narration was not closed neutrally")
	}
	s.EndActivityRun(run)
}

func TestPublicProgressIdempotencyScopeAndWarnings(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	run := s.BeginActivityRun(boundary)
	r := s.BeginActivityResponse()
	u := activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("Work"), Sections: sectionsPtr(activity.ProgressSection{Kind: activity.SectionEvidence, Text: "Observed output", EvidenceRefs: []string{"pending-alias"}})}
	one := applyProgress(t, s, r, u)
	if len(one.Warnings) != 1 || one.Warnings[0] != "some evidence references are unavailable" {
		t.Fatalf("warnings = %v", one.Warnings)
	}
	before := len(s.Messages())
	again := applyProgress(t, s, r, u)
	if again.Revision != one.Revision || len(s.Messages()) != before {
		t.Fatal("duplicate response appended state")
	}
	if again.Owner.NarrationID != one.Owner.NarrationID {
		t.Fatalf("idempotent replay owner = %+v, want narration %q", again.Owner, one.Owner.NarrationID)
	}
	if _, err := s.ApplyPublicProgress(r, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("Different")}); !errors.Is(err, ErrProgressConflict) {
		t.Fatalf("conflicting update error = %v", err)
	}
	foreign := newTestState()
	addActivityBoundary(foreign)
	foreign.BeginActivityRun(addActivityBoundary(foreign))
	if _, err := foreign.ApplyPublicProgress(r, u); !errors.Is(err, ErrProgressScope) {
		t.Fatalf("foreign response error = %v", err)
	}
	s.EndActivityRun(run)
	if _, err := s.ApplyPublicProgress(r, u); !errors.Is(err, ErrProgressScope) {
		t.Fatalf("ended run error = %v", err)
	}
}

func TestPublicProgressPublishesAfterReleasingLifecycleLock(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	run := s.BeginActivityRun(boundary)
	response := s.BeginActivityResponse()
	broker := pubsub.NewBroker[Event]()
	s.SetEventBroker(broker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx, pubsub.WithBufferSize[Event](0), pubsub.WithTerminal[Event]())
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		<-events
		// This lifecycle operation takes progressMu. It would deadlock if the
		// publisher still held that lock while waiting for this subscriber.
		s.EndActivityRun(run)
	}()
	applyDone := make(chan error, 1)
	go func() {
		_, err := s.ApplyPublicProgress(response, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("Work")})
		applyDone <- err
	}()
	select {
	case err := <-applyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress apply deadlocked while a terminal subscriber entered lifecycle code")
	}
	select {
	case <-consumerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle subscriber did not finish")
	}
}

func TestPublicProgressRevisionsAreCopiedAndBranchScoped(t *testing.T) {
	s := newTestState()
	boundary := addActivityBoundary(s)
	s.BeginActivityRun(boundary)
	r := s.BeginActivityResponse()
	applyProgress(t, s, r, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("A"), Sections: sectionsPtr(activity.ProgressSection{Kind: activity.SectionChange, Text: "original", EvidenceRefs: []string{"x"}})})
	firstSource := s.Messages()[len(s.Messages())-1].ID
	r2 := s.BeginActivityResponse()
	applyProgress(t, s, r2, activity.ProgressUpdate{Mode: activity.ProgressRevise, Body: strptr("new")})
	snap := s.ActivitySnapshot()
	snap.ProgressRevisions[0].Sections[0].Text = "mutated"
	snap.ProgressRevisions[0].Headline = "mutated"
	if got := s.ActivitySnapshot().ProgressRevisions[0]; got.Headline != "A" || got.Sections[0].Text != "original" {
		t.Fatalf("snapshot mutation leaked: %+v", got)
	}
	s.Rewind(firstSource)
	if got := len(s.ActivitySnapshot().ProgressRevisions); got != 0 {
		t.Fatalf("rewind retained %d revisions", got)
	}
}

func TestPublicProgressIndependentConcurrentStates(t *testing.T) {
	a, b := newTestState(), newTestState()
	ba := addActivityBoundary(a)
	bb := addActivityBoundary(b)
	a.BeginActivityRun(ba)
	b.BeginActivityRun(bb)
	ra, rb := a.BeginActivityResponse(), b.BeginActivityResponse()
	ca := applyProgress(t, a, ra, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("A")})
	cb := applyProgress(t, b, rb, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: strptr("B")})
	if ca.Owner.NarrationID == cb.Owner.NarrationID {
		t.Fatalf("child states collided: %q", ca.Owner.NarrationID)
	}
}
