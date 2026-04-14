package backend

import (
	"fmt"

	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/models"
)

// NewRegistryFromBackends builds a Registry directly from a role→Backend map.
// Useful in tests that supply mock backends without a full config.
func NewRegistryFromBackends(backends map[string]Backend) *Registry {
	return &Registry{backends: backends}
}

// Registry maps the four model roles to their respective Backend instances.
type Registry struct {
	backends map[string]Backend
}

// NewRegistry builds a Registry from the loaded config.
// tokenCounter is passed to each OpenAI-compat backend; pass nil to use the
// char-heuristic fallback.
// modelReg provides per-model capability detection (supports_tools, etc.).
func NewRegistry(cfg *config.Config, tokenCounter func([]Message) (int, error), modelReg *models.Registry) (*Registry, error) {
	r := &Registry{backends: make(map[string]Backend, 4)}

	roles := []struct {
		name string
		mc   config.ModelConfig
	}{
		{config.RoleMarshal, cfg.Model.Marshal},
		{config.RoleExecutor, cfg.Model.Executor},
		{config.RoleCritic, cfg.Model.Critic},
		{config.RoleCompactor, cfg.Model.Compactor},
	}

	for _, entry := range roles {
		b, err := newOpenAICompatFromModelConfig(entry.mc, tokenCounter, modelReg)
		if err != nil {
			return nil, fmt.Errorf("backend for role %q: %w", entry.name, err)
		}
		r.backends[entry.name] = b
	}
	return r, nil
}

// For returns the Backend for the given role name.
func (r *Registry) For(role string) (Backend, error) {
	b, ok := r.backends[role]
	if !ok {
		return nil, fmt.Errorf("unknown role %q (want: marshal|executor|critic|compactor)", role)
	}
	return b, nil
}

// newOpenAICompatFromModelConfig constructs an OpenAI-compat Backend from a
// ModelConfig. All providers in v0.1 use the OpenAI-compat endpoint.
// Tool-use support is determined from per-model settings; local models default
// to false to avoid loop-prone behavior on weak models (PR-3 7.2).
func newOpenAICompatFromModelConfig(mc config.ModelConfig, tc func([]Message) (int, error), modelReg *models.Registry) (Backend, error) {
	if mc.Model == "" {
		return nil, fmt.Errorf("model name is required")
	}
	baseURL := mc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	subtype := ProviderSubtype(mc.Subtype)
	if subtype == "" {
		subtype = SubtypeOpenAI
	}

	// Check per-model settings for tool support (PR-3 7.2).
	// Local models (ollama, llama.cpp, etc.) default to supports_tools=false.
	supTools := true // default for hosted models
	supJSON := true
	if modelReg != nil {
		settings := modelReg.Lookup(mc.Model)
		supTools = settings.SupportsTools
		supJSON = settings.SupportsJSON
	}
	// Auto-disable tools for detected local subtypes as a safety net.
	if subtype != SubtypeOpenAI {
		supTools = false
	}

	return NewOpenAICompat(OpenAICompatConfig{
		BaseURL:       baseURL,
		APIKey:        mc.APIKey,
		ModelName:     mc.Model,
		SupTools:      supTools,
		SupJSONMode:   supJSON,
		Subtype:       subtype,
		Temperature:   mc.Temperature,
		TopP:          mc.TopP,
		MinP:          mc.MinP,
		RepeatPenalty: mc.RepeatPenalty,
		Seed:          mc.Seed,
	}, tc), nil
}
