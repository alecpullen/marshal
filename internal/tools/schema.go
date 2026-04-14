// Package tools implements the tool-use executor interface for Marshal.
// This package provides the JSON schema definitions and tool registry
// for the three core tools: read_file, write_file, and run_command.
package tools

import (
	"context"
	"encoding/json"

	"github.com/alecpullen/marshal/internal/sandbox"
)

// ToolHandler is the function signature for all tool handlers.
// It takes the repository root and raw JSON input, returning a result and error.
type ToolHandler func(repoRoot string, input json.RawMessage) (interface{}, error)

// Tool represents a callable tool with a JSON schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Handler     ToolHandler
}

// ToolSchemas contains the JSON schema definitions for all tools.
var ToolSchemas = struct {
	ReadFile   json.RawMessage
	WriteFile  json.RawMessage
	RunCommand json.RawMessage
}{
	ReadFile: []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Relative path to the file within the repository"
			},
			"offset": {
				"type": "integer",
				"description": "Starting line number (0-indexed, default: 0)",
				"minimum": 0,
				"default": 0
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read (0 = unlimited, max: 250)",
				"minimum": 0,
				"maximum": 250,
				"default": 0
			}
		},
		"required": ["path"]
	}`),
	WriteFile: []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Relative path to the file within the repository"
			},
			"content": {
				"type": "string",
				"description": "Content to write to the file"
			},
			"append": {
				"type": "boolean",
				"description": "If true, append to existing file instead of overwriting",
				"default": false
			}
		},
		"required": ["path", "content"]
	}`),
	RunCommand: []byte(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "Shell command to execute"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 30, max: 300)",
				"minimum": 1,
				"maximum": 300,
				"default": 30
			},
			"work_dir": {
				"type": "string",
				"description": "Working directory relative to repo root (default: repo root)"
			}
		},
		"required": ["command"]
	}`),
}

// Registry holds all available tools.
type Registry struct {
	tools map[string]*Tool
}

// NewRegistry creates a new tool registry with all built-in tools.
func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]*Tool)}

	r.Register(&Tool{
		Name:        "read_file",
		Description: "Read a file from the repository. Supports optional line-based pagination with offset and limit. Maximum file size is 1MB.",
		InputSchema: ToolSchemas.ReadFile,
		Handler:     wrapReadFile,
	})

	r.Register(&Tool{
		Name:        "write_file",
		Description: "Write content to a file in the repository. Creates parent directories if needed. Can append or overwrite.",
		InputSchema: ToolSchemas.WriteFile,
		Handler:     wrapWriteFile,
	})

	r.Register(&Tool{
		Name:        "run_command",
		Description: "Execute a shell command within the repository. Command must be in allowlist. Captures stdout/stderr with 64KB limit each.",
		InputSchema: ToolSchemas.RunCommand,
		Handler:     wrapRunCommand,
	})

	return r
}

// NewRegistryWithSandbox creates a registry that uses a specific sandbox config.
func NewRegistryWithSandbox(sandboxCfg interface{}) *Registry {
	// For now, delegate to NewRegistry - sandbox is passed at call time
	return NewRegistry()
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool *Tool) {
	r.tools[tool.Name] = tool
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns all registered tools.
func (r *Registry) All() []*Tool {
	result := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

// wrapReadFile adapts ReadFile to the ToolHandler signature.
func wrapReadFile(repoRoot string, input json.RawMessage) (interface{}, error) {
	return ReadFile(repoRoot, input)
}

// wrapWriteFile adapts WriteFile to the ToolHandler signature.
func wrapWriteFile(repoRoot string, input json.RawMessage) (interface{}, error) {
	return WriteFile(repoRoot, input)
}

// wrapRunCommand adapts RunCommand to the ToolHandler signature.
func wrapRunCommand(repoRoot string, input json.RawMessage) (interface{}, error) {
	// RunCommand needs context - use background context for now
	ctx := context.Background()
	sb := sandbox.New(sandbox.Config{RepoRoot: repoRoot})
	return RunCommand(ctx, sb, input)
}
