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

// TrustPrompt is the directory whose trust decision is pending, "" when none
// is. It does not change while the host runs, so it is the value the host was
// built with.
func (h *Host) TrustPrompt() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deps.TrustPrompt
}

// DecideTrust records the user's trust decision for dir. Only the directory
// the prompt names can be decided, and only with one of the three decisions.
func (h *Host) DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error {
	h.mu.Lock()
	pending := h.deps.TrustPrompt
	decide := h.deps.DecideTrust
	h.mu.Unlock()
	if pending == "" || decide == nil {
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
	decide(trust.Decision(decision))
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
