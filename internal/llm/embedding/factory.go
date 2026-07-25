package embedding

import (
	"fmt"
	"os"

	"marshal/internal/app/config"
)

// NewFromConfig builds an Embedder from a single provider entry and a model
// name. The backend is selected by pc.Type: "ollama" -> native /api/embed;
// "" or "openai_compatible" -> OpenAI-compatible /v1/embeddings.
//
// API-key resolution mirrors provider.NewFromConfig: a literal api_key wins
// over api_key_env; absent auth is normal for local endpoints.
func NewFromConfig(name string, pc config.ProviderConfig, model string) (Embedder, error) {
	if model == "" {
		return nil, fmt.Errorf("embedding provider %q: model is required", name)
	}
	apiKey, err := resolveAPIKey(pc)
	if err != nil {
		return nil, fmt.Errorf("embedding provider %q: %w", name, err)
	}
	switch pc.Type {
	case "ollama":
		return newOllamaEmbedder(pc.BaseURL, apiKey, model), nil
	case "", "openai_compatible":
		return newOpenAIEmbedder(pc.BaseURL, apiKey, model), nil
	default:
		return nil, fmt.Errorf("embedding provider %q: unsupported type %q", name, pc.Type)
	}
}

// resolveAPIKey mirrors provider.resolveAPIKey (literal key wins over env
// lookup; empty is allowed for local endpoints).
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
	return "", nil
}
