package app

import (
	"context"
	"testing"

	"marshal/internal/acpwire"
)

// TestEndServiceRoundTrips pins the transport seam over the runtime's end: the
// end service converts the runtime's shapes into acpwire's neutral mirrors, and
// every decision still lands on the runtime.
func TestEndServiceRoundTrips(t *testing.T) {
	rt := startEndRuntime(t)
	end := &endService{rt: rt}

	if got := end.ActiveWork(); len(got.Items) != 0 {
		t.Fatalf("ActiveWork() = %+v, want an empty membership on a fresh runtime", got)
	}

	// Register real conversation work, so the service reports a real membership
	// rather than a synthesised one.
	_, finish, err := rt.BeginWork(context.Background())
	if err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	defer finish()

	work := end.ActiveWork()
	if len(work.Items) != 1 {
		t.Fatalf("ActiveWork() items = %d, want the registered operation", len(work.Items))
	}
	// The item carries an opaque ID and the runtime's own label: the service
	// copies the runtime's entry rather than inventing one.
	entry := rt.ActiveWork().Items[0]
	if work.Items[0].ID == "" {
		t.Fatalf("work item = %+v, want an opaque ID", work.Items[0])
	}
	if work.Items[0].ID != entry.ID || work.Items[0].Label != entry.Label {
		t.Fatalf("work item = %+v, want the runtime's own %+v", work.Items[0], entry)
	}
	if rt.EndAccepted() {
		t.Fatal("EndAccepted() = true before any acceptance")
	}

	// An acceptance requiring a displayed set must refuse when the membership
	// was not displayed — and the service must relay the refusal faithfully.
	refused, err := end.AcceptEnd(acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{"something-else"},
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if refused.Accepted {
		t.Fatal("the service reported an acceptance of an undisplayed membership")
	}
	if len(refused.Expanded) != 1 || refused.Expanded[0] != work.Items[0].ID {
		t.Fatalf("Expanded = %v, want the undisplayed ID %q", refused.Expanded, work.Items[0].ID)
	}
	if rt.EndAccepted() {
		t.Fatal("a refused acceptance froze the runtime")
	}

	// The real acceptance succeeds and reports the membership it stopped.
	accepted, err := end.AcceptEnd(acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{work.Items[0].ID},
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("accepted = false, want true (expanded: %v)", accepted.Expanded)
	}
	if len(accepted.Work.Items) != 1 {
		t.Fatalf("accepted membership = %d items, want 1", len(accepted.Work.Items))
	}
	if !rt.EndAccepted() {
		t.Fatal("EndAccepted() = false after an acceptance")
	}
}
