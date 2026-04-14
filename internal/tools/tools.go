// Package tools defines the provider-agnostic tool types used by executor agents.
// Tools give agents autonomous capabilities: reading files, writing files,
// running commands, and searching the codebase.
package tools

// Definition is the provider-agnostic description of one callable tool.
// Backends serialize this into their wire format (OpenAI functions, Ollama tools, etc.).
type Definition struct {
	Name        string
	Description string
	Parameters  ParameterSchema
}

// ParameterSchema describes a tool's input shape (JSON Schema subset).
type ParameterSchema struct {
	Type       string // always "object"
	Properties map[string]PropertySchema
	Required   []string
}

// PropertySchema describes one field in a tool's parameter object.
type PropertySchema struct {
	Type        string // "string", "integer", "boolean"
	Description string
}

// Call is what the model emits when it invokes a tool.
type Call struct {
	ID        string // opaque call ID, echoed in Result.CallID
	ToolName  string
	Arguments map[string]any // decoded from the model's JSON arguments
}

// Result is the outcome of executing one tool call.
// It is appended to the message thread so the model can see the output.
type Result struct {
	CallID  string // must match the Call.ID that produced this result
	Content string // tool output, or error description when IsError=true
	IsError bool
}

// Response wraps a backend's reply when it may contain tool calls.
type Response struct {
	Content    string // non-empty when the model produced a final text answer
	ToolCalls  []Call // non-empty when the model wants to invoke tools
	StopReason string // "tool_calls" | "stop" | "length"
}
