package provider

import (
	"context"
	"fmt"
	"os"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/schema"
)

// defaultOllamaKeepAlive is applied when a native Ollama provider does not
// set keep_alive. Mirrors embedding.DefaultOllamaKeepAlive — keep in sync.
const defaultOllamaKeepAlive = "30m"

// NewFromConfig builds a Provider from a single [providers.<name>] entry.
// API key resolution (api_key literal vs api_key_env lookup) happens here,
// once, at construction time — OpenAICompatible itself only ever sees an
// already-resolved bearer token string, never an env var name. If both
// api_key and api_key_env are set, the literal api_key wins.
//
// dataDir is the application data directory used for the on-disk limit
// cache. Pass "" to skip limit table loading.
//
// remoteLimitDiscovery controls whether the limit table is fetched from
// remote sources (OpenRouter, LiteLLM) when no usable cache exists. The
// on-disk cache itself is read whenever dataDir is set — reading it makes
// no network requests.
func NewFromConfig(name string, pc config.ProviderConfig, dataDir string, remoteLimitDiscovery bool) (Provider, error) {
	switch pc.Type {
	case "", "openai_compatible":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		caps := DefaultCapabilities()
		caps.ToolCalling = pc.ToolCalling

		table := loadLimitsTable(dataDir, remoteLimitDiscovery)

		return NewOpenAICompatible(Options{
			Name:         name,
			BaseURL:      pc.BaseURL,
			APIKey:       apiKey,
			Capabilities: &caps,
			LimitsTable:  table,
		})
	case "ollama":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		caps := DefaultCapabilities()
		caps.ToolCalling = pc.ToolCalling
		// Ollama's think toggle is a different mechanism than the
		// reasoning_effort/budget_tokens control; report no reasoning
		// capability so the thinking preset field is not sent on the wire.
		caps.Reasoning = false
		keepAlive := pc.KeepAlive
		if keepAlive == "" {
			keepAlive = defaultOllamaKeepAlive
		}
		table := loadLimitsTable(dataDir, remoteLimitDiscovery)
		return NewOllamaNative(Options{
			Name:         name,
			BaseURL:      pc.BaseURL,
			APIKey:       apiKey,
			Capabilities: &caps,
			KeepAlive:    keepAlive,
			LimitsTable:  table,
		})
	case "anthropic":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q: anthropic providers require api_key or api_key_env", name)
		}
		caps := schema.ProviderCapabilities{ToolCalling: true, JSONMode: false, StructuredOutput: false}
		return NewAnthropic(Options{
			Name:           name,
			BaseURL:        pc.BaseURL,
			APIKey:         apiKey,
			Capabilities:   &caps,
			ThinkingBudget: pc.ThinkingBudget,
		})
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, pc.Type)
	}
}

func loadLimitsTable(dataDir string, remoteLimitDiscovery bool) *limits.Table {
	if dataDir == "" {
		return nil
	}
	if remoteLimitDiscovery {
		if table, err := limits.LoadTable(context.Background(), dataDir, limits.DefaultTTL); err == nil {
			return &table
		}
		return nil
	}
	cache, err := limits.Load(dataDir)
	if err != nil || len(cache.Table) == 0 {
		return nil
	}
	table := limits.NewTable(cache.Table)
	return &table
}

// ResolveAPIKey returns the effective API key for a provider config: a
// literal api_key wins over api_key_env; an empty result means no auth,
// which is normal for local endpoints (Ollama, LM Studio).
func ResolveAPIKey(pc config.ProviderConfig) (string, error) {
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
