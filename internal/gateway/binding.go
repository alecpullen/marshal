package gateway

import (
	"fmt"
	"strings"
)

// Provider identifies the model provider.
type Provider string

const (
	ProviderAnthropic  Provider = "anthropic"
	ProviderOpenAI     Provider = "openai"
	ProviderOpenRouter Provider = "openrouter"
	ProviderFireworks  Provider = "fireworks"
	ProviderRunPod     Provider = "runpod"
	ProviderOllama     Provider = "ollama"
	ProviderLMStudio   Provider = "lmstudio"
	ProviderVLLM       Provider = "vllm"
)

// IsValid reports whether the provider is a known value.
func (p Provider) IsValid() bool {
	switch p {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter,
		ProviderFireworks, ProviderRunPod, ProviderOllama,
		ProviderLMStudio, ProviderVLLM:
		return true
	}
	return false
}

// String returns the string representation.
func (p Provider) String() string {
	return string(p)
}

// SupportsThinking reports if this provider supports extended thinking.
func (p Provider) SupportsThinking() bool {
	switch p {
	case ProviderAnthropic:
		return true // Claude with extended thinking beta
	default:
		return false
	}
}

// SupportsLoRA reports if this provider supports LoRA adapters.
func (p Provider) SupportsLoRA() bool {
	switch p {
	case ProviderFireworks, ProviderVLLM, ProviderRunPod:
		return true
	default:
		return false
	}
}

// RoleHint guides model selection within a provider category.
type RoleHint string

const (
	RoleHintSmall RoleHint = "small"  // Fast, cheap models (e.g., Haiku, GPT-4-mini)
	RoleHintCode  RoleHint = "code"   // Code-optimized models (e.g., Claude Sonnet, Qwen-Coder)
	RoleHintLarge RoleHint = "large"  // Powerful models (e.g., Claude Opus, GPT-4)
	RoleHintFast  RoleHint = "fast"   // Low-latency models
)

// IsValid reports whether the role hint is known.
func (r RoleHint) IsValid() bool {
	switch r {
	case RoleHintSmall, RoleHintCode, RoleHintLarge, RoleHintFast:
		return true
	}
	return false
}

// Binding defines how a role connects to a model provider.
type Binding struct {
	// Provider is the model provider (e.g., "anthropic", "openai", "ollama").
	Provider Provider `json:"provider"`

	// Model is the specific model name (e.g., "claude-opus-4-7", "gpt-4", "qwen2.5-coder:14b").
	Model string `json:"model"`

	// Endpoint is an optional override URL for the provider API.
	// If empty, uses the provider's default endpoint.
	Endpoint string `json:"endpoint,omitempty"`

	// APIKey is the authentication key for the provider.
	// In production, this should reference a secret store rather than being stored directly.
	APIKey string `json:"-"` // Never serialized

	// AuthRef is a reference to where the API key is stored (e.g., "env:ANTHROPIC_API_KEY").
	AuthRef string `json:"auth_ref,omitempty"`

	// LoRAs is a list of LoRA adapter IDs/paths for providers that support them.
	// In v1, only 0 or 1 LoRA is supported. Multiple LoRAs is a warning.
	LoRAs []string `json:"loras,omitempty"`

	// RoleHint guides model selection when multiple models are available.
	RoleHint RoleHint `json:"role_hint,omitempty"`

	// Priority for auto-selection (higher = preferred when multiple providers available).
	// Anthropic=100, OpenAI=80, Local=50 by default.
	Priority int `json:"priority,omitempty"`

	// CostPer1KInput is the cost per 1000 input tokens (for budgeting).
	CostPer1KInput float64 `json:"cost_per_1k_input,omitempty"`

	// CostPer1KOutput is the cost per 1000 output tokens (for budgeting).
	CostPer1KOutput float64 `json:"cost_per_1k_output,omitempty"`
}

// NewBinding creates a new binding with defaults.
func NewBinding(provider Provider, model string) Binding {
	b := Binding{
		Provider: provider,
		Model:    model,
		Priority: DefaultPriority(provider),
	}
	b.SetDefaultCosts()
	return b
}

