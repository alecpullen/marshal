package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// factoryContinuationEvidence is what a worker session's own transcript proves
// about one delegated continuation. Known is false for a transcript the bridge
// could not read, and a transcript that proves no dispatch is the only thing
// that authorizes sending one: an unread session is never evidence of absence.
type factoryContinuationEvidence struct {
	Known      bool
	Dispatched bool
	Accepted   bool
	Report     string
	Turn       string
}

// factoryDelegatedEffect is the effect whose turn handed back delegated work.
// Only the newest effect can still be waiting: an earlier turn settled before
// the next effect was prepared.
func factoryDelegatedEffect(i FactoryItem) (FactoryEffect, bool) {
	if len(i.Effects) == 0 {
		return FactoryEffect{}, false
	}
	e := i.Effects[len(i.Effects)-1]
	if e.Delegated == nil {
		return FactoryEffect{}, false
	}
	if e.Status != "dispatched" && e.Status != "uncertain" {
		return FactoryEffect{}, false
	}
	return e, true
}

// factoryDelegatedSettled reports whether every child the delegated turn waited
// on is proven terminal by its own session's projection. A projection the
// bridge could not read, a child it does not name and a state it does not name
// as finished are all unknown work, so none of them is settled.
func factoryDelegatedSettled(d FactoryDelegatedTurn, obs FactoryObservation) bool {
	var projection *FactoryChildSession
	for n := range obs.Children {
		if obs.Children[n].SessionID == d.SessionID {
			projection = &obs.Children[n]
			break
		}
	}
	if projection == nil || !projection.Known {
		return false
	}
	terminal := make(map[int64]bool, len(projection.Children))
	for _, child := range projection.Children {
		terminal[child.ID] = child.Terminal
	}
	for _, id := range d.ChildIDs {
		if done, ok := terminal[id]; !ok || !done {
			return false
		}
	}
	return true
}

// factoryContinuationID names one continuation iteration. It is derived from the
// turn it continues, the children it waited on and the continuation that came
// before it, so the same iteration always derives the same identity — which is
// what makes a repeated attempt a conflict rather than a second prompt — while
// every further round is a new identity.
func factoryContinuationID(e FactoryEffect) string {
	d := e.Delegated
	if d == nil {
		return ""
	}
	ids := make([]string, len(d.ChildIDs))
	for n, id := range d.ChildIDs {
		ids[n] = strconv.FormatInt(id, 10)
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{e.ID, d.WorkerID, d.SessionID, d.ParentTurnID, strings.Join(ids, ","), d.ContinuationID}, "\x00")))
	return "cont-" + hex.EncodeToString(digest[:8])
}

// Recover examines every unfinished item before opening the dispatch gate.
// Ambiguous effects retain their ownership and need an explicit decision.
func (c *FactoryController) Recover(ctx context.Context) error {
	if c.store == nil || c.effects == nil {
		return ErrFactoryUnavailable
	}
	for _, item := range c.store.List() {
		if item.State == "draft" || item.State == "closed" || item.State == "stopped" {
			continue
		}
		if fx, ok := c.effects.(*factoryEffects); ok && fx.fleet != nil {
			for _, w := range item.Workers {
				if !w.Parked {
					_ = fx.fleet.reattachFactoryWorker(ctx, item.ID, w)
				}
			}
		}
		if _, err := c.reconcile(ctx, item, false, ""); err != nil {
			return err
		}
	}
	c.admitAfterRecovery()
	return nil
}

// factorySeedDidNotRun reports a clean checkout still at the approved base,
// which proves the seed commit never landed.
func factorySeedDidNotRun(item FactoryItem, o FactoryObservation) bool {
	return o.Error == "" && !o.Dirty && item.BaseSHA != "" && o.HeadSHA == item.BaseSHA
}

