package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ActionType string

const (
	ActionAnswer   ActionType = "answer"
	ActionToolCall ActionType = "tool_call"
	ActionPatch    ActionType = "patch"
	ActionFinal    ActionType = "final"
)

var (
	ErrNoActionFound     = errors.New("agent: no JSON action object found in model output")
	ErrUnknownActionType = errors.New("agent: unknown action type")
	ErrMissingTool       = errors.New("agent: tool_call action missing tool name")
)

// ModelAction is the parsed form of the JSON action-protocol envelope
// described in docs/07-agent-runtime-and-swarm.md. When Actions is set,
// the single-action fields are empty and vice-versa.
type ModelAction struct {
	Rationale string
	Type      ActionType
	Tool      string
	Args      json.RawMessage
	Content   string
	Actions   []ModelAction // parallel read-only tool calls
}

type actionEnvelope struct {
	Rationale string          `json:"rationale"`
	Action    actionPayload   `json:"action"`
	Actions   []actionPayload `json:"actions,omitempty"`
}

type actionPayload struct {
	Type    ActionType      `json:"type"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
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
	}, nil
}

func validatePayload(p actionPayload) (ModelAction, error) {
	switch p.Type {
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal:
	default:
		return ModelAction{}, fmt.Errorf("%w: %q", ErrUnknownActionType, p.Type)
	}
	if p.Type == ActionToolCall && strings.TrimSpace(p.Tool) == "" {
		return ModelAction{}, ErrMissingTool
	}
	return ModelAction{Type: p.Type, Tool: p.Tool, Args: p.Args, Content: p.Content}, nil
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end < start {
		return "", ErrNoActionFound
	}
	return trimmed[start : end+1], nil
}
