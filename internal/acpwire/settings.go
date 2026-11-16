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
	// The built-in Studio session's access surface. These are a user's
	// settings, so they are session-scoped like every other settings call and
	// reach the agent side only through the client seam: no tool and no model
	// content names them.
	MethodMarshalAccess         = "session/marshal_access"
	MethodSaveMarshalAccess     = "session/save_marshal_access"
	MethodSelectMarshalInstance = "session/select_marshal_instance"
)

// MarshalAccess is a built-in session's factory authority as the access
// surface reads and writes it. Every field is a pointer because "unset" and an
// explicit false are different answers: an unset field inherits the layer
// beneath it, and an explicit false is a denial that stays one until it is
// reset. The zero value therefore writes no change, which is exactly what a
// reset stores.
type MarshalAccess struct {
	Read              *bool   `json:"read,omitempty"`
	CreateDraft       *bool   `json:"createDraft,omitempty"`
	EditDraft         *bool   `json:"editDraft,omitempty"`
	AnswerQuestion    *bool   `json:"answerQuestion,omitempty"`
	ApproveAndQueue   *bool   `json:"approveAndQueue,omitempty"`
	ApprovalAuthority *string `json:"approvalAuthority,omitempty"` // none, user_directed, standing
}

// MarshalAccessLayer is one level's stored access and the opaque version a
// write to it must name. A layer is the project's defaults when the view
// carries no override, and one session's (or worker's) override otherwise.
type MarshalAccessLayer struct {
	Access  MarshalAccess `json:"access"`
	Version string        `json:"version"`
}

// MarshalAccessView is one session's Studio connection and effective built-in
// access. State is the connection: connected, ambiguous, unavailable,
// unsupported or unauthorized. The layers and the effective access are the
// policy, which is stored whether or not a connection is up.
//
// Stale is set on the answer to a write the server refused because the version
// it named had moved: the view then carries the value that won, so an editor
// shows the conflict instead of re-reading blind. A write is otherwise an
// ordinary answer, and a surface never treats a conflict as a failed call.
type MarshalAccessView struct {
	State      string   `json:"state"`
	Reason     string   `json:"reason,omitempty"`
	InstanceID string   `json:"instanceId,omitempty"`
	Generation string   `json:"generation,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	WorkerID   string   `json:"workerId,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	// SelectedProjectID is the project picked for a session whose enrollment
	// was ambiguous. SelectionVersion is the version a selection write names.
	SelectedProjectID string              `json:"selectedProjectId,omitempty"`
	SelectionVersion  string              `json:"selectionVersion,omitempty"`
	Default           MarshalAccessLayer  `json:"default"`
	Override          *MarshalAccessLayer `json:"override,omitempty"`
	// OverrideVersion is the version a session-layer write must name. It is
	// present even when no override is stored: that level reports the stable
	// version of the absent value, so the first write from a surface that has
	// read it is conditional on exactly what it was shown.
	OverrideVersion string            `json:"overrideVersion,omitempty"`
	Effective       MarshalAccess     `json:"effective"`
	Source          map[string]string `json:"source,omitempty"`
	// Editable says this surface may write the layers. A connection that is
	// offline, ambiguous or unauthorized never makes it false on its own: the
	// policy is a stored setting.
	Editable bool `json:"editable"`
	// Stale says a write was refused because the version it named had moved.
	Stale bool `json:"stale,omitempty"`
}

// MarshalAccessParams is session/marshal_access's params.
type MarshalAccessParams struct {
	SessionID string `json:"sessionId"`
}

// The connection states an access view reports. They are the wire's words, so
// both surfaces name the same one, and a state is never a call's failure: an
// offline, ambiguous or unauthorized session is an answer.
const (
	MarshalAccessConnected    = "connected"
	MarshalAccessAmbiguous    = "ambiguous"
	MarshalAccessUnavailable  = "unavailable"
	MarshalAccessUnsupported  = "unsupported"
	MarshalAccessUnauthorized = "unauthorized"
)

// CodeMarshalAccessUnavailable is the code an access write returns when the
// session has no Studio bridge to write to. The read surface reports the same
// condition as a state, so only a write — which the user asked for — fails.
const CodeMarshalAccessUnavailable ErrorCode = "marshal_access_unavailable"

// The two levels an access write names. A project layer is the default every
// session of the project inherits; a session layer is the one session's (or
// worker's) override.
const (
	MarshalAccessScopeProject = "project"
	MarshalAccessScopeSession = "session"
)

// SaveMarshalAccessParams is session/save_marshal_access's params. Scope names
// the level: "project" writes the project's defaults and "session" this
// session's override. The session is always the caller's own: the client never
// names a session it does not hold.
type SaveMarshalAccessParams struct {
	SessionID string        `json:"sessionId"`
	Scope     string        `json:"scope"`
	Access    MarshalAccess `json:"access"`
	Version   string        `json:"version"`
}

// SelectMarshalInstanceParams is session/select_marshal_instance's params: the
// project an ambiguous session acts on, or none to clear the choice.
type SelectMarshalInstanceParams struct {
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId,omitempty"`
	Version   string `json:"version"`
}

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
// decision is pending, empty when none is, and whether a trust applies to the
// running session in place (a daemon session) rather than through the
// client's restart (embedded mode).
type TrustPromptResult struct {
	Dir     string `json:"dir,omitempty"`
	InPlace bool   `json:"inPlace,omitempty"`
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