func recoveryStopConfirmed(item FactoryItem, obs FactoryObservation) bool {
	for _, w := range item.Workers {
		if !w.Parked && (!obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || !obs.WorkIdle) {
			return false
		}
	}
	// The children a turn launched are its work too: a stop is confirmed only
	// when each one is proven terminal by its own session's projection. An
	// unread projection is unknown work, never a settled stop.
	if e, ok := factoryDelegatedEffect(item); ok && len(e.Delegated.ChildIDs) > 0 && !factoryDelegatedSettled(*e.Delegated, obs) {
		return false
	}
	return true
}

// recoveryOutcome accepts only evidence that identifies the exact effect.
// There is no ACP completion receipt for an interrupted model prompt. A
// continuation is accepted only on the proof its own session's transcript
// carries, which the caller reads before calling this.
func (c *FactoryController) recoveryOutcome(item FactoryItem, e FactoryEffect, o FactoryObservation, proof factoryContinuationEvidence) (FactoryOutcome, bool) {
	out := FactoryOutcome{EffectID: e.ID, Confirmed: true}
	switch e.Kind {
	case "spawn":
		var matches []FactoryWorker
		for _, w := range o.Workers {
			if w.SpawnEffectID == e.ID && w.Role == e.WorkerRole && w.AgentID != "" && w.SessionID != "" {
				matches = append(matches, w)
			}
		}
		if len(matches) != 1 {
			return out, false
		}
		out.Worker = &matches[0]
	case "seed":
		// Seed creates the owned branch and commits locally; it never pushes.
		// A remote head is therefore not evidence of a seed, and requiring one
		// would leave every seed effect permanently unconfirmed. The seed
		// commit landing on a clean checkout is the evidence.
		if o.Error != "" || o.Dirty || o.HeadSHA == "" || o.TreeSHA == "" || o.HeadSHA == item.BaseSHA {
			return out, false
		}
		out.HeadSHA, out.TreeSHA = o.HeadSHA, o.TreeSHA
	case "publish":
		if o.Error != "" || o.Dirty || o.HeadSHA == "" || o.TreeSHA == "" || o.RemoteHead != o.HeadSHA {
			return out, false
		}
		if e.ExpectedHead != "" && o.RemoteHead == e.ExpectedHead {
			return out, false
		}
		out.HeadSHA, out.TreeSHA = o.HeadSHA, o.TreeSHA
	case "create_pr":
		if len(item.Approvals) == 0 || o.Error != "" || o.PR == nil || o.PR.Number <= 0 || !o.PR.Draft || o.PR.State != "open" || o.PR.HeadRef != item.Branch || o.PR.BaseRef != item.Approvals[len(item.Approvals)-1].Config.TargetBranch || o.PR.HeadSHA == "" {
			return out, false
		}
		out.PR = o.PR
	case "park", "resume":
		if e.Worker == nil {
			return out, false
		}
		found := false
		for _, w := range o.Workers {
			matchingSession := w.SessionID == e.Worker.SessionID
			if e.Kind == "resume" && factoryFreshResume(item, e) {
				matchingSession = w.SessionID != "" && w.SessionID != e.Worker.SessionID
			}
			if w.AgentID == e.Worker.AgentID && matchingSession && w.SpawnEffectID == e.Worker.SpawnEffectID && w.Parked == (e.Kind == "park") {
				found = true
				if e.Kind == "resume" {
					out.Worker = &w
				}
			}
		}
		if !found {
			return out, false
		}
	case "set_ready", "set_draft":
		if o.Error != "" || o.PR == nil || o.PR.Number <= 0 || o.PR.HeadSHA != e.ExpectedHead || o.PR.BaseSHA != e.ExpectedBase || o.PR.Draft != (e.Kind == "set_draft") {
			return out, false
		}
		out.PR = o.PR
	case "implement", "review":
		if e.Delegated == nil {
			// An ordinary worker prompt has no completion receipt: its report
			// reached the journal or it did not.
			return out, false
		}
		return c.recoveryContinuationOutcome(item, e, o, proof)
	default:
		return out, false
	}
	return out, true
}

