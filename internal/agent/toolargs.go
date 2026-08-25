package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

func SummarizeToolArgs(toolName string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	switch toolName {
	case "shell.run", "test.run":
		if c, ok := m["command"].(string); ok {
			return c
		}
		return ""
	case "file.read":
		if p, ok := m["path"].(string); ok {
			return p
		}
		return ""
	case "repo.search":
		if q, ok := m["query"].(string); ok {
			return q
		}
		return ""
	case "file.write_patch":
		// The patch body is large and would flood the live tool-call line;
		// the file path is surfaced separately via ActiveToolCall.Path.
		return "patch"
	case "agent.await":
		// Numeric and boolean arguments only, so the default branch's
		// "first string value" rule finds nothing and returns "". A
		// blocking await that names no target is the least legible line
		// on screen, so it gets an explicit case.
		if id, ok := m["id"].(float64); ok && id != 0 {
			return "#" + strconv.FormatInt(int64(id), 10)
		}
		if any, ok := m["any"].(bool); ok && any {
			return "any"
		}
		if all, ok := m["all"].(bool); ok && all {
			return "all"
		}
		return ""
	default:
		for _, v := range m {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
}

// firstPatchPathFromArgs extracts the first "File: <path>" line from a
// file.write_patch proposal's raw args, used to name the file a file-edit
// tool is working on. It returns "" for any other tool or when no path is
// present.
func firstPatchPathFromArgs(toolName string, args json.RawMessage) string {
	if toolName != "file.write_patch" || len(args) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	p, ok := m["patch"].(string)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(p, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
		}
	}
	return ""
}
