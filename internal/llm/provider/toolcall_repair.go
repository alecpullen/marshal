package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/llm/schema"
)

// repairToolCalls post-processes native tool calls from a model response. Some
// local-model adapters (e.g. minimax-m3 via Ollama Cloud) concatenate multiple
// JSON objects into a single function.arguments string. When that happens we
// split the concatenation into separate tool calls so the rest of the agent
// loop can execute them normally. Calls with already-valid single-object
// arguments are returned unchanged.
func repairToolCalls(calls []schema.ToolCall) ([]schema.ToolCall, bool) {
	repaired := false
	out := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		args := string(call.Args)
		if json.Valid([]byte(args)) {
			out = append(out, call)
			continue
		}
		split := splitJSONObjects(args)
		if len(split) == 0 {
			// Not recoverable; pass through so the caller can report the
			// original malformed arguments as a tool error.
			out = append(out, call)
			continue
		}
		repaired = true
		for i, obj := range split {
			id := call.ID
			if i > 0 && id != "" {
				id = fmt.Sprintf("%s_r%d", id, i)
			}
			out = append(out, schema.ToolCall{
				ID:   id,
				Name: call.Name,
				Args: json.RawMessage(obj),
			})
		}
	}
	return out, repaired
}

// splitJSONObjects attempts to split a string that may contain multiple
// concatenated JSON objects into individual object strings. It returns nil if
// the input is not a sequence of one or more valid JSON objects.
func splitJSONObjects(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			break
		}
		obj, rest, ok := nextJSONObject(s)
		if !ok {
			return nil
		}
		out = append(out, obj)
		s = rest
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nextJSONObject finds the first complete JSON object in s and returns it
// along with the remaining string. It uses json.Decoder to locate the object
// boundary without writing a custom brace-matching parser.
func nextJSONObject(s string) (obj string, rest string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", "", false
	}
	obj = string(raw)
	// dec.InputOffset() reports bytes consumed from the start of s.
	offset := dec.InputOffset()
	if offset < 0 || offset > int64(len(s)) {
		return "", "", false
	}
	return obj, s[offset:], true
}
