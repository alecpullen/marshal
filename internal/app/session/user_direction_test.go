package session

import (
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
)

// These tests cover the session's half of user direction: the ledger a trusted
// confirmation writes into, and the exact operation each entry covers. The
// receipt's id is the bridge's — nothing here mints one — so what is proven is
// that a session records a confirmation only for a verified association, for one
// exact operation, once, and never past its expiry or across a generation.

// userDirectionState is a state on a frozen clock with the given Studio
// association already recorded.
func userDirectionState(t *testing.T, now time.Time, assoc MarshalSession) *State {
	t.Helper()
	s := New(config.Default(), "/repo", now, Persistence{}, WithClock(func() time.Time { return now }))
	if assoc != (MarshalSession{}) {
		s.RecordMarshalSession(assoc)
	}
	return s
}

func verifiedAssociation() MarshalSession {
	return MarshalSession{InstanceID: "instance-1", Generation: "gen-1", ProjectID: "project-1"}
}

// confirmation is one operation's confirmation request, valid at now+minute.
func confirmation(now time.Time) UserDirectionRequest {
	return UserDirectionRequest{
		ID:        "receipt-1",
		ExpiresAt: now.Add(time.Minute),
		ProjectID: "project-1",
		Action:    "approveAndQueue",
		Target:    "item-1\x000\x00",
		RequestID: "approve-1",
	}
}

// TestMarshalUserDirectionSessionReceipt proves one confirmation authorizes
// exactly one attempt of exactly the operation it names.
func TestMarshalUserDirectionSessionReceipt(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := userDirectionState(t, now, verifiedAssociation())

	req := confirmation(now)
	recorded, err := s.ConfirmUserDirection(req)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if recorded.ID != req.ID || recorded.Generation != "gen-1" {
		t.Fatalf("recorded receipt = %+v", recorded)
	}

	taken, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID)
	if !ok {
		t.Fatal("the confirmed operation found no receipt")
	}
	if taken.ID != req.ID {
		t.Fatalf("took receipt %q, want %q", taken.ID, req.ID)
	}
	// One confirmation authorizes one attempt.
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); ok {
		t.Fatal("the receipt was taken twice")
	}
}

// TestMarshalUserDirectionSessionUnverified proves a session with no verified
// association, or one acting on another project, records nothing.
func TestMarshalUserDirectionSessionUnverified(t *testing.T) {
	now := time.Unix(1000, 0).UTC()

	unverified := userDirectionState(t, now, MarshalSession{})
	if _, err := unverified.ConfirmUserDirection(confirmation(now)); !errors.Is(err, errUserDirectionUnverified) {
		t.Fatalf("an unverified session confirmed a receipt: %v", err)
	}

	// A partly claimed association is not a verified one.
	partial := userDirectionState(t, now, MarshalSession{InstanceID: "instance-1", Generation: "gen-1"})
	if _, err := partial.ConfirmUserDirection(confirmation(now)); !errors.Is(err, errUserDirectionUnverified) {
		t.Fatalf("a partly claimed association confirmed a receipt: %v", err)
	}

	// A confirmation for a project other than the verified one is refused
	// rather than rebound to it.
	other := confirmation(now)
	other.ProjectID = "project-2"
	verified := userDirectionState(t, now, verifiedAssociation())
	if _, err := verified.ConfirmUserDirection(other); !errors.Is(err, errUserDirectionUnverified) {
		t.Fatalf("a confirmation for another project was recorded: %v", err)
	}
}

// TestMarshalUserDirectionSessionExpiry proves an expired receipt is not
// evidence, whether it lapses before the confirmation or after it.
func TestMarshalUserDirectionSessionExpiry(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := userDirectionState(t, now, verifiedAssociation())

	// Already past its expiry when the confirmation arrives.
	stale := confirmation(now)
	stale.ExpiresAt = now.Add(-time.Second)
	if _, err := s.ConfirmUserDirection(stale); !errors.Is(err, errUserDirectionExpired) {
		t.Fatalf("an expired receipt was confirmed: %v", err)
	}

	// Confirmed in time, but lapsed before it was used: dropped, not returned.
	req := confirmation(now)
	req.ID = "receipt-2"
	if _, err := s.ConfirmUserDirection(req); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); ok {
		t.Fatal("an expired receipt was handed back")
	}
	// It is dropped, not merely refused: the ledger holds nothing either way.
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); ok {
		t.Fatal("an expired receipt survived in the ledger")
	}
}

// TestMarshalUserDirectionSessionGenerationRevokes proves a new association
// revokes what the old generation's confirmations covered.
func TestMarshalUserDirectionSessionGenerationRevokes(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := userDirectionState(t, now, verifiedAssociation())

	req := confirmation(now)
	if _, err := s.ConfirmUserDirection(req); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// The same association recorded again keeps the ledger.
	s.RecordMarshalSession(verifiedAssociation())
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); !ok {
		t.Fatal("re-recording the same association revoked a live receipt")
	}

	// A new generation revokes every receipt the old one held.
	s.RecordMarshalSession(MarshalSession{InstanceID: "instance-1", Generation: "gen-2", ProjectID: "project-1"})
	req.ID = "receipt-3"
	if _, err := s.ConfirmUserDirection(req); err != nil {
		t.Fatalf("confirm on the new generation: %v", err)
	}
	next := MarshalSession{InstanceID: "instance-1", Generation: "gen-3", ProjectID: "project-1"}
	s.RecordMarshalSession(next)
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); ok {
		t.Fatal("a receipt survived a generation change")
	}
}

// TestMarshalUserDirectionSessionRequiresOneOperation proves a confirmation
// that does not name exactly one operation records nothing, and that
// re-confirming one operation replaces its entry instead of adding a second.
func TestMarshalUserDirectionSessionRequiresOneOperation(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := userDirectionState(t, now, verifiedAssociation())

	for name, mutate := range map[string]func(*UserDirectionRequest){
		"no receipt id": func(r *UserDirectionRequest) { r.ID = " " },
		"no project":    func(r *UserDirectionRequest) { r.ProjectID = "" },
		"no action":     func(r *UserDirectionRequest) { r.Action = "" },
		"no target":     func(r *UserDirectionRequest) { r.Target = "" },
		"no request id": func(r *UserDirectionRequest) { r.RequestID = "" },
	} {
		req := confirmation(now)
		mutate(&req)
		if _, err := s.ConfirmUserDirection(req); !errors.Is(err, errUserDirectionInvalid) {
			t.Fatalf("%s: confirmation = %v, want errUserDirectionInvalid", name, err)
		}
	}

	// Re-confirming the same operation replaces its entry: one operation owes
	// one confirmation, and a replay cannot leave a second attempt available.
	req := confirmation(now)
	if _, err := s.ConfirmUserDirection(req); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := s.ConfirmUserDirection(req); err != nil {
		t.Fatalf("re-confirm: %v", err)
	}
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); !ok {
		t.Fatal("the re-confirmed operation found no receipt")
	}
	if _, ok := s.TakeUserDirectionReceipt(req.ProjectID, req.Action, req.Target, req.RequestID); ok {
		t.Fatal("a replayed confirmation left a second attempt available")
	}
}
