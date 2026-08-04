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
		Description: "Ask the user one or more clarifying questions in a single round-trip. Use this tool only when you need information or a decision from the user in order to proceed. Rules: (1) If a question has a known set of choices, you MUST pass them in the options field — never embed lettered or numbered choices (e.g. 'A) ... B) ... C) ...') in the question text; options render as a selectable list in the UI. Set multi to true when several choices may apply, and allow_other to true when a custom answer is plausible. (2) Never use this tool to deliver instructions (e.g. asking the user to set an environment variable, run a command, or edit a file) — write instructions in your normal message text instead. (3) Never use this tool to request a permission-mode switch (default/plan/edit/copilot/auto) — call the mode.request tool instead; the user cannot change modes while a question popup is open.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}},"multi":{"type":"boolean"},"allow_other":{"type":"boolean"}},"required":["question"],"additionalProperties":false}}},"required":["questions"],"additionalProperties":false}`),
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
		Description: "Ask the user a single free-text question (alias for question.ask with one question and no options). Use it only when you need information or a decision from the user. If the question has a known set of choices, call question.ask with the options field instead. Never use it to deliver instructions — state those in your normal message text — and never to request a permission-mode switch; call mode.request for that.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"],"additionalProperties":false}`),
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
