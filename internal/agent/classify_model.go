package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

const classifyDirective = `Classify the user's request into exactly one category:
- "question": read-only — the user wants an answer or explanation.
- "edit": the user wants files created or modified.
- "command": the user wants a command or build/test run executed.
Reply with exactly one word: question, edit, or command.`

const classifyCallTimeout = 10 * time.Second

// NewModelClassifier returns a Runner.Classifier hook backed by a one-shot,
// no-tools call to the given provider/model (the router role). The call goes
// straight to the provider — never through Runner.Run, so it cannot recurse
// into classification. Any failure or unrecognized reply returns an error so
// the caller keeps the keyword-derived class.
func NewModelClassifier(p provider.Provider, model string) func(context.Context, string) (TaskClass, error) {
	return func(ctx context.Context, goal string) (TaskClass, error) {
		ctx, cancel := context.WithTimeout(ctx, classifyCallTimeout)
		defer cancel()
		res, err := provider.ChatText(ctx, p, schema.ChatRequest{
			Model: model,
			Messages: []schema.ChatMessage{
				{Role: schema.RoleSystem, Content: classifyDirective},
				{Role: schema.RoleUser, Content: goal},
			},
		})
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(res)) {
		case string(ClassQuestion):
			return ClassQuestion, nil
		case string(ClassEdit):
			return ClassEdit, nil
		case string(ClassCommand):
			return ClassCommand, nil
		}
		return "", fmt.Errorf("unrecognized classifier reply %q", res)
	}
}
