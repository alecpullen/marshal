package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/jsonextract"
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
	Rationale string        `json:"rationale"`
	Action    payloadOrList `json:"action"`
	Actions   payloadOrList `json:"actions,omitempty"`
}

// payloadOrList accepts either a single action object or an array of them,
// under either the "action" or "actions" key. Models routinely mix the two
// forms up; the shape carries no information the key does not, so rejecting
// a mismatch only costs a regeneration.
type payloadOrList struct {
	Items []actionPayload
	List  bool // input was an array
}

func (p *payloadOrList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '[' {
		p.List = true
		return json.Unmarshal(trimmed, &p.Items)
	}
	var one actionPayload
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return err
	}
	p.Items = []actionPayload{one}
	return nil
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
	action, _, err := ParseActionRepairing(raw, nil)
	return action, err
}

// ParseActionRepairing parses an envelope, healing deviations that have a
// single unambiguous reading instead of forcing the model to regenerate.
// knownTool reports whether a name is a registered tool; when nil, the
// tool-name repair is skipped (no registry to verify against, and guessing
// would be worse than the error).
//
// Repairs returns one human-readable note per healed deviation so the caller
// can tell the model what it got wrong. An empty slice means the envelope was
// already well-formed.
func ParseActionRepairing(raw string, knownTool func(string) bool) (ModelAction, []string, error) {
	var repairs []string

	jsonText, jsonRepaired, err := jsonextract.ExtractRepairing(raw)
	if err != nil {
		if errors.Is(err, jsonextract.ErrNotFound) {
			return ModelAction{}, nil, ErrNoActionFound
		}
		return ModelAction{}, nil, err
	}
	if jsonRepaired {
		repairs = append(repairs, "the JSON envelope was unbalanced (missing or stray brackets) and had to be closed")
	}

	var envelope actionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return ModelAction{}, nil, fmt.Errorf("agent: malformed action JSON: %w", err)
	}

	// Either key may carry either shape. Prefer whichever is populated.
	payloads, list := envelope.Actions.Items, envelope.Actions.List
	key := "actions"
	if len(payloads) == 0 {
		payloads, list = envelope.Action.Items, envelope.Action.List
		key = "action"
	}
	if len(payloads) == 0 {
		return ModelAction{}, nil, fmt.Errorf("%w: %q", ErrUnknownActionType, "")
	}
	// batch decides which form the action takes. The "actions" array is the
	// parallel form and carries a read-only restriction (F-SEC-11), so a
	// one-element array must STAY a batch — collapsing it to a single action
	// would route a write tool around that guard.
	batch := key == "actions" && list
	switch {
	case key == "action" && list && len(payloads) > 1:
		// More than one action can only mean the parallel form.
		batch = true
		repairs = append(repairs, `multiple actions were sent under "action"; use "actions" for parallel calls`)
	case key == "action" && list:
		repairs = append(repairs, `a single action was wrapped in an array under "action"; send the object directly`)
	case key == "actions" && !list:
		batch = true
		repairs = append(repairs, `a single action was sent under "actions" as an object; use "action", or an array for parallel calls`)
	}

	if batch {
		actions := make([]ModelAction, 0, len(payloads))
		for _, p := range payloads {
			ma, notes, err := validatePayload(p, knownTool)
			if err != nil {
				return ModelAction{}, nil, err
			}
			repairs = append(repairs, notes...)
			actions = append(actions, ma)
		}
		return ModelAction{Rationale: envelope.Rationale, Actions: actions}, repairs, nil
	}

	ma, notes, err := validatePayload(payloads[0], knownTool)
	if err != nil {
		return ModelAction{}, nil, err
	}
	repairs = append(repairs, notes...)
	return ModelAction{
		Rationale: envelope.Rationale,
		Type:      ma.Type,
		Tool:      ma.Tool,
		Args:      ma.Args,
		Content:   ma.Content,
		Questions: ma.Questions,
	}, repairs, nil
}

func validatePayload(p actionPayload, knownTool func(string) bool) (ModelAction, []string, error) {
	var repairs []string
	switch p.Type {
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal, ActionAskUser, ActionQuestionAsk:
	default:
		// Models reach for the tool name as the action type ("todo.write").
		// Verified against the registry, so this cannot guess: an unknown
		// name still errors.
		if knownTool != nil && strings.TrimSpace(p.Tool) == "" && knownTool(string(p.Type)) {
			repairs = append(repairs, fmt.Sprintf(
				"%q is a tool name, not an action type; use {\"type\": \"tool_call\", \"tool\": %q}",
				string(p.Type), string(p.Type)))
			p.Tool = string(p.Type)
			p.Type = ActionToolCall
			break
		}
		return ModelAction{}, nil, fmt.Errorf("%w: %q", ErrUnknownActionType, p.Type)
	}
	if p.Type == ActionToolCall && strings.TrimSpace(p.Tool) == "" {
		return ModelAction{}, nil, ErrMissingTool
	}
	if p.Type == ActionAskUser && strings.TrimSpace(p.Content) == "" {
		return ModelAction{}, nil, ErrMissingQuestion
	}
	if p.Type == ActionQuestionAsk && len(p.Questions) == 0 {
		return ModelAction{}, nil, fmt.Errorf("agent: question.ask action requires at least one question")
	}
	return ModelAction{Type: p.Type, Tool: p.Tool, Args: p.Args, Content: p.Content, Questions: p.Questions}, repairs, nil
}

// knownTool reports whether name is a tool this runner can dispatch. Used to
// verify the tool-name-as-action-type repair against reality rather than
// guessing. A runner with no registry heals nothing.
func (r *Runner) knownTool(name string) bool {
	if r == nil || r.Registry == nil || name == "" {
		return false
	}
	_, ok := r.Registry.Lookup(name)
	return ok
}
