package session

import "testing"

// TestASecondClaimIsRefused pins the run slot's exclusivity (P4 D2): while one
// claim is held a second reports false, and a claim after the release succeeds.
func TestASecondClaimIsRefused(t *testing.T) {
	s := newTestState()

	release, ok := s.ClaimRun()
	if !ok {
		t.Fatal("the first claim was refused on a free session")
	}
	if _, ok := s.ClaimRun(); ok {
		t.Fatal("a second claim succeeded while the first was held")
	}
	release()
	if _, ok := s.ClaimRun(); !ok {
		t.Fatal("a claim after the release was refused")
	}
}

// TestAReleaseIsIdempotent pins the release's contract: releasing twice frees
// the slot once, so the first holder's second release cannot free a later
// holder's claim.
func TestAReleaseIsIdempotent(t *testing.T) {
	s := newTestState()

	first, ok := s.ClaimRun()
	if !ok {
		t.Fatal("the first claim was refused on a free session")
	}
	first()
	first()

	second, ok := s.ClaimRun()
	if !ok {
		t.Fatal("a claim after the first release was refused")
	}
	// The first holder releases again: the slot is the second holder's now, and
	// an idempotent release must not touch it.
	first()
	if _, ok := s.ClaimRun(); ok {
		t.Fatal("a claim succeeded after a stale release freed the second holder's claim")
	}

	second()
	if _, ok := s.ClaimRun(); !ok {
		t.Fatal("a claim after the second release was refused")
	}
}

// TestANilStateClaimsNothing pins the nil tolerance the run paths rely on: a
// runtime with no state (the fakes some tests build) takes no claim.
func TestANilStateClaimsNothing(t *testing.T) {
	var s *State
	release, ok := s.ClaimRun()
	if !ok {
		t.Fatal("a nil state refused the claim")
	}
	release()
}
