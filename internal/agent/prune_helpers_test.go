package agent

import "encoding/json"

// jsonRaw is a tiny helper for tests that need schema.ToolCall.Args
// without importing encoding/json at every call site.
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}
