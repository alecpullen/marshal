package contract

// The models and routing document is what the control agent's `config/get`
// answers, which GET /api/models passes through unchanged
// (internal/acp/config.go, web/bridge/models.go). Providers and presets are
// user-global only: nothing here is ever saved to a project config.

// ProviderView is one provider as clients see it. A provider's key is never
// part of it: HasKey and KeySource are all a client learns, and
// PUT /api/models/providers/{name}/key is the only way a key goes in.
type ProviderView struct {
	Type        string `json:"type"`
	BaseURL     string `json:"baseUrl"`
	APIKeyEnv   string `json:"apiKeyEnv,omitempty"`
	ToolCalling bool   `json:"toolCalling"`
	Template    string `json:"template,omitempty"`
	Auth        string `json:"auth,omitempty"`
	KeepAlive   string `json:"keepAlive,omitempty"`
	// ThinkingBudget, ReasoningSummary, ReasoningField, StructuredOutput and
	// TemperatureLocked are the provider's own knobs, passed through.
	ThinkingBudget    int    `json:"thinkingBudget,omitempty"`
	ReasoningSummary  bool   `json:"reasoningSummary,omitempty"`
	ReasoningField    string `json:"reasoningField,omitempty"`
	StructuredOutput  bool   `json:"structuredOutput,omitempty"`
	TemperatureLocked bool   `json:"temperatureLocked,omitempty"`
	// HasKey says a key can be read for this provider now; KeySource says
	// where from: config, env or none.
	HasKey    bool   `json:"hasKey"`
	KeySource string `json:"keySource"`
}

// ProviderWrite is one provider as PUT /api/models/providers takes it: the
// same record without the read-only credential facts.
type ProviderWrite struct {
	Type              string `json:"type"`
	BaseURL           string `json:"baseUrl"`
	APIKeyEnv         string `json:"apiKeyEnv,omitempty"`
	ToolCalling       bool   `json:"toolCalling"`
	Template          string `json:"template,omitempty"`
	Auth              string `json:"auth,omitempty"`
	KeepAlive         string `json:"keepAlive,omitempty"`
	ThinkingBudget    int    `json:"thinkingBudget,omitempty"`
	ReasoningSummary  bool   `json:"reasoningSummary,omitempty"`
	ReasoningField    string `json:"reasoningField,omitempty"`
	StructuredOutput  bool   `json:"structuredOutput,omitempty"`
	TemperatureLocked bool   `json:"temperatureLocked,omitempty"`
}

// ProvidersUpdate is PUT /api/models/providers' body. A null provider is
// removed.
type ProvidersUpdate struct {
	Providers map[string]*ProviderWrite `json:"providers"`
}

// Preset is one model preset: a provider and a model with the limits it is
// run with.
type Preset struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	ContextWindow   int      `json:"contextWindow,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	ToolCalling     string   `json:"toolCalling,omitempty"`
	LocalOnly       bool     `json:"localOnly,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	// VerificationGate turns the gate off for a preset that cannot reach it.
	VerificationGate *bool `json:"verificationGate,omitempty"`
}

// PresetsUpdate is PUT /api/models/presets' body. A null preset is removed.
type PresetsUpdate struct {
	Presets map[string]*Preset `json:"presets"`
}

// Binding is what one role runs: a preset, or a custom agent.
type Binding struct {
	Preset      string `json:"preset,omitempty"`
	CustomAgent string `json:"customAgent,omitempty"`
}

// RoutingUpdate is PUT /api/models/routing' body: the parts of the routing
// document a client changes. An omitted part is left as it was.
type RoutingUpdate struct {
	Profiles       map[string]map[string]Binding `json:"profiles,omitempty"`
	DefaultProfile *string                       `json:"defaultProfile,omitempty"`
	ActivePreset   *string                       `json:"activePreset,omitempty"`
}

// ProviderKey is PUT /api/models/providers/{name}/key's body. It goes one way:
// no route answers a key back.
type ProviderKey struct {
	Key string `json:"key"`
}

// ProbeRequest is POST /api/models/probe's body.
type ProbeRequest struct {
	Name string `json:"name"`
}

// ProbeModel is one model a probe found.
type ProbeModel struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// ProbeResult is POST /api/models/probe's answer. A probe that failed is a
// result with Error set, not a refusal.
type ProbeResult struct {
	Models []ProbeModel `json:"models"`
	Error  string       `json:"error,omitempty"`
}

// ModelsConfig is GET /api/models' answer: the user-global config's providers,
// presets, roles and caps.
type ModelsConfig struct {
	Providers map[string]ProviderView `json:"providers"`
	Presets   map[string]Preset       `json:"presets"`
	// Profiles maps a profile name to its roles, and a role to its binding.
	Profiles       map[string]map[string]Binding `json:"profiles"`
	CustomAgents   []string                      `json:"customAgents"`
	DefaultProfile string                        `json:"defaultProfile"`
	ActivePreset   string                        `json:"activePreset"`
	Roles          []string                      `json:"roles"`
	Budgets        Budgets                       `json:"budgets"`
}

// BudgetsUpdate is PUT /api/budgets' body: the caps, written to the user's
// global config, which answers the report it stored.
type BudgetsUpdate struct {
	Budgets Budgets `json:"budgets"`
}