// DefaultPriority returns the default priority for a provider.
func DefaultPriority(p Provider) int {
	switch p {
	case ProviderAnthropic:
		return 100 // Highest priority
	case ProviderOpenAI:
		return 80
	case ProviderOpenRouter:
		return 70
	case ProviderFireworks:
		return 60
	case ProviderOllama, ProviderLMStudio, ProviderVLLM:
		return 50 // Local models lower priority by default
	case ProviderRunPod:
		return 55
	default:
		return 50
	}
}

// SetDefaultCosts sets default cost estimates for known providers.
func (b *Binding) SetDefaultCosts() {
	switch b.Provider {
	case ProviderAnthropic:
		switch {
		case strings.Contains(b.Model, "opus"):
			b.CostPer1KInput = 15.0
			b.CostPer1KOutput = 75.0
		case strings.Contains(b.Model, "sonnet"):
			b.CostPer1KInput = 3.0
			b.CostPer1KOutput = 15.0
		case strings.Contains(b.Model, "haiku"):
			b.CostPer1KInput = 0.25
			b.CostPer1KOutput = 1.25
		default:
			b.CostPer1KInput = 3.0
			b.CostPer1KOutput = 15.0
		}
	case ProviderOpenAI:
		switch {
		case strings.Contains(b.Model, "gpt-4") && strings.Contains(b.Model, "mini"):
			b.CostPer1KInput = 0.15
			b.CostPer1KOutput = 0.60
		case strings.Contains(b.Model, "gpt-4"):
			b.CostPer1KInput = 30.0
			b.CostPer1KOutput = 60.0
		case strings.Contains(b.Model, "gpt-3.5"):
			b.CostPer1KInput = 0.5
			b.CostPer1KOutput = 1.5
		default:
			b.CostPer1KInput = 10.0
			b.CostPer1KOutput = 30.0
		}
	case ProviderOllama, ProviderLMStudio, ProviderVLLM:
		// Local models are free (compute cost only)
		b.CostPer1KInput = 0.0
		b.CostPer1KOutput = 0.0
	default:
		// Default estimates for unknown providers
		b.CostPer1KInput = 5.0
		b.CostPer1KOutput = 15.0
	}
}

// String returns a human-readable representation (e.g., "anthropic/claude-opus-4-7").
func (b Binding) String() string {
	if b.Endpoint != "" {
		return fmt.Sprintf("%s/%s@%s", b.Provider, b.Model, b.Endpoint)
	}
	return fmt.Sprintf("%s/%s", b.Provider, b.Model)
}

// Validate checks if the binding is valid.
func (b Binding) Validate() error {
	if !b.Provider.IsValid() {
		return fmt.Errorf("%w: invalid provider %q", ErrInvalidBinding, b.Provider)
	}
	if b.Model == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidBinding)
	}

	// Check LoRA constraints for v1
	if len(b.LoRAs) > 1 {
		// Warning: v1 only supports 0 or 1 LoRA
		return fmt.Errorf("%w: v1 supports at most 1 LoRA, got %d", ErrInvalidBinding, len(b.LoRAs))
	}

	// Validate LoRA support
	if len(b.LoRAs) > 0 && !b.Provider.SupportsLoRA() {
		return fmt.Errorf("%w: provider %s does not support LoRA", ErrInvalidBinding, b.Provider)
	}

	return nil
}

// WithEndpoint returns a new binding with the endpoint overridden.
func (b Binding) WithEndpoint(endpoint string) Binding {
	b.Endpoint = endpoint
	return b
}

// WithAPIKey returns a new binding with the API key set.
func (b Binding) WithAPIKey(key string) Binding {
	b.APIKey = key
	return b
}

// WithLoRA returns a new binding with a LoRA attached.
func (b Binding) WithLoRA(lora string) Binding {
	b.LoRAs = []string{lora}
	return b
}

// WithPriority returns a new binding with priority overridden.
func (b Binding) WithPriority(priority int) Binding {
	b.Priority = priority
	return b
}

