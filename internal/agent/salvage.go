package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"marshal/internal/jsonextract"
	"marshal/internal/llm/schema"
)

// textToolCall is the union of the shapes weak models use when they emit a
// tool call as prose JSON instead of a structured native call:
//
//	{"name": "file.read", "arguments": {...}}       — OpenAI flat
//	{"tool_name": "file.read", "arguments": {...}}  — Qwen-style flat
//	{"function": {"name": ..., "arguments": ...}}   — OpenAI nested
//	{"tool_calls": [{...}, ...]}                    — OpenAI batch wrapper
//	{"tool": "file.read", "args": {...}}            — envelope-ish
type textToolCall struct {
	Name      string          `json:"name"`
	ToolName  string          `json:"tool_name"`
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
		name = c.ToolName
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

// Qwen/Hermes-style XML tool calls, emitted into the text channel by local
// models whose server has no tool-call parser for their architecture
// (observed live: qwen3.8-27b on LM Studio returns tool_calls:[] and dumps
// this markup into content). The closing-tag alternation tolerates
// malformation — truncated finetunes routinely drop </parameter> or
// </function> before </tool_call> or end of text — and the name class
// admits = and / because the same models mangle dotted tool names
// (<function=file=read>, <function=file/read> for file.read).
var (
	xmlFunctionRe  = regexp.MustCompile(`(?s)<function=([A-Za-z0-9_.\-=/]+)>(.*?)(?:</function>|</tool_call>|$)`)
	xmlParameterRe = regexp.MustCompile(`(?s)<parameter=([A-Za-z0-9_.\-]+)>(.*?)(?:</parameter>|</function>|</tool_call>|$)`)
	// xmlBareTagRe matches argument tags emitted without the parameter=
	// wrapper (<function=file.read><path>src/big.go</path></function>).
	// Backreference \1 requires matching open/close tags.
	xmlBareTagRe = regexp.MustCompile(`(?s)<([a-z][a-z0-9_.\-]*)>\s*([^<]+?)\s*</([a-z][a-z0-9_.\-]*)>`)
)

// xmlArgValue converts a raw XML argument value to JSON, preserving the
// type of values that parse as JSON and quoting everything else.
func xmlArgValue(val string) (json.RawMessage, bool) {
	val = strings.TrimSpace(val)
	if raw := json.RawMessage(val); json.Valid(raw) {
		return raw, true
	}
	s, err := json.Marshal(val)
	if err != nil {
		return nil, false
	}
	return s, true
}

// knownXMLTool resolves an XML function name against the registry,
// unmangling the = and / separators weak models substitute for dots.
// Returns the canonical tool name and whether it is registered.
func (r *Runner) knownXMLTool(name string) (string, bool) {
	if r.knownTool(name) {
		return name, true
	}
	unmangled := strings.NewReplacer("=", ".", "/", ".").Replace(name)
	if unmangled != name && r.knownTool(unmangled) {
		return unmangled, true
	}
	return "", false
}

// salvageXMLToolCalls recovers Hermes-style XML tool calls:
//
//	<tool_call><function=file.read><parameter=path>src/main.go</parameter></function></tool_call>
//
// Parameter values that parse as JSON keep their type; everything else
// becomes a string. Like the JSON grammars, every recovered name is verified
// against the registry.
func (r *Runner) salvageXMLToolCalls(text string) ([]schema.ToolCall, bool) {
	if !strings.Contains(text, "<function=") {
		return nil, false
	}
	var calls []schema.ToolCall
	for _, m := range xmlFunctionRe.FindAllStringSubmatch(text, -1) {
		name, ok := r.knownXMLTool(m[1])
		if !ok {
			return nil, false
		}
		args := map[string]json.RawMessage{}
		for _, pm := range xmlParameterRe.FindAllStringSubmatch(m[2], -1) {
			raw, ok := xmlArgValue(pm[2])
			if !ok {
				return nil, false
			}
			args[pm[1]] = raw
		}
		if len(args) == 0 {
			// Fallback: bare argument tags without the parameter= wrapper
			// (<path>src/big.go</path>). Open and close tags must match
			// (backreference) and tags that name the call itself are skipped.
			for _, bm := range xmlBareTagRe.FindAllStringSubmatch(m[2], -1) {
				if bm[1] != bm[3] || bm[1] == "function" || bm[1] == "tool_call" {
					continue
				}
				raw, ok := xmlArgValue(bm[2])
				if !ok {
					return nil, false
				}
				args[bm[1]] = raw
			}
		}
		var buf json.RawMessage
		if len(args) == 0 && strings.HasPrefix(strings.TrimSpace(m[2]), "{") {
			// Fallback: the arguments as a JSON object directly inside the
			// function tags (<function=file.read>{"path": ...}</function>).
			if body := json.RawMessage(strings.TrimSpace(m[2])); json.Valid(body) {
				buf = body
			}
		}
		if buf == nil {
			var err error
			buf, err = json.Marshal(args)
			if err != nil {
				return nil, false
			}
		}
		calls = append(calls, schema.ToolCall{
			ID:   fmt.Sprintf("salvaged-%d", len(calls)+1),
			Name: name,
			Args: buf,
		})
	}
	if len(calls) == 0 {
		return nil, false
	}
	return calls, true
}

// salvageTextToolCalls recovers tool calls a model emitted as prose in its
// text channel instead of as structured native calls — a common failure of
// small local models. Without this, the markup is shown to the user as the
// final answer and the turn ends unverified.
//
// Three grammars are recognized: the marshal action envelope
// ({"action": {"type": "tool_call", ...}}, handled by ParseActionRepairing),
// the OpenAI/Qwen-style direct JSON call shapes above, and Hermes-style
// <function=name><parameter=k>v</parameter></function> XML. Every recovered
// name is verified against the registry — unknown names are not salvaged,
// so prose that merely contains JSON or XML cannot trigger tool execution.
func (r *Runner) salvageTextToolCalls(text string) ([]schema.ToolCall, bool) {
	jsonText, _, err := jsonextract.ExtractRepairing(text)
	if err != nil {
		// No JSON value at all — the last resort is XML markup.
		return r.salvageXMLToolCalls(text)
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

	// Grammar 2: direct OpenAI/Qwen-style call object(s).
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
			// Not a call object — fall through to the XML grammar.
			return r.salvageXMLToolCalls(text)
		}
		name, args := c.normalize()
		if strings.TrimSpace(name) == "" {
			// Tool-name-keyed object: {"file.read": {"path": ...}} — the
			// model uses the tool name as the sole JSON key.
			var keyed map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keyed); err == nil && len(keyed) == 1 {
				for k, v := range keyed {
					if r.knownTool(k) {
						name, args = k, v
					}
				}
			}
		}
		if strings.TrimSpace(name) == "" || !r.knownTool(name) {
			// JSON that is not a tool call — fall through to the XML grammar.
			return r.salvageXMLToolCalls(text)
		}
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		calls = append(calls, schema.ToolCall{
			ID:   fmt.Sprintf("salvaged-%d", len(calls)+1),
			Name: name,
			Args: args,
		})
	}
	if len(calls) > 0 {
		return calls, true
	}
	// JSON was present but not a tool call — still try XML (mixed output
	// with markup after/before an unrelated JSON snippet).
	return r.salvageXMLToolCalls(text)
}
