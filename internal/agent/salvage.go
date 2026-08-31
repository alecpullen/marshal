package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/jsonextract"
	"marshal/internal/llm/schema"
)

// textToolCall is the union of the shapes weak models use when they emit a
// tool call as prose JSON instead of a structured native call:
//
//	{"name": "file.read", "arguments": {...}}       — OpenAI flat
//	{"function": {"name": ..., "arguments": ...}}   — OpenAI nested
//	{"tool_calls": [{...}, ...]}                    — OpenAI batch wrapper
//	{"tool": "file.read", "args": {...}}            — envelope-ish
type textToolCall struct {
	Name      string          `json:"name"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Args      json.RawMessage `json:"args"`
	Function  *struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// normalize collapses the shape variants down to a tool name and an args
// object. OpenAI encodes arguments as a JSON *string*; that is unwrapped so
// the registry always receives a JSON object.
func (c *textToolCall) normalize() (name string, args json.RawMessage) {
	name, args = c.Name, c.Arguments
	if c.Function != nil {
		name, args = c.Function.Name, c.Function.Arguments
	}
	if name == "" {
		name = c.Tool
	}
	if len(args) == 0 {
		args = c.Args
	}
	var s string
	if err := json.Unmarshal(args, &s); err == nil {
		args = json.RawMessage(s)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return name, args
}

// salvageTextToolCalls recovers tool calls a model emitted as prose JSON in
// its text channel instead of as structured native calls — a common failure
// of small local models. Without this, the JSON is shown to the user as the
// final answer and the turn ends unverified.
//
// Two grammars are recognized: the marshal action envelope
// ({"action": {"type": "tool_call", ...}}, handled by ParseActionRepairing)
// and the OpenAI-style direct call shapes above. Every recovered name is
// verified against the registry — unknown names are not salvaged, so prose
// that merely contains JSON cannot trigger tool execution.
func (r *Runner) salvageTextToolCalls(text string) ([]schema.ToolCall, bool) {
	jsonText, _, err := jsonextract.ExtractRepairing(text)
	if err != nil {
		return nil, false
	}

	// Grammar 1: the marshal action envelope.
	if action, _, err := ParseActionRepairing(jsonText, r.knownTool); err == nil {
		payloads := action.Actions
		if len(payloads) == 0 && action.Type != "" {
			payloads = []ModelAction{action}
		}
		var calls []schema.ToolCall
		for _, p := range payloads {
			if p.Type != ActionToolCall || !r.knownTool(p.Tool) {
				calls = nil
				break
			}
			args := p.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			calls = append(calls, schema.ToolCall{
				ID:   fmt.Sprintf("salvaged-%d", len(calls)+1),
				Name: p.Tool,
				Args: args,
			})
		}
		if len(calls) > 0 {
			return calls, true
		}
	}

	// Grammar 2: direct OpenAI-style call object(s).
	var rawCalls []json.RawMessage
	var wrapper struct {
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(jsonText), &wrapper); err == nil && len(wrapper.ToolCalls) > 0 {
		rawCalls = wrapper.ToolCalls
	} else {
		rawCalls = []json.RawMessage{json.RawMessage(jsonText)}
	}
	var calls []schema.ToolCall
	for _, raw := range rawCalls {
		var c textToolCall
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, false
		}
		name, args := c.normalize()
		if strings.TrimSpace(name) == "" || !r.knownTool(name) {
			return nil, false
		}
		calls = append(calls, schema.ToolCall{
			ID:   fmt.Sprintf("salvaged-%d", len(calls)+1),
			Name: name,
			Args: args,
		})
	}
	if len(calls) == 0 {
		return nil, false
	}
	return calls, true
}
