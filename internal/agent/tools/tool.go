// Package tools provides the agent-facing tool abstraction layer.
// It wraps internal/tools/ with enforcement capabilities for read-before-edit
// and staleness detection.
package tools

import (
	"context"
	"encoding/json"

	"github.com/alecpullen/marshal/pkg/protocol"
)

// Tool is the agent-facing tool interface.
type Tool interface {
	// Metadata
	Name() string
	Description() string
	Schema() json.RawMessage

	// Capabilities
	IsReadOperation() bool        // Does this tool read files?
	IsMutating() bool             // Does this tool modify files?
	RequiresReadBeforeEdit() bool // Must file be read first?

	// Execution
	Invoke(ctx context.Context, args json.RawMessage) (*Result, error)
}

// Result is the standardized tool result.
type Result struct {
	Content    string              // Human-readable output
	Data       json.RawMessage     // Structured data
	Error      *ToolError          // Structured error if failed
	ContextRef protocol.ContextRef // Reference if stored to context store

	// Read-set tracking
	ReadPath    string // Path that was read (if read operation)
	ReadHash    string // BLAKE3 hash of content at read time
	ContentSize int    // Size in bytes
}

// ToolError provides structured error information for agent recovery.
type ToolError struct {
	Code    string         // "file_not_found", "stale_hash", "read_before_edit", etc.
	Message string         // Human-readable message
	Hint    string         // Recovery guidance for the agent
	Details map[string]any // Additional context
}

func (e *ToolError) Error() string {
	return e.Message
}

// IsCriticalError returns true if this error should surface to the orchestrator
// rather than being handled by the agent's retry loop.
func (e *ToolError) IsCriticalError() bool {
	switch e.Code {
	case "permission_denied",
		"out_of_disk_space",
		"network_unreachable",
		"repository_corrupted":
		return true
	default:
		return false
	}
}

// IsRetryableError returns true if the agent should automatically retry
// or if this is a recoverable error that the agent can handle.
func (e *ToolError) IsRetryableError() bool {
	// If it's critical, it's not retryable by the agent
	if e.IsCriticalError() {
		return false
	}
	// All other errors are considered retryable
	return true
}