// recoveryContinuationOutcome accepts a continuation's report only when the
// worker session's own transcript proves the dispatch ran and wrote a fresh
// report naming this continuation, for this worker and this candidate. A prompt
// that was sent with no proof stays unknown: it is never accepted and never
// re-sent.
func (c *FactoryController) recoveryContinuationOutcome(item FactoryItem, e FactoryEffect, o FactoryObservation, proof factoryContinuationEvidence) (FactoryOutcome, bool) {
	out := FactoryOutcome{EffectID: e.ID, Confirmed: true}
	d := e.Delegated
	if d == nil || e.Worker == nil || d.ContinuationID == "" || !proof.Accepted {
		return out, false
	}
	// A report is never accepted while children a continuation launched remain
	// running or unknown.
	if len(d.ChildIDs) > 0 && !factoryDelegatedSettled(*d, o) {
		return out, false
	}
	var worker *FactoryWorker
	for n := range o.Workers {
		if o.Workers[n].AgentID == d.WorkerID {
			worker = &o.Workers[n]
			break
		}
	}
	if worker == nil || worker.SessionID != d.SessionID || worker.Role != e.Worker.Role {
		return out, false
	}
	report, err := ParseFactoryReport(proof.Report, e.ID, worker.Role, e.ExpectedBase, e.ExpectedHead)
	if err != nil {
		return out, false
	}
	out.Report = &report
	out.Worker = worker
	out.Continuation = d.ContinuationID
	if report.Outcome == "needs_input" {
		return out, true
	}
	out.BaseSHA = item.BaseSHA
	if worker.Role == "reviewer" {
		if o.Dirty || o.HeadSHA == "" || o.HeadSHA != e.ExpectedHead {
			return out, false
		}
		out.HeadSHA, out.TreeSHA = o.HeadSHA, o.TreeSHA
		return out, true
	}
	if o.Error != "" || o.Dirty || o.HeadSHA == "" || o.TreeSHA == "" {
		return out, false
	}
	out.HeadSHA, out.TreeSHA = o.HeadSHA, o.TreeSHA
	return out, true
}

// factoryContinuationSeams is the two reads a delegated continuation needs from
// an adapter: what the worker session's own transcript proves, and how to run
// the second turn that report is asked for.
type factoryContinuationSeams interface {
	ContinuationEvidence(context.Context, FactoryItem, FactoryEffect) (factoryContinuationEvidence, error)
	Continue(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error)
}

func (c *FactoryController) continuationSeams() (factoryContinuationSeams, bool) {
	seams, ok := c.effects.(factoryContinuationSeams)
	return seams, ok
}

