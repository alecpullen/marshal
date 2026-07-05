package agent

import (
	"context"
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
)

// finalize makes one no-tools model call that must produce a final answer, then
// records it as a salvaged (flagged) completion. It never returns an
// ErrMaxIterationsExceeded-style failure: the only error path is a transport
// failure from chatWithRetry. A model that ignores the directive and tries to
// call a tool anyway is handled by synthesizing a fallback answer.
func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason) (*Task, error) {
	final := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: FinalizationDirective})

	raw, err := r.chatWithRetry(ctx, p, model, final)
	if err != nil {
		return task, err
	}

	content := ""
	if action, parseErr := ParseAction(raw); parseErr == nil &&
		(action.Type == ActionAnswer || action.Type == ActionFinal) {
		content = action.Content
	}
	if strings.TrimSpace(content) == "" {
		content = synthesizeFallback(task, raw)
	}

	task.Summary = content
	task.Status = TaskStatusCompleted
	task.SalvagedReason = string(reason)
	r.State.AddMessageSalvaged(session.RoleAssistant, content, session.ContentTypeMarkdown, string(reason))
	return task, nil
}

// synthesizeFallback builds a best-effort answer when the model refuses to
// conclude. It stitches together any prose the model emitted plus the plan so
// the user is never left with nothing.
func synthesizeFallback(task *Task, raw string) string {
	var b strings.Builder
	b.WriteString("I ran out of tool budget before fully finishing. Here is my best summary of progress.\n\n")
	if len(task.Plan) > 0 {
		b.WriteString("Plan I was following:\n")
		for _, step := range task.Plan {
			fmt.Fprintf(&b, "- %s\n", step)
		}
		b.WriteString("\n")
	}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		b.WriteString("Latest model output:\n")
		b.WriteString(trimmed)
	}
	return b.String()
}
