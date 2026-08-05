package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/llm/schema"
)

// pruneToolOutputStub is the placeholder body that replaces a stale
// tool-result message. It is intentionally human-readable so a model
// reading the pruned history can see which tool produced it and decide
// whether to re-issue the call. The exact wording is pinned by tests.
const pruneToolOutputStubFmt = "[pruned stale output of %s(%s): %d lines; re-read if needed]"

// pruneMinSizeDefault is the minimum original Content length for a
// tool-result message to be eligible for pruning. Short outputs (a tool
// error, a brief lookup result) are kept verbatim — they cost little
// space but losing them obscures error context.
const pruneMinSizeDefault = 256

// toolKey returns a "tool|path" identifier for a tool call so we can
// recognise "this is a re-read of the same file/path/tool as an earlier
// call in the same turn". Calls without a recognisable path argument
// (shell.run, generic fetch, etc.) get a unique key per argument —
// never collide — so their outputs are never superseded.
func toolKey(toolName string, args json.RawMessage) string {
	if len(args) == 0 || string(args) == "null" {
		return toolName + "\x00" + string(args)
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return toolName + "\x00" + string(args)
	}
	switch toolName {
	case "file.read", "file.read_range", "symbols.find", "search.grep":
		if p, ok := stringField(m, "path"); ok {
			return toolName + "|" + p
		}
		if q, ok := stringField(m, "query"); ok {
			return toolName + "|" + q
		}
		return toolName + "\x00" + string(args)
	}
	// Non-read-class tools never get superseded: concatenating the raw
	// args ensures every distinct call gets a distinct key, so the
	// supersession check below never fires for them.
	return toolName + "\x00" + string(args)
}

// stringField returns m[key] as a string when present and convertible.
func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

// wireToolMeta builds toolCallID -> (tool name, key) from a single
// assistant message. The map is local to one wire position because
// tool calls always sit next to their results.
func wireToolMeta(assistant *schema.ChatMessage) map[string]struct {
	tool string
	key  string
} {
	idx := make(map[string]struct {
		tool string
		key  string
	}, len(assistant.ToolCalls))
	for _, tc := range assistant.ToolCalls {
		idx[tc.ID] = struct {
			tool string
			key  string
		}{tool: tc.Name, key: toolKey(tc.Name, tc.Args)}
	}
	return idx
}

// pruneStaleToolOutputs replaces tool-result messages whose (tool, path)
// was re-called later in the same turn, shrinking the wire before
// compaction. Only read-class tools with a stable path key (file.read,
// file.read_range, symbols.find, search.grep) participate — every other
// tool gets a unique key so its outputs are never superseded.
//
// The returned slice is a fresh copy of wire; the original is not
// mutated. pruned is the number of outputs replaced. minSize is the
// minimum Content length for a result to be considered for pruning
// (pass 0 to disable that gate).
func pruneStaleToolOutputs(wire []schema.ChatMessage, minSize int) ([]schema.ChatMessage, int) {
	// Pass 1: collect tags for every tool-result message.
	type tag struct {
		msgIdx  int
		key     string
		tool    string
		path    string
		content string
		length  int
	}
	var tags []tag
	for i := 0; i < len(wire); i++ {
		m := &wire[i]
		if m.Role != schema.RoleTool {
			continue
		}
		if i == 0 {
			continue
		}
		// Find the immediately-preceding assistant message that issued
		// this tool_call_id. Protocol places assistant+results
		// adjacently; we walk back to the most recent assistant with
		// any tool calls.
		callerIdx := -1
		for j := i - 1; j >= 0; j-- {
			if wire[j].Role == schema.RoleAssistant && len(wire[j].ToolCalls) > 0 {
				callerIdx = j
				break
			}
		}
		if callerIdx < 0 {
			continue
		}
		idx := wireToolMeta(&wire[callerIdx])
		meta, ok := idx[m.ToolCallID]
		if !ok {
			continue
		}
		// The path component is the substring after "tool|" for
		// read-class tools, the empty string otherwise. toolKey's
		// "\x00" sentinel means "non-stable" path; the strings.HasPrefix
		// test below already excludes those from supersession.
		var path string
		if idx2 := strings.IndexByte(meta.key, '|'); idx2 >= 0 {
			path = meta.key[idx2+1:]
		}
		tags = append(tags, tag{
			msgIdx:  i,
			key:     meta.key,
			tool:    meta.tool,
			path:    path,
			content: m.Content,
			length:  len(m.Content),
		})
	}

	// Pass 2: identify superseded tags (those whose key appears more
	// than once and the path is a real, non-sentinel value).
	keyIndices := map[string][]int{}
	for i, t := range tags {
		keyIndices[t.key] = append(keyIndices[t.key], i)
	}
	superseded := map[int]bool{}
	for _, idxs := range keyIndices {
		if len(idxs) < 2 {
			continue
		}
		// Read-class tools use "|" as path separator; non-read-class
		// tools' keys start with "tool\x00…" and are therefore excluded.
		if strings.Contains(tags[idxs[0]].key, "\x00") {
			continue
		}
		// Mark all but the most-recent as superseded.
		for _, i := range idxs[:len(idxs)-1] {
			superseded[i] = true
		}
	}

	// Pass 3: rewrite superseded, eligible tags.
	out := make([]schema.ChatMessage, len(wire))
	copy(out, wire)
	pruned := 0
	for i, t := range tags {
		if !superseded[i] {
			continue
		}
		if minSize > 0 && t.length < minSize {
			continue
		}
		out[t.msgIdx] = schema.ChatMessage{
			Role:       schema.RoleTool,
			ToolCallID: wire[t.msgIdx].ToolCallID,
			Content:    formatPruneStub(t.tool, t.path, t.content),
		}
		pruned++
	}
	return out, pruned
}

// formatPruneStub builds the visible replacement string. Test pins the
// format precisely so the model can rely on the consistent shape.
func formatPruneStub(tool, path, original string) string {
	lines := strings.Count(original, "\n") + 1
	return fmt.Sprintf(pruneToolOutputStubFmt, tool, path, lines)
}