// reconcileDelegated reconciles a turn that handed back delegated work. A
// waiting turn resumes its cycle; a prepared continuation is sent only when the
// session provably has none, and a dispatched one is accepted only on the fresh
// report that names it. Everything else becomes uncertain and surfaces the
// existing recovery decision: nothing here re-sends a prompt on a guess, and a
// report is never accepted while new children remain running or unknown.
func (c *FactoryController) reconcileDelegated(ctx context.Context, item FactoryItem, e FactoryEffect, obs FactoryObservation) (FactoryItem, error) {
	d := *e.Delegated
	if item.State == "stopping" {
		// A stop owns this turn's children: cancel them and wait for terminal
		// outcomes. The stop path above completes the effect once they are
		// proven stopped.
		return c.stopDelegated(ctx, item, e, obs)
	}
	switch d.State {
	case "", factoryDelegatedWaiting:
		// The turn is still waiting on its children: the scheduler resumes the
		// cycle, and no prompt is sent from here.
		return item, nil
	case factoryDelegatedPrepared:
		proof, err := c.continuationEvidence(ctx, item, e)
		if err != nil || !proof.Known {
			return c.delegatedUncertain(item, e, "continuation dispatch cannot be proven absent: "+factoryReason(err))
		}
		if proof.Dispatched {
			return c.delegatedUncertain(item, e, "continuation was dispatched without a recorded dispatch")
		}
		return c.dispatchPreparedContinuation(ctx, item, e)
	case factoryDelegatedDispatched, factoryDelegatedUncertain:
		proof, err := c.continuationEvidence(ctx, item, e)
		if err == nil {
			if out, ok := c.recoveryOutcome(item, e, obs, proof); ok {
				next, completeErr := CompleteFactoryEffect(factoryCopy(item), e.ID, out, c.now())
				if completeErr != nil {
					return c.delegatedUncertain(item, e, "continuation report rejected: "+completeErr.Error())
				}
				next.Version = item.Version
				next.Decision = nil
				next.History = append(next.History, FactoryEvent{At: c.now(), Kind: "continuation_accepted", EffectID: e.ID, Message: d.ContinuationID})
				return c.internalUpdate(item, "continuation_accepted", func(dst *FactoryItem) error { *dst = next; dst.Version = item.Version; return nil })
			}
		}
		if d.State == factoryDelegatedUncertain {
			return item, nil
		}
		return c.delegatedUncertain(item, e, "continuation outcome unconfirmed: "+factoryReason(err))
	}
	return c.delegatedUncertain(item, e, "continuation state "+d.State+" is not reconcilable")
}

// stopDelegated asks the children this turn owns to stop and waits for their
// terminal outcomes. An acknowledgement is not one, so the item keeps stopping
// until the projection says each child finished.
func (c *FactoryController) stopDelegated(ctx context.Context, item FactoryItem, e FactoryEffect, obs FactoryObservation) (FactoryItem, error) {
	if !factoryDelegatedSettled(*e.Delegated, obs) {
		if err := c.cancelDelegatedChildren(ctx, item); err != nil {
			_, _ = c.internalUpdate(item, "stop_cancel_children", func(dst *FactoryItem) error {
				dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "stop_cancel_children_uncertain", EffectID: e.ID, Message: err.Error()})
				return nil
			})
		}
	}
	if recoveryStopConfirmed(item, obs) {
		return c.internalUpdate(item, "stop_confirmed_idle", func(dst *FactoryItem) error {
			last := &dst.Effects[len(dst.Effects)-1]
			last.Status = "completed"
			last.Outcome = &FactoryOutcome{EffectID: e.ID, Error: "stopped after turn and child work observed terminal", Confirmed: false}
			dst.Decision = nil
			dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "stop_observed_idle", EffectID: e.ID, Message: "turn and every child observed terminal; prompt outcome unknown"})
			return nil
		})
	}
	return c.internalUpdate(item, "stop_wait_children", func(dst *FactoryItem) error {
		factoryDecision(dst, "recovery", "waiting for the turn's child work to stop", c.now())
		dst.State = "stopping"
		return nil
	})
}

// delegatedUncertain records a continuation whose dispatch or outcome the
// bridge cannot prove. The effect stops being schedulable and the existing
// recovery decision is what surfaces it; nothing is retried from here.
func (c *FactoryController) delegatedUncertain(item FactoryItem, e FactoryEffect, reason string) (FactoryItem, error) {
	return c.internalUpdate(item, "continuation_uncertain", func(dst *FactoryItem) error {
		last := &dst.Effects[len(dst.Effects)-1]
		if last.ID != e.ID || last.Delegated == nil {
			return ErrFactoryConflict
		}
		next := *last.Delegated
		next.State = factoryDelegatedUncertain
		last.Delegated = &next
		last.Status = "uncertain"
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "continuation_uncertain", EffectID: e.ID, Message: reason})
		factoryDecision(dst, "recovery", reason, c.now())
		return nil
	})
}

