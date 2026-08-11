package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/jsonextract"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

type finalizeReason string

const (
	reasonExhausted  finalizeReason = "exhausted"
	reasonOverhead   finalizeReason = "overhead_exhausted"
	reasonStalled    finalizeReason = "stalled"
	reasonMalformed  finalizeReason = "malformed"
	reasonEmpty      finalizeReason = "empty"
	reasonUnverified finalizeReason = "unverified"

	// maxFinalizeAttempts bounds how many times finalize will ask the model
	// to comply with FinalizationDirective (or NativeFinalizationDirective in
	// native tool-calling mode) before giving up and synthesizing a fallback
	// answer. Weaker/local models frequently ignore a single "stop calling
	// tools" directive and emit another tool_call; without a retry, that raw
	// tool_call JSON gets dumped straight into the user-facing salvaged answer
	// (see docs/07, "stalled" completions).
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

// nativeFinalizeCorrectionMessage is appended when the model ignores the
// native finalization directive and emits tool calls even though no tools
// were offered. It restates the prose-only constraint in native vocabulary.
const nativeFinalizeCorrectionMessage = `Do not call tools. Respond with a concise final answer in normal prose.`

// nativeFinalizeFinalWarning is the escalated version of
// nativeFinalizeCorrectionMessage for the penultimate retry.
const nativeFinalizeFinalWarning = `STOP. Do NOT call tools. Respond RIGHT NOW with a concise final answer in normal prose based on what you already know.`

// nativeFinalizeEmptyResponsePrompt is appended when the model returns an
// empty response in native finalize mode. It asks for a concise prose answer.
const nativeFinalizeEmptyResponsePrompt = `Please respond with a concise final answer in normal prose.`

// nativeToolCallDisabledReply is the content used for the required role:tool
// result message when a native finalize attempt returns tool calls even though
// no tools were offered. Providers that enforce tool-result ordering reject
// requests where an assistant message lists ToolCalls without matching tool
// results, so we must answer every tool_call_id before the next model turn.
const nativeToolCallDisabledReply = `Tool calls are disabled for this step. Respond with a concise final answer in normal prose.`

// finalize repeatedly asks the model (up to maxFinalizeAttempts times) to
// produce a final answer with tools disabled, then records the result as a
// salvaged (flagged) completion. It never returns an
// ErrMaxIterationsExceeded-style failure: the only error path is a transport
// failure from chatWithRetry. A model that keeps ignoring the directive
// after all attempts is handled by synthesizing a fallback answer instead of
// surfacing its raw, unparsed tool-call output to the user.
func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason, responseFormat *schema.ResponseFormat) (*Task, error) {
	directive := FinalizationDirective
	if r.NativeTools {
		directive = NativeFinalizationDirective
	}
	final := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: directive})

	var raw string
	content := ""
	for attempt := 0; attempt < maxFinalizeAttempts; attempt++ {
		var err error
		res, err := r.chatWithRetryNoNativeTools(ctx, p, model, final, responseFormat)
		if err != nil {
			return task, err
		}
		raw = res.Text

		if r.NativeTools {
			// In native mode a response that contains tool calls is a request to
			// act, not a final answer, even if it also has accompanying text.
			// Correct the model and retry.
			if len(res.ToolCalls) > 0 {
				final = append(final,
					schema.ChatMessage{Role: schema.RoleAssistant, Content: raw, ToolCalls: res.ToolCalls},
				)
				// Providers that enforce tool-result ordering require exactly one
				// role:tool message per tool_call_id before the next model turn.
				for _, call := range res.ToolCalls {
					final = append(final, schema.ChatMessage{
						Role:       schema.RoleTool,
						ToolCallID: call.ID,
						Content:    nativeToolCallDisabledReply,
					})
				}
				if attempt < maxFinalizeAttempts-1 {
					correction := nativeFinalizeCorrectionMessage
					if attempt == maxFinalizeAttempts-2 {
						correction = nativeFinalizeFinalWarning
					}
					final = append(final,
						schema.ChatMessage{Role: schema.RoleSystem, Content: correction},
					)
				}
				continue
			}

			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				content = trimmed
				break
			}

			// Empty response; ask for prose.
			if attempt < maxFinalizeAttempts-1 {
				final = append(final,
					schema.ChatMessage{Role: schema.RoleAssistant, Content: raw},
					schema.ChatMessage{Role: schema.RoleSystem, Content: nativeFinalizeEmptyResponsePrompt},
				)
			}
			continue
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
	jsonText, err := jsonextract.Extract(trimmed)
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
	case reasonMalformed:
		b.WriteString("The model kept producing output I could not parse. Here is the best answer I can construct from the work completed so far.\n\n")
	case reasonEmpty:
		b.WriteString("The model stopped producing output. Here is the best summary I can construct from the work completed so far.\n\n")
	case reasonOverhead:
		b.WriteString(fmt.Sprintf("I hit the overhead turn cap (%d turns with no tool progress) before fully finishing. Here is my best summary of progress.\n\n", maxOverheadTurns))
	case reasonExhausted:
		b.WriteString("I ran out of tool budget before fully finishing. Increase agent.max_tool_iterations for a longer budget. Here is my best summary of progress.\n\n")
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
