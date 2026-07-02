package schema

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
	Role    Role
	Content string
}

// ChatRequest is the provider-agnostic chat request shape. Pointer fields
// (Temperature, TopP, MaxTokens) distinguish "unset" from "zero" so the
// provider can omit them from the wire request rather than sending
// temperature=0 unintentionally.
type ChatRequest struct {
	Model       string
	Messages    []ChatMessage
	Stream      bool
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Stop        []string
}
