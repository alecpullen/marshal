package provider

import (
	"fmt"
	"os"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/modelcache"
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
// dataDir is the application data directory used for the model cache. Pass ""
// to disable caching.
//
// thinkingBudgetMargin is the AgentConfig.ThinkingBudgetMargin value,
// honored only by the native Anthropic backend. It is threaded separately
// because NewFromConfig receives a ProviderConfig, which has no access to
// the [agent] section.
func NewFromConfig(name string, pc config.ProviderConfig, dataDir string, thinkingBudgetMargin int) (Provider, error) {
	return NewFromConfigWithSession(name, pc, dataDir, thinkingBudgetMargin, "")
}

// NewFromConfigWithSession is NewFromConfig with a stable per-conversation
// session identifier, threaded into OpenAI-compatible providers so chat
// requests carry x-opencode-session (OpenCode Zen requires it for routing
// and prompt caching). Callers without a conversation — model listing,
// capability probing — should use NewFromConfig instead.
func NewFromConfigWithSession(name string, pc config.ProviderConfig, dataDir string, thinkingBudgetMargin int, sessionID string) (Provider, error) {
	return NewFromConfigWithAuth(name, pc, dataDir, thinkingBudgetMargin, sessionID, DefaultAuthResolver())
}

// NewFromConfigWithAuth is NewFromConfigWithSession with an explicit auth
// resolver: every credential-backed decision — today, the OAuth engine behind
// an openai_codex provider — comes from auth instead of the process-wide
// default. That lets a caller hold a private engine cache, so two callers
// cannot observe each other's tokens.
//
// Passing a nil auth is an error; callers that want production behavior pass
// DefaultAuthResolver() explicitly.
func NewFromConfigWithAuth(name string, pc config.ProviderConfig, dataDir string, thinkingBudgetMargin int, sessionID string, auth AuthResolver) (Provider, error) {
	if auth == nil {
		return nil, fmt.Errorf("provider %q: auth resolver is required", name)
	}
	switch pc.Type {
	case "", "openai_compatible":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		caps := DefaultCapabilities()
		caps.ToolCalling = pc.ToolCalling
		caps.TemperatureLocked = pc.TemperatureLocked

		return NewOpenAICompatible(Options{
			ThinkingLookup:   func(model string) *schema.ThinkingOptions { return cachedThinkingOptions(dataDir, name, pc)[model] },
			Name:             name,
			BaseURL:          pc.BaseURL,
			APIKey:           apiKey,
			Capabilities:     &caps,
			ReasoningSummary: pc.ReasoningSummary,
			ReasoningField:   pc.ReasoningField,
			SessionID:        sessionID,
		})
	case "ollama":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		caps := DefaultCapabilities()
		caps.ToolCalling = pc.ToolCalling
		// Structured output (format / response_format enforcement) is a
		// per-provider opt-in mirroring tool_calling: ollama.com cloud
		// silently ignores format constraints (verified 2026-09-03), and
		// /api/show cannot distinguish enforcing from non-enforcing
		// endpoints, so default false and let the config opt in where
		// format is proven enforced (e.g. a local Ollama server).
		caps.JSONMode = pc.StructuredOutput
		caps.StructuredOutput = pc.StructuredOutput
		caps.TemperatureLocked = pc.TemperatureLocked
		// Native Ollama encodes effort through its model-specific think field.
		caps.Reasoning = true
		keepAlive := pc.KeepAlive
		if keepAlive == "" {
			keepAlive = defaultOllamaKeepAlive
		}
		return NewOllamaNative(Options{
			ThinkingLookup: func(model string) *schema.ThinkingOptions { return cachedThinkingOptions(dataDir, name, pc)[model] },
			Name:           name,
			BaseURL:        pc.BaseURL,
			APIKey:         apiKey,
			Capabilities:   &caps,
			KeepAlive:      keepAlive,
			ReasoningField: pc.ReasoningField,
		})
	case "anthropic":
		apiKey, err := ResolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q: anthropic providers require api_key or api_key_env", name)
		}
		caps := schema.ProviderCapabilities{ToolCalling: true, JSONMode: false, StructuredOutput: false, Reasoning: true, TemperatureLocked: pc.TemperatureLocked}
		return NewAnthropic(Options{
			Name:                 name,
			BaseURL:              pc.BaseURL,
			APIKey:               apiKey,
			Capabilities:         &caps,
			ThinkingBudget:       pc.ThinkingBudget,
			ModelThinking:        cachedThinkingOptions(dataDir, name, pc),
			ThinkingLookup:       func(model string) *schema.ThinkingOptions { return cachedThinkingOptions(dataDir, name, pc)[model] },
			ThinkingBudgetMargin: thinkingBudgetMargin,
		})
	case "openai_codex":
		// The resolver's engine: under the default adapter that is the
		// process-wide shared instance the refresh worker also holds, so
		// their refreshes serialize on one mutex. An injected resolver
		// hands back an engine from its own private cache instead.
		engine, err := auth.CodexOAuthEngine(name)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		// Capabilities are hardcoded from the spike's verified results
		// (docs/codex-spike-findings-2026-09-14.md §6): tool calling,
		// structured output, and reasoning all PASS. Streaming is implied
		// — the endpoint only accepts stream=true.
		caps := schema.ProviderCapabilities{
			ToolCalling:       true,
			JSONMode:          false,
			StructuredOutput:  true,
			Reasoning:         true,
			TemperatureLocked: pc.TemperatureLocked,
		}
		return NewOpenAICodex(CodexOptions{
			Name:             name,
			BaseURL:          pc.BaseURL,
			Engine:           engine,
			Capabilities:     &caps,
			ReasoningSummary: pc.ReasoningSummary,
			StaticModels:     codexStaticModels(pc),
			DataDir:          dataDir,
			ProviderConfig:   pc,
			SessionID:        sessionID,
			ReasoningField:   pc.ReasoningField,
		})
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, pc.Type)
	}
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

func cachedThinkingOptions(dataDir, name string, pc config.ProviderConfig) map[string]*schema.ThinkingOptions {
	if dataDir == "" {
		return nil
	}
	models, ok := modelcache.Load(dataDir).Lookup(name, pc, modelcache.DefaultTTL, time.Now())
	if !ok {
		return nil
	}
	options := make(map[string]*schema.ThinkingOptions)
	for _, model := range models {
		if model.Thinking != nil {
			options[model.ID] = model.Thinking
		}
	}
	return options
}
