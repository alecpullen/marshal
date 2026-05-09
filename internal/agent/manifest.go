// Package agent implements the agent runtime for swarm tasks.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest defines an agent role configuration.
type Manifest struct {
	Role        string `yaml:"role"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`

	// Model configuration
	ModelBinding string `yaml:"model_binding"`
	SystemPrompt string `yaml:"system_prompt"`

	// Tool configuration
	Tools         []string     `yaml:"tools"`
	ToolSchemas   []ToolSchema `yaml:"tool_schemas,omitempty"`

	// Execution limits
	MaxIterations int           `yaml:"max_iterations"`
	Timeout       time.Duration `yaml:"timeout"`

	// Context assembly
	ContextPolicy ContextPolicy `yaml:"context_policy"`

	// Output validation
	OutputSchema   json.RawMessage `yaml:"output_schema"`
	OutputRequired bool          `yaml:"output_required"`

	// Safety settings
	RequiresReadBeforeEdit bool `yaml:"requires_read_before_edit"`

	// Sub-agent capabilities
	CanSpawnAgents   bool     `yaml:"can_spawn_agents"`
	AllowedSubRoles  []string `yaml:"allowed_sub_roles,omitempty"`
	MaxConcurrentSubs int     `yaml:"max_concurrent_subs"`

	// Metadata
	Capabilities []string `yaml:"capabilities"`
}

// ContextPolicy defines how context is assembled.
type ContextPolicy struct {
	Inherit         []string `yaml:"inherit"`
	Exclude         []string `yaml:"exclude"`
	IncludeExplicit []string `yaml:"include_explicit,omitempty"`
	MaxTokens       int      `yaml:"max_tokens"`
	SummarizeIfOver int      `yaml:"summarize_if_over"`
}

// ToolSchema extends a tool with custom configuration.
type ToolSchema struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Parameters  json.RawMessage `yaml:"parameters"`
}

// DefaultManifestPath returns the default path for role manifests.
func DefaultManifestPath(role string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "marshal", "roles", role+".yaml")
}

// LoadManifest reads and parses a role manifest from YAML.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Apply defaults
	m.applyDefaults()

	return &m, nil
}

// LoadManifestForRole loads a manifest for a specific role from user config.
func LoadManifestForRole(role string) (*Manifest, error) {
	path := DefaultManifestPath(role)
	return LoadManifest(path)
}

// applyDefaults sets default values for unspecified fields.
func (m *Manifest) applyDefaults() {
	if m.MaxIterations == 0 {
		m.MaxIterations = 3
	}
	if m.Timeout == 0 {
		m.Timeout = 5 * time.Minute
	}
	if m.ContextPolicy.MaxTokens == 0 {
		m.ContextPolicy.MaxTokens = 8000
	}
	if m.MaxConcurrentSubs == 0 {
		m.MaxConcurrentSubs = 3
	}
	if m.Version == "" {
		m.Version = "1.0"
	}
}

// Validate checks if the manifest is complete and valid.
func (m *Manifest) Validate() error {
	if m.Role == "" {
		return fmt.Errorf("manifest: role is required")
	}
	if m.ModelBinding == "" {
		return fmt.Errorf("manifest: model_binding is required")
	}
	if m.SystemPrompt == "" {
		return fmt.Errorf("manifest: system_prompt is required")
	}
	if len(m.Tools) == 0 {
		return fmt.Errorf("manifest: at least one tool is required")
	}

	// Check for circular sub-role references (simplified)
	if m.CanSpawnAgents && len(m.AllowedSubRoles) == 0 {
		// If can_spawn_agents is true but no sub-roles specified, that's a warning
		// but not necessarily an error
	}

	return nil
}

// CanSpawnRole checks if this agent can spawn a sub-agent of the given role.
func (m *Manifest) CanSpawnRole(role string) bool {
	if !m.CanSpawnAgents {
		return false
	}
	if len(m.AllowedSubRoles) == 0 {
		return true // All roles allowed if list is empty
	}
	for _, allowed := range m.AllowedSubRoles {
		if allowed == role {
			return true
		}
	}
	return false
}

// Clone creates a copy of the manifest with modifications.
func (m *Manifest) Clone() *Manifest {
	// Simple deep copy via JSON roundtrip
	data, _ := json.Marshal(m)
	var cloned Manifest
	json.Unmarshal(data, &cloned)
	return &cloned
}
