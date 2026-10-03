package agent

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"marshal/internal/app/session"
)

// actor describes who is running: the orchestrator (empty role), or a
// pipeline/subagent role with its label, on the route model/provider.
func (r *Runner) actor(model, provider string) session.Actor {
	return session.Actor{Role: r.actorRole(), Label: r.actorLabel(), Model: model, Provider: provider}
}

// actorRole is the role stamped on steps and audits. The general role is the
// orchestrator and is stored as "".
func (r *Runner) actorRole() string {
	if r.role() == RoleGeneral {
		return ""
	}
	return string(r.role())
}

func (r *Runner) actorLabel() string {
	if r.ActorLabel != "" {
		return r.ActorLabel
	}
	return prettyRole(r.actorRole())
}

// prettyRole turns a role value into a label: "sdd_reviewer" → "reviewer",
// "sdd_branch_reviewer" → "branch reviewer".
func prettyRole(role string) string {
	role = strings.TrimPrefix(role, "sdd_")
	return strings.ReplaceAll(role, "_", " ")
}

// beginStep closes any open step and opens one for the model response about
// to be requested.
func (r *Runner) beginStep(model, provider string) {
	r.endOpenStep()
	r.curModel, r.curProvider = model, provider
	r.curStep = r.State.BeginStep(r.actor(model, provider))
}

// endOpenStep ends the step in flight, if any. Deferred from RunTask so a
// turn that returns early still closes its last step.
func (r *Runner) endOpenStep() {
	if r.curStep == 0 {
		return
	}
	r.State.EndStep(r.curStep)
	r.curStep = 0
}

// envelopeCallID synthesises a tool-call ID for the i-th action of the
// current step. Native mode gets real IDs from the provider; envelope mode has
// none, and the TUI and ACP need one to pair a running call with its result.
func (r *Runner) envelopeCallID(i int) string {
	return fmt.Sprintf("s%d-a%d", r.curStep, i)
}

// assignEnvelopeCallIDs fills in missing IDs on a batch before dispatch.
func (r *Runner) assignEnvelopeCallIDs(actions []ModelAction) {
	for i := range actions {
		if actions[i].ToolCallID == "" {
			actions[i].ToolCallID = r.envelopeCallID(i)
		}
	}
}

// actionNarrates reports whether an envelope action's rationale is worth
// showing: it must accompany tool work or a question to the user.
func actionNarrates(a ModelAction) bool {
	switch a.Type {
	case ActionToolCall, ActionPatch, ActionAskUser, ActionQuestionAsk:
		return true
	}
	return len(a.Actions) > 0
}

// intentOpener matches the start of a forward-intent sentence.
var intentOpener = regexp.MustCompile(`(?i)^\s*(i'?ll|i will|let me|next,? i|now i|i'?m going to|going to)\b`)

// looksLikeIntentOnly reports whether a text-only model reply is a short
// statement of what the model means to do next rather than an answer: at most
// 240 runes and two sentence terminators, no code fence, and opening with a
// forward-intent phrase.
func looksLikeIntentOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 240 || strings.Contains(text, "```") {
		return false
	}
	terminators := 0
	for _, r := range text {
		if r == '.' || r == '!' || r == '?' {
			terminators++
		}
	}
	if terminators > 2 {
		return false
	}
	return intentOpener.MatchString(text)
}
