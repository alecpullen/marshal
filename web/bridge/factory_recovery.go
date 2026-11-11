package bridge

import (
	"context"
	"fmt"
	"strings"
)

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

func recoveryStopConfirmed(item FactoryItem, obs FactoryObservation) bool {
	for _, w := range item.Workers {
		if !w.Parked && (!obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || !obs.WorkIdle) {
			return false
		}
	}
	return true
}

// recoveryOutcome accepts only evidence that identifies the exact effect.
// There is no ACP completion receipt for an interrupted model prompt.
func (c *FactoryController) recoveryOutcome(item FactoryItem, e FactoryEffect, o FactoryObservation) (FactoryOutcome, bool) {
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
	case "seed", "publish":
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
	default:
		return out, false
	}
	return out, true
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
			if out, ok := c.recoveryOutcome(item, e, obs); ok {
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
