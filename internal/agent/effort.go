package agent

import (
	"fmt"

	"marshal/internal/app/session"
)

// crossesOffBoundary reports whether moving from current to target
// effort turns thinking on or off ("" counts as on: provider default).
func crossesOffBoundary(current, target string) bool {
	return (current == "off") != (target == "off")
}

// sessionEffortTarget returns the effort the session asks for on this
// route, and whether the override applies to it at all.
func (r *Runner) sessionEffortTarget(presetThinking, providerName, model string) (string, bool) {
	if r.ThinkingOverride != "" || r.State == nil {
		return "", false
	}
	o := r.State.EffortOverride()
	if o.Level == "" {
		return presetThinking, true
	}
	if o.Provider != providerName || o.Model != model {
		return "", false
	}
	return o.Level, true
}

// applySessionEffortAtStep moves the turn to the session's requested effort
// when that is a level change, and leaves off-boundary changes for the next
// turn. step is the 1-based number of the step about to run.
func (r *Runner) applySessionEffortAtStep(presetThinking, providerName, model string, step int, deferredNotice *string) {
	target, ok := r.sessionEffortTarget(presetThinking, providerName, model)
	current := r.turnRequestOptions.thinking
	if !ok || target == current {
		return
	}
	if crossesOffBoundary(current, target) {
		if *deferredNotice != target {
			*deferredNotice = target
			r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Effort %q takes effect from the next turn (thinking is not switched on or off mid-turn).", target), session.ContentTypePlain)
			r.State.PublishEffortChange(session.EffortChange{Effective: current, Pending: target, AppliesFrom: session.EffortAppliesNextTurn})
		}
		return
	}
	r.turnRequestOptions.thinking = target
	if turn, running := r.State.TurnRoute(); running {
		turn.Thinking = target
		r.State.SetTurnRoute(turn)
	}
	label := target
	if label == "" {
		label = "default"
	}
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Effort → %s from step %d.", label, step), session.ContentTypePlain)
	r.State.PublishEffortChange(session.EffortChange{Effective: target, AppliesFrom: session.EffortAppliesNextStep})
}
