// internal/acp/service/trust_test.go
package service

// The trust prompt, its decision and the tools read moved to the conformance
// suite, which runs each case against this package and against the client over
// a pipe (P3 plan 7a, V1). These cases pin the in-process host alone: the
// prompt's InPlace half and the in-place apply's settling and failure rules
// (P4 D8).

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/trust"
)

// trustState builds the state every case runs over.
func trustState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: "s1"})
}

// trustRecorder is the fake apply and recorder the cases share: it appends
// every decision it is given and returns the scripted error.
type trustRecorder struct {
	applied []trust.Decision
	decided []trust.Decision
	err     error
}

func (r *trustRecorder) apply(_ context.Context, d trust.Decision) error {
	r.applied = append(r.applied, d)
	return r.err
}

func (r *trustRecorder) decide(d trust.Decision) {
	r.decided = append(r.decided, d)
}

// TestTheEmbeddedPromptIsNotInPlace: without Deps.ApplyTrust the prompt names
// the directory and is not in place, the decision reaches Deps.DecideTrust as
// it always has, and the prompt stays pending (embedded mode, P4 W6).
func TestTheEmbeddedPromptIsNotInPlace(t *testing.T) {
	dir := t.TempDir()
	rec := &trustRecorder{}
	host, sess := New(Deps{
		State:       trustState(t),
		TrustPrompt: dir,
		DecideTrust: rec.decide,
	})
	t.Cleanup(sess.Release)

	if got := host.TrustPrompt(); got.Dir != dir || got.InPlace {
		t.Fatalf("TrustPrompt() = %+v, want %q and not in place", got, dir)
	}
	if err := host.DecideTrust(context.Background(), dir, acpwire.TrustPermanent); err != nil {
		t.Fatalf("DecideTrust: %v", err)
	}
	if !reflect.DeepEqual(rec.decided, []trust.Decision{trust.DecisionTrustPermanent}) {
		t.Fatalf("recorded decisions = %v, want one permanent", rec.decided)
	}
	if got := host.TrustPrompt(); got.Dir != dir || got.InPlace {
		t.Fatalf("TrustPrompt() after the decision = %+v, want it still pending", got)
	}
}

// TestATrustAppliedInPlaceSettlesThePrompt: with Deps.ApplyTrust the prompt is
// in place, a trust reaches the apply once, and the prompt then names no
// directory.
func TestATrustAppliedInPlaceSettlesThePrompt(t *testing.T) {
	dir := t.TempDir()
	rec := &trustRecorder{}
	host, sess := New(Deps{
		State:       trustState(t),
		TrustPrompt: dir,
		ApplyTrust:  rec.apply,
	})
	t.Cleanup(sess.Release)

	if got := host.TrustPrompt(); got.Dir != dir || !got.InPlace {
		t.Fatalf("TrustPrompt() = %+v, want %q in place", got, dir)
	}
	if err := host.DecideTrust(context.Background(), dir, acpwire.TrustPermanent); err != nil {
		t.Fatalf("DecideTrust: %v", err)
	}
	if !reflect.DeepEqual(rec.applied, []trust.Decision{trust.DecisionTrustPermanent}) {
		t.Fatalf("applied decisions = %v, want one permanent", rec.applied)
	}
	if got := host.TrustPrompt(); got.Dir != "" || got.InPlace {
		t.Fatalf("TrustPrompt() after the apply = %+v, want no prompt", got)
	}
}

// TestASecondAnswerChangesNothing: one answer wins, so a later trust succeeds
// without effect.
func TestASecondAnswerChangesNothing(t *testing.T) {
	dir := t.TempDir()
	rec := &trustRecorder{}
	host, sess := New(Deps{
		State:       trustState(t),
		TrustPrompt: dir,
		ApplyTrust:  rec.apply,
	})
	t.Cleanup(sess.Release)

	ctx := context.Background()
	if err := host.DecideTrust(ctx, dir, acpwire.TrustPermanent); err != nil {
		t.Fatalf("first DecideTrust: %v", err)
	}
	if err := host.DecideTrust(ctx, dir, acpwire.TrustSession); err != nil {
		t.Fatalf("second DecideTrust: %v", err)
	}
	if !reflect.DeepEqual(rec.applied, []trust.Decision{trust.DecisionTrustPermanent}) {
		t.Fatalf("applied decisions = %v, want only the first", rec.applied)
	}
}

// TestAFailedApplyKeepsThePromptPending: a failed apply returns its error and
// leaves the prompt pending, so the next decision tries again.
func TestAFailedApplyKeepsThePromptPending(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("the config does not load")
	rec := &trustRecorder{err: boom}
	host, sess := New(Deps{
		State:       trustState(t),
		TrustPrompt: dir,
		ApplyTrust:  rec.apply,
	})
	t.Cleanup(sess.Release)

	ctx := context.Background()
	if err := host.DecideTrust(ctx, dir, acpwire.TrustPermanent); !errors.Is(err, boom) {
		t.Fatalf("DecideTrust = %v, want the apply's error", err)
	}
	if got := host.TrustPrompt(); got.Dir != dir || !got.InPlace {
		t.Fatalf("TrustPrompt() after the failure = %+v, want it still pending", got)
	}

	rec.err = nil
	if err := host.DecideTrust(ctx, dir, acpwire.TrustPermanent); err != nil {
		t.Fatalf("second DecideTrust: %v", err)
	}
	if len(rec.applied) != 2 {
		t.Fatalf("applied decisions = %v, want the failed one and the retry", rec.applied)
	}
}

// TestAnInPlaceDeclineKeepsThePromptPending: a decline calls nothing and
// leaves the prompt as it was.
func TestAnInPlaceDeclineKeepsThePromptPending(t *testing.T) {
	dir := t.TempDir()
	rec := &trustRecorder{}
	host, sess := New(Deps{
		State:       trustState(t),
		TrustPrompt: dir,
		ApplyTrust:  rec.apply,
	})
	t.Cleanup(sess.Release)

	if err := host.DecideTrust(context.Background(), dir, acpwire.TrustDecline); err != nil {
		t.Fatalf("DecideTrust with a decline: %v", err)
	}
	if len(rec.applied) != 0 {
		t.Fatalf("applied decisions = %v, want none", rec.applied)
	}
	if got := host.TrustPrompt(); got.Dir != dir || !got.InPlace {
		t.Fatalf("TrustPrompt() after the decline = %+v, want it still pending", got)
	}
}
