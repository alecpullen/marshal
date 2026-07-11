package acp

import (
	"marshal/internal/tools/registry"
)

// auditEvent returns a minimal registry.AuditEvent for use in tests.
// Construction is isolated here so turn_test.go does not need to import
// the registry package directly.
func auditEvent(toolName string) *registry.AuditEvent {
	return &registry.AuditEvent{ToolName: toolName}
}
