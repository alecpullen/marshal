package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

type finalizeReason string

const (
	reasonExhausted finalizeReason = "exhausted"
	reasonStalled   finalizeReason = "stalled"

	// maxFinalizeAttempts bounds how many times finalize will ask the model
	// to comply with FinalizationDirective before giving up and
	// synthesizing a fallback answer. Weaker/local models frequently ignore
	// a single "stop calling tools" directive and emit another tool_call;
	// without a retry, that raw tool_call JSON gets dumped straight into
	// the user-facing salvaged answer (see docs/07, "stalled" completions).
	maxFinalizeAttempts = 3
)

// finalizeCorrectionMessage is appended when the model ignores
// FinalizationDirective and returns a tool_call/patch/actions envelope
// instead of a final answer. It re-states the constraint more forcefully
// than the original directive, since some models need the emphasis
// repeated once before they comply.
const finalizeCorrectionMessage = `Tool calls are disabled for this step — that request will not be executed. Respond again with exactly one JSON object of the form {"rationale": "...", "action": {"type": "final", "content": "..."}} and nothing else. Do not include a tool_call, patch, or actions array.`

// finalizeFinalWarning is used on the last retry inside finalize to make it
// extremely clear to weaker/local models that emitting another tool_call is
// futile. It is appended after finalizeCorrectionMessage on the penultimate
// retry so that the model sees an escalation pattern.
const finalizeFinalWarning = `STOP. Do NOT respond with a tool_call, patch, or actions envelope. The system will ignore it entirely. Respond RIGHT NOW with {"rationale": "...", "action": {"type": "final", "content": "<your best answer based on what you already know>"}} and nothing else. If you truly cannot answer, explain what you would check next inside "content".`

// finalize repeatedly asks the model (up to maxFinalizeAttempts times) to
// produce a final answer with tools disabled, then records the result as a
// salvaged (flagged) completion. It never returns an
// ErrMaxIterationsExceeded-style failure: the only error path is a transport
// failure from chatWithRetry. A model that keeps ignoring the directive
// after all attempts is handled by synthesizing a fallback answer instead of
// surfacing its raw, unparsed tool-call output to the user.
func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason) (*Task, error) {
	final := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: FinalizationDirective})

	var raw string
	content := ""
	for attempt := 0; attempt < maxFinalizeAttempts; attempt++ {
		var err error
		raw, err = r.chatWithRetry(ctx, p, model, final)
		if err != nil {
			return task, err
		}

		if action, parseErr := ParseAction(raw); parseErr == nil &&
			(action.Type == ActionAnswer || action.Type == ActionFinal) {
			content = action.Content
			break
		}

		// No correction after the last attempt — nothing reads it.
		if attempt < maxFinalizeAttempts-1 {
			correction := finalizeCorrectionMessage
			if attempt == maxFinalizeAttempts-2 {
				correction = finalizeFinalWarning
			}
			final = append(final,
				schema.ChatMessage{Role: schema.RoleAssistant, Content: raw},
				schema.ChatMessage{Role: schema.RoleSystem, Content: correction},
			)
		}
	}
	if strings.TrimSpace(content) == "" {
		content = synthesizeFallback(task, raw, reason)
	}

	task.Summary = content
	task.Status = TaskStatusCompleted
	task.SalvagedReason = string(reason)
	r.State.AddMessageSalvaged(session.RoleAssistant, content, session.ContentTypeMarkdown, string(reason))
	return task, nil
}

// extractUsefulProse tries to parse raw as a JSON action envelope. For any
// envelope that parses, it returns only the "rationale" field (the model's
// own justification in human-readable prose) and discards the action
// payload, regardless of action type. If raw does not parse as an envelope,
// the trimmed raw string is returned as-is. An empty string means nothing
// usable could be extracted.
func extractUsefulProse(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	jsonText, err := extractJSONObject(trimmed)
	if err != nil {
		return trimmed
	}
	var env struct {
		Rationale string          `json:"rationale"`
		Action    json.RawMessage `json:"action"`
	}
	if err := json.Unmarshal([]byte(jsonText), &env); err != nil {
		return trimmed
	}
	if r := strings.TrimSpace(env.Rationale); r != "" {
		return r
	}
	return ""
}

// synthesizeFallback builds a best-effort answer when the model refuses to
// conclude. It stitches together any human-readable prose the model emitted
// plus the plan so the user is never left with nothing. Raw tool_call /
// patch JSON is deliberately NOT emitted: the user already sees those tool
// calls in the transcript, and a JSON dump masquerading as a final answer
// is the original bug this function guards against.
func synthesizeFallback(task *Task, raw string, reason finalizeReason) string {
	var b strings.Builder
	switch reason {
	case reasonStalled:
		b.WriteString("I appear to be stuck repeating the same kind of lookup without making progress. Here is my best summary of what I know so far.\n\n")
	default:
		b.WriteString("I ran out of tool budget before fully finishing. Here is my best summary of progress.\n\n")
	}
	if len(task.Plan) > 0 {
		b.WriteString("Plan I was following:\n")
		for _, step := range task.Plan {
			fmt.Fprintf(&b, "- %s\n", step)
		}
		b.WriteString("\n")
	}
	if prose := extractUsefulProse(raw); prose != "" {
		b.WriteString("Reasoning so far:\n")
		b.WriteString(prose)
		b.WriteString("\n")
	}
	return b.String()
}
