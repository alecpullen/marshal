package provider

import (
	"context"
	"fmt"
	"os"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/limits"
)

// NewFromConfig builds a Provider from a single [providers.<name>] entry.
// API key resolution (api_key literal vs api_key_env lookup) happens here,
// once, at construction time — OpenAICompatible itself only ever sees an
// already-resolved bearer token string, never an env var name. If both
// api_key and api_key_env are set, the literal api_key wins.
//
// dataDir is the application data directory used for the on-disk limit
// cache. Pass "" to skip limit table loading.
func NewFromConfig(name string, pc config.ProviderConfig, dataDir string) (Provider, error) {
	switch pc.Type {
	case "", "openai_compatible":
		apiKey, err := resolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		caps := DefaultCapabilities()
		caps.ToolCalling = pc.ToolCalling

		var table *limits.Table
		if dataDir != "" {
			if t, err := limits.LoadTable(context.Background(), dataDir, limits.DefaultTTL); err == nil {
				table = &t
			}
		}

		return NewOpenAICompatible(Options{
			Name:         name,
			BaseURL:      pc.BaseURL,
			APIKey:       apiKey,
			Capabilities: &caps,
			LimitsTable:  table,
		})
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, pc.Type)
	}
}

func resolveAPIKey(pc config.ProviderConfig) (string, error) {
	if pc.APIKey != "" {
		return pc.APIKey, nil
	}
	if pc.APIKeyEnv != "" {
		v, ok := os.LookupEnv(pc.APIKeyEnv)
		if !ok || v == "" {
			return "", fmt.Errorf("environment variable %q (from api_key_env) is not set", pc.APIKeyEnv)
		}
		return v, nil
	}
	return "", nil // no auth — normal for local Ollama/LM Studio
}
