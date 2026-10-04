package schema

// ModelInfo describes a model as reported by a provider's model-listing
// endpoint. Most OpenAI-compatible /v1/models endpoints (Ollama, LM Studio)
// return only id/owned_by, so ContextWindow and MaxOutputTokens are optional
// and are filled in from external limit tables when available. Zero values
// mean "unknown"; callers should fall back to built-in defaults or user config.
type ModelInfo struct {
	ID              string
	OwnedBy         string
	ContextWindow   int // 0 when unknown
	MaxOutputTokens int // 0 when unknown
	// ToolCalling reports whether the model supports tool/function calling,
	// when some source reports it. nil means no source reported it.
	ToolCalling *bool
	// Thinking is nil when the provider does not describe its controls.
	Thinking *ThinkingOptions
}

// ThinkingOptions describes model-specific controls. Levels are wire values,
// except on/off for boolean controls. An empty list means no selectable control.
type ThinkingOptions struct {
	Levels  []string `toml:"levels" json:"levels"`
	Default string   `toml:"default,omitempty" json:"default,omitempty"`
	// Mode is effort, toggle, budget, or adaptive.
	Mode string `toml:"mode,omitempty" json:"mode,omitempty"`
}
