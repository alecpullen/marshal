package backend

import (
	"fmt"
	"strings"
)

// Provider constants for backend factory.
const (
	ProviderOllama    = "ollama"
	ProviderOpenAI    = "openai"
	ProviderFireworks = "fireworks"
	ProviderTogether  = "together"
	ProviderClaude    = "claude"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
	ProviderBedrock   = "bedrock"
)

// Factory creates the appropriate backend based on provider type.
// It returns a Backend implementation configured for the specified provider.
//
// Supported providers:
//   - "ollama": Native Ollama API (/api/chat)
//   - "openai", "fireworks", "together": OpenAI-compatible endpoints
//   - "claude", "anthropic": Native Anthropic Claude API
//   - "gemini": Native Google Gemini API
//   - "bedrock": AWS Bedrock
//   - "": Falls back to OpenAI-compatible (backward compatible)
//
// Example:
//
//	be, err := backend.Factory("ollama", "executor", "http://localhost:11434", "")
//	if err != nil {
//	    log.Fatal(err)
//	}
func Factory(provider, name, baseURL, apiKey string) (Backend, error) {
	// Normalize provider string
	provider = strings.ToLower(strings.TrimSpace(provider))

	switch provider {
	case ProviderOllama:
		return NewOllamaBackend(name, baseURL), nil

	case ProviderOpenAI, ProviderFireworks, ProviderTogether:
		// These all use OpenAI-compatible endpoints with provider-specific defaults
		return newOpenAICompatible(provider, name, baseURL, apiKey), nil

	case ProviderClaude, ProviderAnthropic:
		// Native Anthropic Claude API
		// baseURL is ignored for Claude (always uses api.anthropic.com)
		// apiKey is required
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required for %s provider", provider)
		}
		return NewClaudeBackend(name, apiKey), nil

	case ProviderGemini:
		// Native Google Gemini API
		// baseURL is ignored (always uses generativelanguage.googleapis.com)
		// apiKey is required
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required for %s provider", provider)
		}
		return NewGeminiBackend(name, apiKey), nil

	case ProviderBedrock:
		// AWS Bedrock
		// apiKey is not used (credentials from AWS environment)
		// baseURL can specify region (e.g., "us-east-1")
		return NewBedrockBackend(name, baseURL), nil

	case "":
		// Backward compatibility: missing provider = OpenAI-compatible
		return newOpenAICompatible("openai", name, baseURL, apiKey), nil

	default:
		// Unknown provider: try OpenAI-compatible as fallback
		return newOpenAICompatible(provider, name, baseURL, apiKey), nil
	}
}

// newOpenAICompatible creates an OpenAICompatibleBackend with provider-specific defaults.
func newOpenAICompatible(provider, name, baseURL, apiKey string) *OpenAICompatibleBackend {
	be := NewOpenAICompatible(name, baseURL, apiKey)

	// Apply provider-specific defaults
	switch provider {
	case ProviderFireworks:
		// Fireworks benefits from session affinity for KV cache reuse
		// Session ID will be set per-request in the orchestrator
		// Default temperature 0 for deterministic output
		be = be.WithTemperature(0.0)

	case ProviderTogether:
		// Together AI standard settings
		be = be.WithTemperature(0.2)

	case ProviderOpenAI:
		// OpenAI standard settings
		be = be.WithTemperature(0.2)
	}

	return be
}

// applyAgentConfig applies common agent configuration to any backend.
func applyAgentConfig(be Backend, temperature float64, maxTokens int, jsonOutput bool, contextWindow int) Backend {
	switch b := be.(type) {
	case *OpenAICompatibleBackend:
		b = b.WithTemperature(temperature).WithMaxTokens(maxTokens)
		if jsonOutput {
			b = b.WithJSONOutput(true)
		}
		return b

	case *OllamaBackend:
		b = b.WithTemperature(temperature).WithMaxTokens(maxTokens)
		if contextWindow > 0 {
			b = b.WithContextWindow(contextWindow)
		}
		_ = jsonOutput
		return b

	case *ClaudeBackend:
		b = b.WithTemperature(temperature).WithMaxTokens(maxTokens)
		return b

	case *GeminiBackend:
		b = b.WithTemperature(temperature).WithMaxTokens(maxTokens)
		return b

	case *BedrockBackend:
		b = b.WithTemperature(temperature).WithMaxTokens(maxTokens)
		return b
	}

	return be
}

// FactoryForAgent creates a backend for a specific agent role using configuration.
// It applies role-specific defaults (temperature, max_tokens, JSON output).
//
// The agentType parameter is used for:
//   - Naming the backend (appears in logs)
//   - Applying role-specific behavior
//
// Supported agent types: "executor", "critic", "marshal", "planner", "compactor"
func FactoryForAgent(provider, agentType, baseURL, apiKey string, temperature float64, maxTokens int, jsonOutput bool, contextWindow int) (Backend, error) {
	be, err := Factory(provider, agentType, baseURL, apiKey)
	if err != nil {
		return nil, err
	}

	return applyAgentConfig(be, temperature, maxTokens, jsonOutput, contextWindow), nil
}

// ValidateProvider checks if a provider string is recognized.
// Returns an error for unknown providers (except empty string which is valid).
func ValidateProvider(provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))

	switch provider {
	case "", ProviderOllama, ProviderOpenAI, ProviderFireworks, ProviderTogether,
		ProviderClaude, ProviderAnthropic, ProviderGemini, ProviderBedrock:
		return nil
	default:
		return fmt.Errorf("unknown provider %q (known: %s, %s, %s, %s, %s, %s, %s)",
			provider, ProviderOllama, ProviderOpenAI, ProviderFireworks, ProviderTogether,
			ProviderClaude, ProviderGemini, ProviderBedrock)
	}
}
