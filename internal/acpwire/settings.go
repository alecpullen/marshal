package acpwire

import "encoding/json"

// The settings methods and update (P3 plan 7a).
const (
	MethodProviderTemplates  = "config/provider_templates"
	MethodProbeProvider      = "config/probe_provider"
	MethodProbeModel         = "config/probe_model"
	MethodModelsList         = "config/models_list"
	MethodModelsSave         = "config/models_save"
	MethodModelsForget       = "config/models_forget"
	MethodModelsRename       = "config/models_rename"
	MethodLoginProvider      = "config/login_provider"
	MethodSetValue           = "config/set_value"
	MethodSaveProviders      = "config/save_providers"
	MethodSetProviderKey     = "config/set_provider_key"
	MethodSavePresets        = "config/save_presets"
	MethodPreparePresets     = "config/prepare_presets"
	MethodSaveProvenance     = "config/save_provenance"
	MethodTrustPrompt        = "trust/prompt"
	MethodTrustDecide        = "trust/decide"
	MethodSessionConfig      = "session/config"
	MethodApplyConfig        = "session/apply_config"
	MethodReloadConfig       = "session/reload_config"
	MethodMCPReconnect       = "session/mcp_reconnect"
	MethodSaveProjectConfig  = "session/save_project_config"
	MethodSaveVerifyCommands = "session/save_verify_commands"
	MethodMCPLogin           = "session/mcp_login"
	UpdateSessionConfig      = "session_config"
)

// HostCallParams names the session whose runtime serves a host call: the
// client's current session. config/provider_templates and trust/prompt take
// only this.
type HostCallParams struct {
	SessionID string `json:"sessionId"`
}

// ProviderParams is config/probe_provider's params from a marshal client, and
// config/probe_model's. Provider is a config.ProviderConfig, and may carry a
// literal key (P3 R8). Model and Check are config/probe_model's: Check asks
// CanProbeModel's question instead of probing Model.
type ProviderParams struct {
	SessionID string          `json:"sessionId"`
	Name      string          `json:"name"`
	Provider  json.RawMessage `json:"provider"`
	Model     string          `json:"model,omitempty"`
	Check     bool            `json:"check,omitempty"`
}

// CanProbeResult is config/probe_model's result for a check.
type CanProbeResult struct {
	CanProbe bool `json:"canProbe"`
}

// ModelsParams is config/models_list's and config/models_save's params.
// Models is a []schema.ModelInfo, for a save.
type ModelsParams struct {
	SessionID string          `json:"sessionId"`
	Name      string          `json:"name"`
	Models    json.RawMessage `json:"models,omitempty"`
}

// ModelsResult is config/probe_provider's result for a marshal client, and
// config/models_list's. Models is a []schema.ModelInfo; Found is
// ListModels' second result.
type ModelsResult struct {
	Models json.RawMessage `json:"models,omitempty"`
	Found  bool            `json:"found,omitempty"`
}

// RenameModelsParams is config/models_rename's params.
type RenameModelsParams struct {
	SessionID string `json:"sessionId"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// LoginParams is config/login_provider's and session/mcp_login's params.
// Name is the provider or the MCP server; Prepare asks whether the sign-in
// can start instead of running it.
type LoginParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Prepare   bool   `json:"prepare,omitempty"`
}

// LoginProgress is a sign-in's $/progress value: the URL to open.
type LoginProgress struct {
	URL string `json:"url"`
}

// SetValueParams is config/set_value's params. Value is the JSON of the value
// at Path; the agent decodes it into the type of the config field there.
type SetValueParams struct {
	SessionID string          `json:"sessionId"`
	Path      string          `json:"path"`
	Value     json.RawMessage `json:"value"`
}

// SaveProvidersParams is config/save_providers' params. Providers and
// Baseline are map[string]config.ProviderConfig.
type SaveProvidersParams struct {
	SessionID string            `json:"sessionId"`
	Providers json.RawMessage   `json:"providers"`
	Baseline  json.RawMessage   `json:"baseline,omitempty"`
	Renamed   map[string]string `json:"renamed,omitempty"`
}

// ProviderKeyParams is config/set_provider_key's params from a marshal
// client. Key is a secret (P3 R8).
type ProviderKeyParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

// SavePresetsParams is config/save_presets' params. Presets and Baseline are
// map[string]routing.ModelPreset.
type SavePresetsParams struct {
	SessionID string          `json:"sessionId"`
	Presets   json.RawMessage `json:"presets"`
	Baseline  json.RawMessage `json:"baseline,omitempty"`
}

// PreparePresetsParams is config/prepare_presets' params. Config is a
// config.Config and Models a []schema.ModelInfo.
type PreparePresetsParams struct {
	SessionID string          `json:"sessionId"`
	Config    json.RawMessage `json:"config"`
	Provider  string          `json:"provider"`
	BaseURL   string          `json:"baseUrl,omitempty"`
	Models    json.RawMessage `json:"models,omitempty"`
}

// PreparedPresets is config/prepare_presets' result. Presets is a
// map[string]routing.ModelPreset.
type PreparedPresets struct {
	Presets json.RawMessage `json:"presets,omitempty"`
	Setup   PresetSetup     `json:"setup"`
}

// ProvenanceParams is config/save_provenance's params.
type ProvenanceParams struct {
	SessionID  string          `json:"sessionId"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
}

// TrustPromptResult is trust/prompt's result: the directory whose trust
// decision is pending, empty when none is.
type TrustPromptResult struct {
	Dir string `json:"dir,omitempty"`
}

// TrustDecideParams is trust/decide's params.
type TrustDecideParams struct {
	SessionID string        `json:"sessionId"`
	Dir       string        `json:"dir"`
	Decision  TrustDecision `json:"decision"`
}

// SessionConfig is what the config mirror holds: the session's config and
// layers with every secret masked, and its tools. Config is a config.Config
// and Layers a config.Layers.
type SessionConfig struct {
	Config     json.RawMessage `json:"config"`
	Layers     json.RawMessage `json:"layers"`
	Tools      []string        `json:"tools,omitempty"`
	ToolsReady bool            `json:"toolsReady,omitempty"`
}

// SessionConfigUpdate is a session_config update: the whole value, sent
// whenever it differs from the last one sent on the connection.
type SessionConfigUpdate struct {
	Kind   string        `json:"kind"`
	Config SessionConfig `json:"config"`
}

// ConfigParams is session/config's params, and session/apply_config's,
// reload_config's and save_project_config's, whose Config is a config.Config.
type ConfigParams struct {
	SessionID string          `json:"sessionId"`
	Config    json.RawMessage `json:"config,omitempty"`
}

// ReloadResult is session/reload_config's and session/mcp_reconnect's result:
// whether the session has a reload path, and the reload's error, which a
// JSON-RPC error response could not carry with the flag.
type ReloadResult struct {
	Reloaded bool   `json:"reloaded"`
	Error    *Error `json:"error,omitempty"`
}

// VerifyCommandsParams is session/save_verify_commands' params.
type VerifyCommandsParams struct {
	SessionID string `json:"sessionId"`
	Build     string `json:"build,omitempty"`
	Test      string `json:"test,omitempty"`
}
