package embedding

import (
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
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
	apiKey, err := provider.ResolveAPIKey(pc)
	if err != nil {
		return nil, fmt.Errorf("embedding provider %q: %w", name, err)
	}
	switch pc.Type {
	case "ollama":
		keepAlive := pc.KeepAlive
		if keepAlive == "" {
			keepAlive = DefaultOllamaKeepAlive
		}
		return newOllamaEmbedder(pc.BaseURL, apiKey, model, keepAlive), nil
	case "", "openai_compatible":
		return newOpenAIEmbedder(pc.BaseURL, apiKey, model), nil
	default:
		return nil, fmt.Errorf("embedding provider %q: unsupported type %q", name, pc.Type)
	}
}
