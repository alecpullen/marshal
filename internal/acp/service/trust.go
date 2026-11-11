package service

import (
	"context"
	"errors"
	"fmt"

	"marshal/internal/acpwire"
	"marshal/internal/trust"
)

// errNoTrustDecision is what DecideTrust returns when no trust decision is
// pending, and what a directory other than the pending one answers.
var errNoTrustDecision = errors.New("inproc: no trust decision is pending")

// TrustPrompt is the trust prompt's whole answer: the directory whose trust
// decision is pending, "" when none is, and whether a trust applies to the
// running session in place. A settled prompt names no directory.
func (h *Host) TrustPrompt() acpwire.TrustPromptResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	dir := h.deps.TrustPrompt
	if h.trustSettled {
		dir = ""
	}
	return acpwire.TrustPromptResult{Dir: dir, InPlace: dir != "" && h.deps.ApplyTrust != nil}
}

// DecideTrust records the user's trust decision for dir. Only the directory
// the prompt names can be decided, and only with one of the three decisions.
// With Deps.ApplyTrust set the decision applies to the running session in
// place: a trust calls it once and settles the prompt, a decline changes
// nothing, and a failure leaves the prompt pending.
func (h *Host) DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error {
	h.mu.Lock()
	pending := h.deps.TrustPrompt
	decide := h.deps.DecideTrust
	apply := h.deps.ApplyTrust
	h.mu.Unlock()
	if pending == "" || (decide == nil && apply == nil) {
		return errNoTrustDecision
	}
	if dir != pending {
		return fmt.Errorf("inproc: no trust decision is pending for %s", dir)
	}
	switch decision {
	case acpwire.TrustPermanent, acpwire.TrustSession, acpwire.TrustDecline:
	default:
		return fmt.Errorf("inproc: unknown trust decision %q", decision)
	}
	if apply == nil {
		decide(trust.Decision(decision))
		return nil
	}
	// A decline records nothing: the prompt stays pending, so the session
	// asks again.
	if decision == acpwire.TrustDecline {
		return nil
	}
	h.trustMu.Lock()
	defer h.trustMu.Unlock()
	h.mu.Lock()
	settled := h.trustSettled
	h.mu.Unlock()
	// One answer wins: a later answer to a settled prompt succeeds without
	// effect.
	if settled {
		return nil
	}
	if err := apply(ctx, trust.Decision(decision)); err != nil {
		return err
	}
	h.mu.Lock()
	h.trustSettled = true
	h.mu.Unlock()
	return nil
}

// Tools names every tool the session's registry holds, sorted. It reports
// false while the session has no registry. A closed session keeps answering
// with its final value (C4).
func (s *Session) Tools() ([]string, bool) {
	if s.tools == nil {
		return nil, false
	}
	names, ok := s.tools()
	if !ok {
		return nil, false
	}
	// The runtime's names are its own: copy them so a caller cannot change
	// what the next read returns.
	return append([]string(nil), names...), true
}