// IsLocal returns true if this is a local provider (Ollama, LM Studio, vLLM).
func (b Binding) IsLocal() bool {
	switch b.Provider {
	case ProviderOllama, ProviderLMStudio, ProviderVLLM:
		return true
	default:
		return false
	}
}

// IsCloud returns true if this is a cloud provider.
func (b Binding) IsCloud() bool {
	return !b.IsLocal()
}

// SupportsTools reports if this binding supports tool use.
// This is a heuristic based on provider and model name.
func (b Binding) SupportsTools() bool {
	// Most modern models support tools
	switch b.Provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter, ProviderFireworks:
		return true
	case ProviderOllama, ProviderLMStudio, ProviderVLLM:
		// Local models vary - check for known tool-capable models
		toolModels := []string{"qwen", "llama3", "mistral", "mixtral", "command-r"}
		modelLower := strings.ToLower(b.Model)
		for _, m := range toolModels {
			if strings.Contains(modelLower, m) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// SupportsThinking reports if this binding supports extended thinking.
func (b Binding) SupportsThinking() bool {
	return b.Provider.SupportsThinking() && strings.Contains(strings.ToLower(b.Model), "claude")
}

// EstimateCost estimates the cost for a request.
func (b Binding) EstimateCost(inputTokens, outputTokens int) float64 {
	inputCost := float64(inputTokens) * b.CostPer1KInput / 1000.0
	outputCost := float64(outputTokens) * b.CostPer1KOutput / 1000.0
	return inputCost + outputCost
}

// --- Binding Resolution ---

// ResolvedBinding contains a binding plus metadata about how it was chosen.
type ResolvedBinding struct {
	Binding  Binding
	IsPrimary bool
	IsFallback bool
	Reason   string // Why this binding was chosen
}

// --- Environment-based Auth Resolution ---

// ResolveAuth resolves the API key from AuthRef.
// Supported formats:
//   - "env:VAR_NAME" - read from environment variable
//   - "file:/path/to/key" - read from file
//   - "key:direct_key" - use directly (not recommended)
func ResolveAuth(authRef string) (string, error) {
	if authRef == "" {
		return "", nil
	}

	// Check for prefix
	if strings.HasPrefix(authRef, "env:") {
		varName := strings.TrimPrefix(authRef, "env:")
		value := getEnv(varName)
		if value == "" {
			return "", fmt.Errorf("environment variable %s not set", varName)
		}
		return value, nil
	}

	if strings.HasPrefix(authRef, "file:") {
		// path := strings.TrimPrefix(authRef, "file:")
		// Read file would go here - simplified for now
		return "", fmt.Errorf("file-based auth not yet implemented: %s", authRef)
	}

	if strings.HasPrefix(authRef, "key:") {
		return strings.TrimPrefix(authRef, "key:"), nil
	}

	// No prefix, assume it's the key directly (or env var name for backward compat)
	value := getEnv(authRef)
	if value != "" {
		return value, nil
	}

	// Try common provider env vars as fallback
	return resolveProviderEnv(authRef)
}

// getEnv is a wrapper for os.Getenv that can be mocked in tests.
var getEnv = func(key string) string {
	// This will be replaced with actual os.Getenv in real implementation
	return ""
}

// resolveProviderEnv tries standard environment variable names.
func resolveProviderEnv(provider string) (string, error) {
	envVars := map[string]string{
		"anthropic":   "ANTHROPIC_API_KEY",
		"openai":      "OPENAI_API_KEY",
		"openrouter":  "OPENROUTER_API_KEY",
		"fireworks":   "FIREWORKS_API_KEY",
		"runpod":      "RUNPOD_API_KEY",
	}

	if varName, ok := envVars[provider]; ok {
		return getEnv(varName), nil
	}

	return "", fmt.Errorf("unknown provider %s", provider)
}

// SetGetEnv sets the getEnv function (for testing).
func SetGetEnv(fn func(string) string) {
	getEnv = fn
}
