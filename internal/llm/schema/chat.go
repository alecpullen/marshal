package schema

import "encoding/json"

// Role identifies the speaker of a ChatMessage in the wire protocol sense.
// This is intentionally a separate type from session.Role: session.Role is a
// TUI/transcript concern, schema.Role is an LLM wire-protocol concern.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool is reserved for Milestone D tool-calling; not emitted or
	// consumed by anything in this milestone.
	RoleTool Role = "tool"
)

type ChatMessage struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"`
}

// JSONSchemaSpec is the json_schema payload of a ResponseFormat.
type JSONSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

// ChatRequest is the provider-agnostic chat request shape. Pointer fields
// (Temperature, TopP, MaxTokens, ResponseFormat) distinguish "unset" from
// "zero" so the provider can omit them from the wire request rather than
// sending temperature=0 unintentionally.
type ChatRequest struct {
	Model          string
	Messages       []ChatMessage
	Stream         bool
	Temperature    *float64
	TopP           *float64
	MaxTokens      *int
	Stop           []string
	ResponseFormat *ResponseFormat
	Tools          []ToolDefinition
	ToolChoice     string
}
