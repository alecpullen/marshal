package registry

import (
	"context"
	"encoding/json"
)

type RiskLevel string

const (
	RiskReadOnly       RiskLevel = "read_only"
	RiskWorkspaceWrite RiskLevel = "workspace_write"
	RiskCommand        RiskLevel = "command"
	RiskNetwork        RiskLevel = "network"
	RiskDestructive    RiskLevel = "destructive"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskReadOnly, RiskWorkspaceWrite, RiskCommand, RiskNetwork, RiskDestructive:
		return true
	default:
		return false
	}
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Risk        RiskLevel
	Cacheable   bool
	Handler     ToolHandler
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

type ToolResult struct {
	Summary         string
	Content         string
	FilesChanged    []string
	CommandExitCode *int
}
