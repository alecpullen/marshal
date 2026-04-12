package backend

import (
	"fmt"

	"github.com/alec/marshal/internal/config"
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
func NewRegistry(cfg *config.Config, tokenCounter func([]Message) (int, error)) (*Registry, error) {
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
		b, err := newOpenAICompatFromModelConfig(entry.mc, tokenCounter)
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
// ModelConfig.  All providers in v0.1 use the OpenAI-compat endpoint.
func newOpenAICompatFromModelConfig(mc config.ModelConfig, tc func([]Message) (int, error)) (Backend, error) {
	if mc.Model == "" {
		return nil, fmt.Errorf("model name is required")
	}
	baseURL := mc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	// Assume tools + JSON mode are supported by default; the per-model
	// settings table (M5) will refine this per model string.
	return NewOpenAICompat(OpenAICompatConfig{
		BaseURL:     baseURL,
		APIKey:      mc.APIKey,
		ModelName:   mc.Model,
		SupTools:    true,
		SupJSONMode: true,
	}, tc), nil
}
