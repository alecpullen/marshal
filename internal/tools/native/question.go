package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type questionStore interface {
	SetPendingQuestion(*session.PendingQuestion)
	PendingQuestion() *session.PendingQuestion
}

type questionAskArgs struct {
	Questions []session.Question `json:"questions"`
}

func (t *toolSet) questionAskTool() registry.Tool {
	tool := registry.Tool{
		Name:        "question.ask",
		Description: "Ask the user one or more clarifying questions in a single round-trip. Each question may offer options; otherwise it is free-text.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}},"multi":{"type":"boolean"},"allow_other":{"type":"boolean"}},"required":["question"]}}},"required":["questions"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[questionAskArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if len(args.Questions) == 0 {
			return registry.ToolResult{}, fmt.Errorf("at least one question is required")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("session state not available")
		}
		store := questionStore(t.sessionState)
		ch := make(chan []session.Answer, 1)
		store.SetPendingQuestion(&session.PendingQuestion{
			Questions:    args.Questions,
			ResponseChan: ch,
		})
		answers := <-ch
		store.SetPendingQuestion(nil)
		parts := []string{"You can now continue with the user's answers in mind."}
		for _, a := range answers {
			parts = append(parts, fmt.Sprintf("%q=%q", a.Question, a.Answer))
		}
		return registry.ToolResult{
			Summary: "user answered",
			Content: strings.Join(parts, "\n"),
		}, nil
	}
	return tool
}

func (t *toolSet) askUserTool() registry.Tool {
	tool := registry.Tool{
		Name:        "ask_user",
		Description: "Alias for question.ask with a single free-text question.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode ask_user arguments: %w", err)
		}
		if strings.TrimSpace(args.Question) == "" {
			return registry.ToolResult{}, fmt.Errorf("question string is required")
		}
		newArgs, _ := json.Marshal(map[string]any{
			"questions": []map[string]any{{"question": args.Question}},
		})
		return t.questionAskTool().Handler(ctx, registry.ToolCall{Args: newArgs})
	}
	return tool
}
