package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

type ActionType string

const (
	ActionAnswer      ActionType = "answer"
	ActionToolCall    ActionType = "tool_call"
	ActionPatch       ActionType = "patch"
	ActionFinal       ActionType = "final"
	ActionAskUser     ActionType = "ask_user"
	ActionQuestionAsk ActionType = "question.ask"
)

var (
	ErrNoActionFound     = errors.New("agent: no JSON action object found in model output")
	ErrUnknownActionType = errors.New("agent: unknown action type")
	ErrMissingTool       = errors.New("agent: tool_call action missing tool name")
	ErrMissingQuestion   = errors.New("agent: ask_user action missing question content")
)

// ModelAction is the parsed form of the JSON action-protocol envelope
// described in docs/07-agent-runtime-and-swarm.md. When Actions is set,
// the single-action fields are empty and vice-versa.
type ModelAction struct {
	Rationale  string
	Type       ActionType
	Tool       string
	Args       json.RawMessage
	Content    string
	ToolCallID string
	Actions    []ModelAction      // parallel read-only tool calls
	Questions  []session.Question // structured question payload (question.ask)
}

type actionEnvelope struct {
	Rationale string          `json:"rationale"`
	Action    actionPayload   `json:"action"`
	Actions   []actionPayload `json:"actions,omitempty"`
}

type actionPayload struct {
	Type      ActionType         `json:"type"`
	Tool      string             `json:"tool,omitempty"`
	Args      json.RawMessage    `json:"args,omitempty"`
	Content   string             `json:"content,omitempty"`
	Questions []session.Question `json:"questions,omitempty"`
}

// ParseAction extracts and validates the JSON action envelope. It tolerates a
// leading/trailing ```json fence, since local models frequently wrap JSON in
// markdown even when told not to.
func ParseAction(raw string) (ModelAction, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return ModelAction{}, err
	}

	var envelope actionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return ModelAction{}, fmt.Errorf("agent: malformed action JSON: %w", err)
	}

	if len(envelope.Actions) > 0 {
		actions := make([]ModelAction, 0, len(envelope.Actions))
		for _, p := range envelope.Actions {
			ma, err := validatePayload(p)
			if err != nil {
				return ModelAction{}, err
			}
			actions = append(actions, ma)
		}
		return ModelAction{Rationale: envelope.Rationale, Actions: actions}, nil
	}

	ma, err := validatePayload(envelope.Action)
	if err != nil {
		return ModelAction{}, err
	}
	return ModelAction{
		Rationale: envelope.Rationale,
		Type:      ma.Type,
		Tool:      ma.Tool,
		Args:      ma.Args,
		Content:   ma.Content,
		Questions: envelope.Action.Questions,
	}, nil
}

func validatePayload(p actionPayload) (ModelAction, error) {
	switch p.Type {
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal, ActionAskUser, ActionQuestionAsk:
	default:
		return ModelAction{}, fmt.Errorf("%w: %q", ErrUnknownActionType, p.Type)
	}
	if p.Type == ActionToolCall && strings.TrimSpace(p.Tool) == "" {
		return ModelAction{}, ErrMissingTool
	}
	if p.Type == ActionAskUser && strings.TrimSpace(p.Content) == "" {
		return ModelAction{}, ErrMissingQuestion
	}
	if p.Type == ActionQuestionAsk && len(p.Questions) == 0 {
		return ModelAction{}, fmt.Errorf("agent: question.ask action requires at least one question")
	}
	return ModelAction{Type: p.Type, Tool: p.Tool, Args: p.Args, Content: p.Content, Questions: p.Questions}, nil
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	// Stack-based scan for the first complete, balanced JSON object.
	// Walks the string character by character, tracking brace depth while
	// respecting string boundaries and escape sequences inside strings.
	depth := 0
	start := -1
	inString := false
	escaped := false

	for i, ch := range trimmed {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
			escaped = false
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start != -1 {
				return trimmed[start : i+1], nil
			}
		}
	}

	return "", ErrNoActionFound
}
