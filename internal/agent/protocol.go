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
// described in docs/07-agent-runtime-and-swarm.md:
//
//	{"rationale": "...", "action": {"type": "tool_call", "tool": "...", "args": {...}}}
type ModelAction struct {
	Rationale string
	Type      ActionType
	Tool      string
	Args      json.RawMessage
	Content   string
}

type actionEnvelope struct {
	Rationale string        `json:"rationale"`
	Action    actionPayload `json:"action"`
}

type actionPayload struct {
	Type    ActionType      `json:"type"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
}

// ParseAction extracts and validates the single JSON action object a model
// is instructed (via BuildSystemPrompt) to reply with. It tolerates a
// leading/trailing ```json fence, since local models frequently wrap JSON
// in markdown even when told not to.
func ParseAction(raw string) (ModelAction, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return ModelAction{}, err
	}

	var envelope actionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return ModelAction{}, fmt.Errorf("agent: malformed action JSON: %w", err)
	}

	switch envelope.Action.Type {
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal:
	default:
		return ModelAction{}, fmt.Errorf("%w: %q", ErrUnknownActionType, envelope.Action.Type)
	}

	if envelope.Action.Type == ActionToolCall && strings.TrimSpace(envelope.Action.Tool) == "" {
		return ModelAction{}, ErrMissingTool
	}

	return ModelAction{
		Rationale: envelope.Rationale,
		Type:      envelope.Action.Type,
		Tool:      envelope.Action.Tool,
		Args:      envelope.Action.Args,
		Content:   envelope.Action.Content,
	}, nil
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