// continuationEvidence reads the transcript proof one delegated continuation
// needs. An adapter that cannot read it proves nothing.
func (c *FactoryController) continuationEvidence(ctx context.Context, item FactoryItem, e FactoryEffect) (factoryContinuationEvidence, error) {
	seams, ok := c.continuationSeams()
	if !ok {
		return factoryContinuationEvidence{}, fmt.Errorf("%w: continuation evidence unsupported", ErrFactoryUncertain)
	}
	return seams.ContinuationEvidence(ctx, item, e)
}

// factoryReason renders an error for a journaled decision.
func factoryReason(err error) string {
	if err == nil {
		return "no proof in the worker session's transcript"
	}
	return err.Error()
}

func (c *FactoryController) reconcile(ctx context.Context, item FactoryItem, attest bool, reason string) (FactoryItem, error) {
	if fx, ok := c.effects.(*factoryEffects); ok && fx.fleet != nil {
		for _, w := range item.Workers {
			if !w.Parked {
				_ = fx.fleet.reattachFactoryWorker(ctx, item.ID, w)
			}
		}
	}
	obs, observeErr := c.effects.Observe(ctx, item)
	if observeErr != nil {
		obs.Error = observeErr.Error()
	}
	if len(item.Effects) > 0 {
		e := item.Effects[len(item.Effects)-1]
		if e.Status == "dispatched" || e.Status == "uncertain" {
			if item.State == "stopping" && (e.Kind == "implement" || e.Kind == "review") && len(item.Workers) > 0 && recoveryStopConfirmed(item, obs) {
				return c.internalUpdate(item, "stop_confirmed_idle", func(dst *FactoryItem) error {
					last := &dst.Effects[len(dst.Effects)-1]
					last.Status = "completed"
					last.Outcome = &FactoryOutcome{EffectID: e.ID, Error: "stopped after turn and active work observed idle", Confirmed: false}
					dst.Decision = nil
					dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "stop_observed_idle", EffectID: e.ID, Message: "turn and active work observed idle; prompt outcome unknown"})
					return nil
				})
			}
			// A turn that handed back delegated work is reconciled against the
			// continuation it journaled, never against the observation alone.
			if e.Delegated != nil {
				return c.reconcileDelegated(ctx, item, e, obs)
			}
			if out, ok := c.recoveryOutcome(item, e, obs, factoryContinuationEvidence{}); ok {
				// CompleteFactoryEffect only accepts a dispatched intent.
				copy := factoryCopy(item)
				copy.Effects[len(copy.Effects)-1].Status = "dispatched"
				next, err := CompleteFactoryEffect(copy, e.ID, out, c.now())
				if err == nil {
					if (e.Kind == "spawn" || e.Kind == "resume") && !obs.RuntimeKnown {
						factoryDecision(&next, "recovery", "worker found; runtime and active work remain unconfirmed", c.now())
						next.State = "recovering"
					} else if item.State == "recovering" && next.State == "recovering" {
						next.State = factoryStateAfterRecovery(e)
					}
					if next.State != "recovering" && next.Decision != nil && next.Decision.Kind == "recovery" {
						next.Decision = nil
					}
					return c.internalUpdate(item, "reconciled", func(dst *FactoryItem) error { *dst = next; dst.Version = item.Version; return nil })
				}
			}
			if e.Kind == "seed" && factorySeedDidNotRun(item, obs) {
				// A clean checkout still at the approved base proves the seed
				// never ran. Record a definite failure so the operator can
				// retry it instead of leaving the item uncertain forever.
				copy := factoryCopy(item)
				copy.Effects[len(copy.Effects)-1].Status = "dispatched"
				next, err := CompleteFactoryEffect(copy, e.ID, FactoryOutcome{
					EffectID:  e.ID,
					Error:     "seed did not run: checkout is clean at the approved base",
					Confirmed: true,
				}, c.now())
				if err == nil {
					return c.internalUpdate(item, "seed_not_run", func(dst *FactoryItem) error { *dst = next; dst.Version = item.Version; return nil })
				}
			}
			if attest && e.Kind != "spawn" && len(item.Workers) > 0 && strings.Contains(reason, item.Workers[len(item.Workers)-1].AgentID) && !obs.RuntimeKnown {
				known := item.Workers[len(item.Workers)-1]
				return c.internalUpdate(item, "attested", func(dst *FactoryItem) error {
					last := &dst.Effects[len(dst.Effects)-1]
					last.Status = "completed"
					last.Outcome = &FactoryOutcome{EffectID: e.ID, Error: "operator attested previous worker stopped: " + known.AgentID + "; " + reason, Confirmed: false}
					dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "operator_attestation", EffectID: e.ID, Message: "previous worker " + known.AgentID + " stopped: " + reason})
					factoryDecision(dst, "worker_failed", "Previous worker stopped; retry requires additional allowance", c.now())
					return nil
				})
			}
			return c.internalUpdate(item, "recovering", func(dst *FactoryItem) error {
				stopping := dst.State == "stopping"
				msg := fmt.Sprintf("%s outcome or worker ownership unconfirmed", e.Kind)
				if obs.Error != "" {
					msg += ": " + obs.Error
				}
				factoryDecision(dst, "recovery", msg, c.now())
				if stopping {
					dst.State = "stopping"
				} else {
					dst.State = "recovering"
				}
				return nil
			})
		}
	}
	if item.State == "stopping" && !recoveryStopConfirmed(item, obs) {
		return c.internalUpdate(item, "stop_wait", func(dst *FactoryItem) error {
			factoryDecision(dst, "recovery", "waiting for turn and active work to stop", c.now())
			dst.State = "stopping"
			return nil
		})
	}
	for _, w := range item.Workers {
		if !w.Parked && (!obs.RuntimeKnown || !obs.ActiveWorkKnown || !obs.TurnIdle || !obs.WorkIdle) {
			return c.internalUpdate(item, "worker_unknown", func(dst *FactoryItem) error {
				factoryDecision(dst, "recovery", "previous worker or detached work remains unconfirmed: "+w.AgentID, c.now())
				dst.State = "recovering"
				return nil
			})
		}
	}
	if obs.Error != "" {
		return c.internalUpdate(item, "observe_unknown", func(dst *FactoryItem) error {
			factoryDecision(dst, "recovery", obs.Error, c.now())
			dst.State = "recovering"
			return nil
		})
	}
	if item.State == "recovering" && item.Decision != nil && item.Decision.Kind == "recovery" && len(item.Effects) > 0 {
		return c.internalUpdate(item, "recovered", func(dst *FactoryItem) error {
			dst.Decision = nil
			dst.State = factoryStateAfterRecovery(dst.Effects[len(dst.Effects)-1])
			return nil
		})
	}
	return item, nil
}

func factoryStateAfterRecovery(e FactoryEffect) string {
	switch e.Kind {
	case "spawn", "seed", "create_pr":
		return "preparing"
	case "implement":
		if e.Round > 1 {
			return "repairing"
		}
		return "implementing"
	case "review":
		return "reviewing"
	default:
		return "checking"
	}
}

func factoryLastAttested(i FactoryItem) bool {
	if len(i.Effects) == 0 {
		return false
	}
	e := i.Effects[len(i.Effects)-1]
	if e.Status != "completed" || e.Outcome == nil || e.Outcome.Confirmed || !strings.HasPrefix(e.Outcome.Error, "operator attested previous worker stopped: ") {
		return false
	}
	for _, h := range i.History {
		if h.Kind == "operator_attestation" && h.EffectID == e.ID {
			return true
		}
	}
	return false
}

func factoryFreshResume(i FactoryItem, e FactoryEffect) bool {
	if e.Kind != "resume" || len(i.Effects) < 2 || i.Effects[len(i.Effects)-1].ID != e.ID {
		return false
	}
	previous := factoryCopy(i)
	previous.Effects = previous.Effects[:len(previous.Effects)-1]
	return factoryLastAttested(previous)
}
