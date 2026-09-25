package native

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
		Description: "Ask the user one or more clarifying questions in a single round-trip. Use this tool only when you need information or a decision from the user in order to proceed. Rules: (1) If a question has a known set of choices, you MUST pass them in the options field — never embed lettered or numbered choices (e.g. 'A) ... B) ... C) ...') in the question text; options render as a selectable list in the UI. The user can always supply a custom answer outside the list via the Other affordance. (2) Never use this tool to deliver instructions (e.g. asking the user to set an environment variable, run a command, or edit a file) — write instructions in your normal message text instead. (3) Never use this tool to request a permission-mode switch (default/plan/edit/copilot/auto) — call the mode.request tool instead; the user cannot change modes while a question popup is open. Example: {\"questions\":[{\"question\":\"Pick one\",\"options\":[\"simple\",{\"label\":\"rich\",\"description\":\"with explanation\"}]}]}",
		Schema:      json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"}},"required":["label"],"additionalProperties":false}]}},"multi":{"type":"boolean"}},"required":["question"],"additionalProperties":false}}},"required":["questions"],"additionalProperties":false}`),
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

const questionAskExample = `{"questions":[{"question":"Pick one","options":["simple",{"label":"rich","description":"more detail"}]}]}`

// unknownQuestionKeys returns the sorted keys of m that are not in allowed,
// so a rejection names every offending property deterministically rather
// than whichever one map iteration happened to reach first.
func unknownQuestionKeys(m map[string]json.RawMessage, allowed ...string) []string {
	var out []string
	for k := range m {
		permitted := false
		for _, a := range allowed {
			if k == a {
				permitted = true
				break
			}
		}
		if !permitted {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateQuestionAsk replaces the generic JSON schema check with a
// hand-rolled walk so the rejection error names the exact field and
// embeds a valid example.
//
// It deliberately checks types as well as presence: a payload whose
// 'question' or option 'label' is present but not a string would otherwise
// sail through here and fail later in the runner's own decode with a
// generic "arguments are not valid JSON", which is exactly the unhelpful
// message this validator exists to replace.
//
// Unknown properties are rejected, matching the declared schema's
// additionalProperties:false. The two paths must agree: a single call goes
// through this validator, while the same tool inside an actions[] batch
// goes through registry.ValidateArgs against the schema. Tolerating a
// stray key here would make the identical payload succeed as a single call
// and fail in a batch.
func ValidateQuestionAsk(raw json.RawMessage) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("question.ask arguments must be an object with a 'questions' array: %v\n\nexample: %s", err, questionAskExample)
	}
	if extra := unknownQuestionKeys(probe, "questions"); len(extra) > 0 {
		return fmt.Errorf("question.ask arguments contain unknown properties: %s\n\nexample: %s", strings.Join(extra, ", "), questionAskExample)
	}
	rawQuestions, ok := probe["questions"]
	if !ok {
		return fmt.Errorf("question.ask arguments must be an object with a 'questions' array\n\nexample: %s", questionAskExample)
	}
	var questions []map[string]json.RawMessage
	if err := json.Unmarshal(rawQuestions, &questions); err != nil {
		return fmt.Errorf("questions must be an array; got %s\n\nexample: %s", string(rawQuestions), questionAskExample)
	}
	if len(questions) == 0 {
		return fmt.Errorf("questions must be a non-empty array\n\nexample: %s", questionAskExample)
	}
	for i, q := range questions {
		if extra := unknownQuestionKeys(q, "question", "options", "multi"); len(extra) > 0 {
			return fmt.Errorf("questions[%d] contains unknown properties: %s\n\nexample: %s", i, strings.Join(extra, ", "), questionAskExample)
		}
		rawQ, ok := q["question"]
		if !ok {
			return fmt.Errorf("questions[%d] is missing required field 'question'\n\nexample: %s", i, questionAskExample)
		}
		var question string
		if err := json.Unmarshal(rawQ, &question); err != nil {
			return fmt.Errorf("questions[%d].question must be a string; got %s\n\nexample: %s", i, string(rawQ), questionAskExample)
		}
		if rawMulti, ok := q["multi"]; ok {
			var multi bool
			if err := json.Unmarshal(rawMulti, &multi); err != nil {
				return fmt.Errorf("questions[%d].multi must be a boolean; got %s\n\nexample: %s", i, string(rawMulti), questionAskExample)
			}
		}
		if rawOpts, ok := q["options"]; ok {
			var arr []json.RawMessage
			if err := json.Unmarshal(rawOpts, &arr); err != nil {
				return fmt.Errorf("questions[%d].options must be an array; got %s\n\nexample: %s", i, string(rawOpts), questionAskExample)
			}
			for j, item := range arr {
				// json.Unmarshal treats null as a no-op success for every
				// target type, so null would otherwise take the string
				// fast-path and decode as an empty label.
				if strings.TrimSpace(string(item)) == "null" {
					return fmt.Errorf("questions[%d].options[%d] must be a string or an object with label/description; got null\n\nexample: %s", i, j, questionAskExample)
				}
				var s string
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(item, &s); err != nil {
					if err2 := json.Unmarshal(item, &obj); err2 != nil {
						return fmt.Errorf("questions[%d].options[%d] must be a string or an object with label/description; got %s\n\nexample: %s", i, j, string(item), questionAskExample)
					}
					if extra := unknownQuestionKeys(obj, "label", "description"); len(extra) > 0 {
						return fmt.Errorf("questions[%d].options[%d] contains unknown properties: %s\n\nexample: %s", i, j, strings.Join(extra, ", "), questionAskExample)
					}
					rawLabel, hasLabel := obj["label"]
					if !hasLabel {
						return fmt.Errorf("questions[%d].options[%d] object is missing required field 'label'\n\nexample: %s", i, j, questionAskExample)
					}
					var label string
					if err := json.Unmarshal(rawLabel, &label); err != nil {
						return fmt.Errorf("questions[%d].options[%d].label must be a string; got %s\n\nexample: %s", i, j, string(rawLabel), questionAskExample)
					}
					// The decoder rejects an object with an empty label, so the
					// validator must too — otherwise the payload sails through
					// here and fails later as "arguments are not valid JSON".
					if label == "" {
						return fmt.Errorf("questions[%d].options[%d].label must not be empty\n\nexample: %s", i, j, questionAskExample)
					}
					if rawDesc, ok := obj["description"]; ok {
						var desc string
						if err := json.Unmarshal(rawDesc, &desc); err != nil {
							return fmt.Errorf("questions[%d].options[%d].description must be a string; got %s\n\nexample: %s", i, j, string(rawDesc), questionAskExample)
						}
					}
				}
			}
		}
	}
	return nil
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
