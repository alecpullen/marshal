package agent

import "encoding/json"

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
		if p, ok := m["patch"].(string); ok {
			return p
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
