# P3 plan 7a: Config, providers and trust — implementation plan

Date: 2026-11-08. Status: ready for review.
Base: `main` at `725abd3c` (P3 plans 4b and 5 merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-base-suite`, `-timeout 20m`; `web/bridge` 85s).
- **It runs alongside P3 plan 6b** ([turns and controls](2026-11-08-p3-6b-controls-plan.md)). Both plans add a manager to `internal/acp`, the same `marshalOr` helper, a fence to the response barrier, a case to the client's router, methods to the conformance harness's `seam`, and rows to `acpclient.Methods`. "Integration with plan 6b" (at the end) lists the conflicts this causes and how to resolve each one.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Decided with the user (2026-11-08)", "Methods P3 adds" (plans 6b, 6c, 7a and 7b), R2's host-call note, R3's table and its host-reads bullet, R8, R9's list, and Migration order items 6 and 7. Task 7 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (the service behind the handlers), R2 (read your writes, cancellation, progress), R3 (the mirror: the `Config` row and host reads), R8 (secrets), R9 (compatibility), R10 (budgets), Migration order item 7a, and tests V1, V3, V5, V6 and V8.
Previous plans: [P3 plan 5, requests](2026-11-07-p3-5-requests-plan.md), [P3 plan 3b, status](2026-11-07-p3-3b-status-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client reads and writes the user's and the session's config, probes providers and models, signs in, and answers the trust prompt over ACP, all on the session's service.
- Sixteen new methods: `config/provider_templates`, `config/probe_model`, `config/models_list`, `config/models_save`, `config/models_forget`, `config/models_rename`, `config/login_provider`, `config/set_value`, `config/save_providers`, `config/save_presets`, `config/prepare_presets`, `config/save_provenance`, `trust/prompt`, `trust/decide`, `session/mcp_login` and `session/config`, plus five session writes: `session/apply_config`, `session/reload_config`, `session/mcp_reconnect`, `session/save_project_config` and `session/save_verify_commands`.
- For a marshal client, `config/probe_provider` and `config/set_provider_key` are served by the service. Every other client reaches today's handlers.
- A `session_config` update carries the session's config, layers and tools whenever they change, at most every 50ms, and before any call's response.
- Host calls name the client's current session, so the agent resolves that runtime's service host, and the barrier flushes that session.
- `rpc.Host` gains its eighteen settings methods and `rpc.Session` its ten; the conformance harness gains a host arm and their cases.

**Non-goals:**
- **No change for a client without the capability.** Its `config/get`, `config/set_*` and `config/probe_provider` are today's, byte for byte (R9).
- **No embedding trust prompt over ACP.** An ACP session's service is built by `ServiceDeps`, which sets no trust prompt, so `trust/prompt` answers none there. Plan 8 hands the embedding's prompt and decision to the agent host. This plan proves the two methods over a test `Deps` that carries them.
- **No notes, commands, browsers or project data.** Those are plan 7b's.
- **No change to the service's settings logic.** Mask restoring, the layer refresh, the trust refresh, the model cache, the preset setup and the sign-ins are the service's, unchanged. The handlers only decode and encode.
- **No change to `internal/app/tui`.** The interface's methods keep their signatures; only their `ACP:` doc lines change.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-7a-suite`. Create the directory first: `mkdir -p /private/tmp/p3-7a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 6b uses `/private/tmp/p3-6b-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier. Rerun a failure alone before treating it as new.
- **The service's host acts on its current session.** In `internal/acp/service`, the host's settings calls (`config.go`: `SaveUserValue` `:78`, `SaveUserProviders` `:141`, `SaveUserPresets` `:159`, `SaveProviderKey` `:175`, `PreparePresets` `:199`, `SaveProvenance` `:249`; `providers.go`: `ProviderTemplates` `:54` to `RenameModels` `:152`; `logins.go`: `PrepareProviderLogin` `:36`, `LoginProvider` `:47`; `trust.go`: `TrustPrompt` `:19`, `DecideTrust` `:27`) read the host's `Deps` and its current session's state: the layer refresh after a write, the thinking margin and the stored secrets a probe restores from. `service.New` (`service.go:301`) returns the host and its session together.
- **ACP keeps only the session.** `SessionManager.Service` (`internal/acp/session.go:715`) builds `service.New(rt.ServiceDeps(…))` once per runtime and keeps only the session in `services` (`:120`); `releaseService` (`:745`) releases it.
- **The session's settings calls** (`config.go`): `Config` (`:270`) and `Layers` (`:274`) return the state's values with every secret masked in place (P2 plan 7d); `ApplyConfig` (`:301`), `ReloadConfig` (`:316`), `ReconnectMCP` (`:338`), `SaveProjectConfig` (`:367`) and `SaveVerifyCommands` (`:387`) restore a masked secret before they install or write. `ReloadConfig` and `ReconnectMCP` return `(bool, error)`, where the bool says the session has a reload path even when the error is set; the TUI reads both (`model.go:767`, `mcp.go:60`). `Tools` is `trust.go:50`. `PrepareMCPLogin` and `LoginMCP` are `logins.go:58` and `:67`; a sign-in reports its URL through `show`.
- **Config values have no JSON tags.** `config.Config`, `config.Layers`, `config.ProviderConfig`, `schema.ModelInfo` and `routing.ModelPreset` encode with Go field names. Measured at the base: `config.Default()` round-trips through `encoding/json` unchanged (3,703 bytes, 9.4µs to encode), and so do layers loaded from a user config with two providers, a preset, an MCP server and a search key (13,379 bytes, 22.8µs). Two fields do not:
  - `Layers.HoistError` (`layers.go:56`) is an `error`: a set value encodes as `{}` and fails to decode. Nothing outside `internal/app/config` reads it.
  - `routing.ModelPreset.Pricing` (`routing/types.go:156`) is `any` holding a `*pricing.ModelPricing`. JSON decodes it as `map[string]any`, and `pricing.Lookup` (`pricing.go:23`) type-asserts the pointer, so a preset's price override would be lost silently. `coercePresetPricing` (`config/load.go:102`) fixes the same thing after a TOML decode, by re-decoding through TOML tags, which JSON's field names do not match.
- **`SaveUserValue`'s value is `any`.** The TUI passes what `config.LookupPath` (`layers.go:408`) returns: a value of the `Config` field's own Go type, named types included. `assignReflect` (`layers.go:549`) accepts an assignable value, or converts to an integer kind only. A JSON number decoded as `float64` into a named string or a float field would fail or change type.
- **ACP's config handlers differ.** `ConfigManager` (`internal/acp/config.go`) is built with the process's home directory (`host.go:429`), probes with its own wire shapes and answers a failed probe as a result (`ProbeProvider`, `:496`), and sets a key without the mask check (`SetProviderKey`, `:312`). They stay for other clients.
- **Handlers and managers.** `RequestManager` (`internal/acp/requests.go`) is the model to copy for the forwarder: `ensureForwarder` (`:250`), `runForwarder` (`:275`), a send only when the encoding changed. In `registerHandlers` (`host.go:328`) the barrier is installed at `:427`, the `config/*` methods are registered at `:430` to `:437`, and the cleanup closes the managers at `:585` to `:591`. `cancellable` (`server.go:443`) and `reportProgress` (`:471`) give a handler cancellation and `$/progress`; `Conn.CallWithProgress` (`rpc/conn.go:84`) hands the client each progress value.
- **The client.** `Host` (`rpc/dial.go:24`) holds the connection and the attached sessions; `Attach` (`rpc/session.go:54`) binds one. `routeNotify` (`rpc/session.go:205`) applies `session_status`, `sheet` and `pending_requests`. `rpc/requests.go` is the model for a mirror read on first use. `rpc` may import `config`, `routing` and `schema` (`rpc/imports_test.go:52`); `acpwire` imports only the standard library, and its fields follow `TestWireFieldsFollowTheRules`.
- **The interface** (`internal/acpclient/acpclient.go`): the host's settings methods run from `TrustPrompt` (`:53`) to `SaveProvenance` (`:180`), apart from `ListSessions`; the session's are `Tools` (`:212`) and `PrepareMCPLogin` (`:700`) to `SaveVerifyCommands` (`:760`).
- **Test models.** `conformance_requests_test.go` is a case table over both arms; `TestASudoPasswordCrossesTheWireOnce` in it wraps the agent's stream and logger; `TestAnswerCallBudget` (`:841`) is a budget over `largeStackSession` (`stack_wire_test.go:413`). The service's `newConfigState` (`config_test.go:24`), `secretConfig` (`secrets_test.go:26`) and `testAuth` (`logins_test.go:29`) are the fixtures the moved cases use.

**Decisions this plan makes beyond the spec** (R2, R3 and R9 record them):

1. **Host calls name the client's current session.** `rpc.Host` remembers the session its last successful `Attach` bound, and every host call carries its ID. The agent resolves that runtime's service host, which acts on that session as it does in process, and the barrier fences that session, so a read after a write sees the refreshed layers. A host call before any attach fails with "rpc: no session attached". This is how plan 7 decides how host calls flush (R2).
2. **Two host reads are fetched on first use and kept.** `ProviderTemplates` (the catalog is built in) and `TrustPrompt` (it does not change while the host runs) send their methods on their first read after an attach, under a 5s timeout, and keep the answer. A failed read returns nothing and asks again next time. `Dial` is unchanged.
3. **Config, layers and tools are one mirror.** `session/config` answers a `SessionConfig`, and for a marshal client starts a forwarder that polls every 50ms and on its fence, sending `session_config` when the encoding changed. `Tools` reads it too, so the method table's `Tools` row moves from `session/tools` to `session/config`, and `session/tools` is dropped. A poll costs about 35µs at the base.
4. **Config values travel as the config package's own JSON.** `acpwire` carries them as `json.RawMessage`, and both ends decode with new `config` helpers that restore a preset's `Pricing` to `*pricing.ModelPricing`. `Layers.HoistError` gains a `json:"-"` tag.
5. **`config/set_value` decodes the value into the field's own type.** A new `config.PathType` returns the Go type of the `Config` field at a dotted path with any pointer removed, through map entries that do not exist yet. The agent decodes the JSON value into that type, so `SaveUserValue` gets a value of the type a working in-process call passes: `assignReflect` refuses a pointer value, so every value the TUI writes today is a field's value or a dereferenced one (`model.go:6932` passes `*msg.EnrichmentConsent`). The client refuses a nil or pointer value before sending, so a call that fails in process still fails, with the client's own message.
6. **The reload flag travels with its error.** `session/reload_config` and `session/mcp_reconnect` answer a `ReloadResult` carrying the flag and the error, coded as an `*acpwire.Error`, because a JSON-RPC error response carries no result.
7. **Sign-ins report their URL as progress and can be cancelled.** `config/login_provider` and `session/mcp_login` send the URL as a `$/progress` value, which the client hands to `show` on its read goroutine, and take their context from `cancellable`, so the client's context ending (Esc) stops the flow (R2). Probes and the preset setup are cancellable too.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 6b's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 6b and 7a`. Plan 6b gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-7a-settings` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-7a-settings: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The config helpers (`internal/app/config/wirejson.go`, new)

```go
// UnmarshalConfigJSON decodes a Config that crossed the client seam as JSON,
// then restores each preset's Pricing to a *pricing.ModelPricing: a JSON
// decode into routing.ModelPreset's `any` field yields a map[string]any.
func UnmarshalConfigJSON(data []byte, cfg *Config) error

// UnmarshalLayersJSON is UnmarshalConfigJSON for each of the layers' three
// configs.
func UnmarshalLayersJSON(data []byte, layers *Layers) error

// UnmarshalPresetsJSON decodes a preset map and restores its Pricing values.
func UnmarshalPresetsJSON(data []byte, presets *map[string]routing.ModelPreset) error

// PathType is the Go type of the value SaveUserConfigValue can write at the
// dotted TOML path: the Config field's type there with any pointer removed.
// It walks struct fields by their toml tag, through pointers, and through a
// string-keyed map's element type, so a path into a map entry that does not
// exist yet still has a type. It reports false for a path that names nothing.
func PathType(dottedPath string) (reflect.Type, bool)
```

`Layers.HoistError` (`layers.go:56`) gains the tag `json:"-"`.

### Wire types (`internal/acpwire/settings.go`, new)

```go
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
// client's current session. config/provider_templates, trust/prompt and
// config/models_forget take only this.
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
```

Results: `config/provider_templates` answers a `[]ProviderTemplate`; `trust/prompt` a `TrustPromptResult`; `config/probe_provider` and `config/models_list` a `ModelsResult`; `config/probe_model` a `CanProbeResult` for a check and a `ModelCapabilities` otherwise; `config/prepare_presets` a `PreparedPresets`; `session/config` a `SessionConfig`; `session/reload_config` and `session/mcp_reconnect` a `ReloadResult`; the others `{}`.

### The routing helper (`internal/acp/host.go`)

Plan 6b adds the same declaration, word for word, in the same place (right after `responseBarrier`). If it merged first, the helper is already there: keep it.

```go
// marshalOr serves one method two ways: forMarshal for a client that declared
// the marshal capability, which reaches the session's service (P3 R1, R9), and
// other for every other client, unchanged. A nil other answers such a client
// with method-not-found.
func marshalOr(marshal func() bool, forMarshal, other Handler) Handler
```

### The settings handlers (`internal/acp/settings.go`, new)

```go
// configInterval is how often a config forwarder compares the session's
// config with the last one it sent (P3 R3).
const configInterval = 50 * time.Millisecond

// SettingsManagerConfig wires one connection's settings handlers.
type SettingsManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// HostLookup resolves the service host of a session's runtime.
	HostLookup func(sessionID string) (*service.Host, bool)
	// Notify sends session_config updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// SettingsManager serves the user's and the session's config, the provider
// and model calls, the sign-ins and the trust prompt for one connection, and
// runs one config forwarder per session a marshal client reads.
type SettingsManager struct{ /* unexported */ }

func NewSettingsManager(cfg SettingsManagerConfig) *SettingsManager

func (m *SettingsManager) ProviderTemplates(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) TrustPrompt(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) TrustDecide(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ProbeProvider(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ProbeModel(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ModelsList(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ModelsSave(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ModelsForget(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ModelsRename(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) LoginProvider(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SetValue(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SaveProviders(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SetProviderKey(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SavePresets(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) PreparePresets(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SaveProvenance(ctx context.Context, params json.RawMessage) (any, error)

func (m *SettingsManager) SessionConfig(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ApplyConfig(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) ReloadConfig(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) MCPReconnect(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SaveProjectConfig(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) SaveVerifyCommands(ctx context.Context, params json.RawMessage) (any, error)
func (m *SettingsManager) MCPLogin(ctx context.Context, params json.RawMessage) (any, error)

// Close stops every config forwarder. The connection's cleanup calls it.
func (m *SettingsManager) Close()

// fenceSession waits until the session's config forwarder has sent the config
// as it is now, if it differs from the last one sent (P3 R2).
func (m *SettingsManager) fenceSession(ctx context.Context, sessionID string)

// sessionConfig is what one pass reads: the session's config, layers and
// tools.
func sessionConfig(svc *service.Session) (acpwire.SessionConfig, error)

// registerSettings registers the new methods on srv, and the two existing ones
// through marshalOr: a client without the capability reaches others[method],
// unchanged.
func registerSettings(srv *Server, settings *SettingsManager, others map[string]Handler)
```

### The session manager (`internal/acp/session.go`)

```go
// ServiceHost returns the service host built with the session's service: the
// host its settings calls act through. It builds both on first use, as
// Service does.
func (m *SessionManager) ServiceHost(id string) (*service.Host, bool)
```

### The client (`internal/acpclient/rpc/settings.go`, new)

```go
// hostReadTimeout bounds a host read's first call, and the config mirror's.
const hostReadTimeout = 5 * time.Second

func (h *Host) ProviderTemplates() []acpwire.ProviderTemplate
func (h *Host) TrustPrompt() string
func (h *Host) DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error
func (h *Host) ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error)
func (h *Host) CanProbeModel(ctx context.Context, name string, pc config.ProviderConfig) bool
func (h *Host) ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error)
func (h *Host) ListModels(ctx context.Context, name string) ([]schema.ModelInfo, bool)
func (h *Host) SaveModels(ctx context.Context, name string, models []schema.ModelInfo) error
func (h *Host) ForgetModels(ctx context.Context, name string) error
func (h *Host) RenameModels(ctx context.Context, from, to string) error
func (h *Host) PrepareProviderLogin(ctx context.Context, name string) error
func (h *Host) LoginProvider(ctx context.Context, name string, show func(url string)) error
func (h *Host) SaveUserValue(ctx context.Context, tomlPath string, value any) error
func (h *Host) SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig, renamed map[string]string) error
func (h *Host) SaveUserPresets(ctx context.Context, presets, baseline map[string]routing.ModelPreset) error
func (h *Host) SaveProviderKey(ctx context.Context, name, key string) error
func (h *Host) PreparePresets(ctx context.Context, cfg config.Config, provider, baseURL string, models []schema.ModelInfo) (map[string]routing.ModelPreset, acpwire.PresetSetup, error)
func (h *Host) SaveProvenance(ctx context.Context, provenance json.RawMessage) error

func (s *Session) Tools() ([]string, bool)
func (s *Session) Config() config.Config
func (s *Session) Layers() config.Layers
func (s *Session) ApplyConfig(ctx context.Context, cfg config.Config) error
func (s *Session) ReloadConfig(ctx context.Context, cfg config.Config) (bool, error)
func (s *Session) ReconnectMCP(ctx context.Context) (bool, error)
func (s *Session) SaveProjectConfig(ctx context.Context, cfg config.Config) error
func (s *Session) SaveVerifyCommands(ctx context.Context, build, test string) error
func (s *Session) PrepareMCPLogin(ctx context.Context, server string) error
func (s *Session) LoginMCP(ctx context.Context, server string, show func(url string)) error
```

### The harness (`internal/acp/conformance_test.go`, `conformance_settings_test.go` new)

```go
// hostSeam is the part of acpclient.Host that both arms implement: the
// eighteen methods above, with the interface's signatures.
type hostSeam interface{ /* the eighteen methods */ }

// seam gains the ten session methods above.

// openPipeArmsTB is openPipeSeamTB's body, returning the client's host as
// well as its session. openPipeSeamTB returns its session.
func openPipeArmsTB(tb testing.TB, d service.Deps) (hostSeam, seam)

// serveSettings registers the settings handlers on the pipe arm's server over
// the served host and service, and returns the manager the barrier fences.
func serveSettings(tb testing.TB, srv *Server, servedHost *service.Host, served *service.Session, sessionID string) *SettingsManager

func TestSettingsConformance(t *testing.T)
func TestASecretCrossesTheWireOnlyInItsCall(t *testing.T)
func TestSettingsCallBudget(t *testing.T)
```

## Task 1: The config helpers and the wire types

**Goal:** a config value crosses JSON unchanged, `config/set_value` can find a field's type, and `acpwire` carries the group's params, results and update.

**Files:** `internal/app/config/wirejson.go` (new), `internal/app/config/wirejson_test.go` (new), `internal/app/config/layers.go`, `internal/acpwire/settings.go` (new), `internal/acpwire/settings_test.go` (new).

**Steps:**

1. **`HoistError`.** Add the tag `json:"-"` to `Layers.HoistError`, with a comment: it is an `error`, which JSON cannot decode, and nothing outside this package reads it.
2. **The decode helpers** in `wirejson.go`, as in Contracts. Each runs `json.Unmarshal`, then restores every preset whose `Pricing` decoded as a `map[string]any`: re-encode that map as JSON and decode it into a `pricing.ModelPricing`, and store the pointer. `UnmarshalConfigJSON` does this for `cfg.Models.Presets`; `UnmarshalLayersJSON` for `Default`, `User` and `Merged`; `UnmarshalPresetsJSON` for the map itself. A `Pricing` that is neither a pointer nor such a map is left as it is, as `coercePresetPricing` leaves it.
3. **`PathType`** walks `reflect.TypeOf(Config{})` as `LookupPath` walks a value: through pointer types, into a struct field whose `toml` tag names the segment (as `fieldByTOMLTag` matches it), and into a string-keyed map's element type for any segment. The answer is the type at the last segment with its pointers removed, as `assignReflect` removes them from its destination.
4. **The wire types**: the constants and types in Contracts' `acpwire/settings.go`.
5. **Tests** in `wirejson_test.go`:
   - `TestTheDefaultConfigRoundTripsThroughJSON`: `config.Default()` encoded with `json.Marshal` and decoded with `UnmarshalConfigJSON` is `reflect.DeepEqual` to the original.
   - `TestLoadedLayersRoundTripThroughJSON`: layers loaded (with `LoadLayers`, a temporary home and working directory) from a user config with two providers (one with a key), a preset with a `[models.presets.x.pricing]` table, an MCP server and a search key round-trip with `UnmarshalLayersJSON`, and the preset's `Pricing` comes back as a `*pricing.ModelPricing` equal to the original.
   - `TestAHoistErrorIsNotEncoded`: layers with `HoistError` set encode, decode with `UnmarshalLayersJSON` without error, and come back with `HoistError` nil.
   - `TestPresetsRoundTripWithTheirPricing`: a preset map with one priced and one unpriced preset round-trips with `UnmarshalPresetsJSON`.
   - `TestPathTypeNamesTheFieldsType`: `providers.newname.api_key` (a map entry that does not exist) is `string`; `privacy.models_dev_enrichment`, a `*bool` field, is `bool`; `budgets.daily_usd` is `float64`; `agent.mode` is `interaction.Mode`, not `string`; an unknown path reports false.

   And in `settings_test.go`:
   - `TestSessionConfigUpdateRoundTrips`: an update whose `Config` and `Layers` carry raw JSON and two tools round-trips, and its `kind` is `session_config`.
   - `TestAReloadResultCarriesItsError`: `ReloadResult{Reloaded: true, Error: &Error{Code: CodeKeyringUnavailable, Message: "m"}}` round-trips.

   `TestWireFieldsFollowTheRules` must keep passing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-7a-suite
go build ./...
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/app/config/ ./internal/acpwire/ -count=1
```

Expected: both packages pass, `acpwire`'s rules test included; the config package's existing tests pass with the new tag.

Mutation check: make `UnmarshalConfigJSON` skip the pricing restore. `TestLoadedLayersRoundTripThroughJSON` fails. Restore it.

## Task 2: The host calls

**Goal:** the agent serves the user config writes, the provider and model calls, the provider sign-in and the trust prompt over the named session's service host.

**Files:** `internal/acp/settings.go` (new), `internal/acp/settings_test.go` (new), `internal/acp/session.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **`ServiceHost`.** In `session.go`, `services` holds a small unexported struct of the host and the session `service.New` returned, instead of the session alone. `Service` returns its session and `ServiceHost` its host, both through one shared lookup that builds the pair on first use under the same rules `Service` has today (refusing a closing runtime). `releaseService` releases the pair's session, as today.
2. **`marshalOr`** in `host.go`, right after `responseBarrier`, as in Contracts: when `marshal` is non-nil and reports true it calls `forMarshal`; otherwise it calls `other`, and when `other` is nil it returns `&jsonRPCError{Code: methodNotFound, Message: "method not found"}`. Plan 6b writes the same helper; keep its doc comment exactly as Contracts gives it.
3. **The manager.** `SettingsManager` holds its config, a mutex, one config forwarder per session (Task 3 fills them in; this task declares the map and `Close`), and a `closed` flag, as `RequestManager` does. Its `host(sessionID)` helper refuses an empty ID with `invalidParamsError("sessionId is required")` and an unknown one with `serverErrorf("unknown session: %s", id)`, as `RequestManager.service` (`requests.go:228`) does for a session.
4. **The host handlers.** Each decodes its params type from Contracts (a decode error, a raw config value included, is `invalidParamsError`), resolves the host, and returns the host's answer or its error unchanged. Config values are decoded with Task 1's helpers (`UnmarshalConfigJSON`, `UnmarshalPresetsJSON`) or, for a provider map, a provider config or a model list, with `json.Unmarshal`.
   - `ProviderTemplates` and `TrustPrompt` take `HostCallParams`; `TrustPrompt` answers `TrustPromptResult{Dir}`.
   - `TrustDecide` calls `DecideTrust(ctx, Dir, Decision)`.
   - `ProbeProvider` calls `ProbeProvider` under `cancellable(ctx)` and answers `ModelsResult{Models}`.
   - `ProbeModel`: with `Check`, answers `CanProbeResult` from `CanProbeModel`; otherwise calls `ProbeModel` under `cancellable(ctx)` and answers the `ModelCapabilities`.
   - `ModelsList` answers `ModelsResult{Models, Found}` from `ListModels`; `ModelsSave`, `ModelsForget` and `ModelsRename` call their host methods.
   - `LoginProvider`: with `Prepare`, calls `PrepareProviderLogin`; otherwise calls `LoginProvider` under `cancellable(ctx)` with a `show` that sends `reportProgress(ctx, acpwire.LoginProgress{URL: url})`, the request's own context, so the value precedes the response.
   - `SetValue`: find the field's type with `config.PathType(Path)` (an unknown path is invalid params), decode `Value` into a new value of that type, and call `SaveUserValue(ctx, Path, value)` with it.
   - `SaveProviders` decodes both maps (a missing `baseline` is a nil map) and calls `SaveUserProviders` with `Renamed`.
   - `SetProviderKey` decodes `ProviderKeyParams` and calls `SaveProviderKey`.
   - `SavePresets` and `PreparePresets` decode their presets and config with the helpers, and `PreparePresets` runs under `cancellable(ctx)` and answers `PreparedPresets{Presets, Setup}`.
   - `SaveProvenance` calls `SaveProvenance` with the raw provenance.

   None of them logs its params, an error that quotes them, or a result: a probe's provider config and the key write carry secrets (R8).
5. **`registerSettings`** as in Contracts. This task registers the fourteen new host methods with `srv.Handle`, and `config/probe_provider` and `config/set_provider_key` through `marshalOr(srv.marshalClient, settings.X, others[method])`. Task 3 adds the session methods.
6. **Host wiring.** In `registerHandlers`, build `settings := NewSettingsManager(SettingsManagerConfig{Lookup: manager.Service, HostLookup: manager.ServiceHost, Notify: h.sink.Notify, Marshal: srv.marshalClient})` right after the request manager is built (`host.go:420` to `:424`), so it exists before the barrier line that Task 3 appends it to. Delete the lines registering `config/set_provider_key` (`:433`) and `config/probe_provider` (`:437`), and after the remaining `config/*` lines call `registerSettings(srv, settings, map[string]Handler{acpwire.MethodSetProviderKey: cfgMgr.SetProviderKey, acpwire.MethodProbeProvider: cfgMgr.ProbeProvider})`. Call `settings.Close()` in the cleanup, after `requests.Close()`. The barrier changes in Task 3.
7. **The method table.** In `methods.go`, mark every host row from `TrustPrompt` to `SaveProvenance` existing (`ProbeProvider` and `SaveProviderKey` already are), and rewrite their `ACP:` paragraphs in `acpclient.go` as "ACP: config/probe_model (exists)." and so on. `ProbeProvider`'s and `SaveProviderKey`'s gain: "A marshal client's call is served by the session's service." `ProviderTemplates`' and `TrustPrompt`'s gain: "Read once, after the first attach, and kept." Every host call's gains nothing more: the client names its current session (decision 1), which the `Host` doc says once: add to `Host`'s own doc, "Over ACP every host call but SharedFilesystem names the client's current session, whose runtime serves it."
8. **Tests** in `settings_test.go`, serving a `SettingsManager` and a `StatusManager` on a pipe with `startTestAgent`, `registerInitialize`, `registerStatus`, `registerSettings` (with stub `others`) and `responseBarrier(statuses)`, to a raw `rpc.Conn` that declares the capability (or not). The host and session are `service.New` over a state built as the service's `newConfigState` builds it, with `HomeDir` and `UserConfigPath` in a temporary directory and a `ReloadLayers` that reloads them.
   - `TestHostCallsResolveTheNamedSessionsHost`: `config/set_value` for `s1` writes the value into `s1`'s user config file and refreshes `s1`'s layers; a missing `sessionId` is invalid params; an unknown one is the unknown-session error.
   - `TestSetValueKeepsTheFieldsType`: a bool, a string, an integer field, a float field holding a whole number (`budgets.daily_usd` = 5) and a named-type field (`agent.mode` = `act`) each reach the file as the field's own type; an unknown path is invalid params.
   - `TestProbeProviderForAMarshalClientUsesTheService` and `TestProbeProviderWithoutTheCapabilityReachesItsOtherHandler`.
   - `TestSetProviderKeyForAMarshalClientRejectsAMask`: a masked key is refused with the service's mask error, and the file is unchanged; without the capability the stub `others` handler is reached.
   - `TestLoginProviderReportsTheURLAsProgress`: with the service's `testAuth` fixture copied in, a `config/login_provider` call over a `CallWithProgress` receives one progress value carrying the authorization URL before its response.
   - `TestCancellingASignInEndsTheFlow`: cancel the client's context while the sign-in waits for its callback; the engine's context ends within 2s.
   - `TestTrustCallsReachTheHost`: with `TrustPrompt` and `DecideTrust` in the `Deps`, `trust/prompt` answers the directory and `trust/decide` passes the decision; a directory that is not pending is the service's error.
   - `TestTheHostCallsRefuseBadParams`: a missing `sessionId` is invalid params for each method.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... ./internal/app/config/
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'HostCalls|SetValue|ProbeProvider|SetProviderKey|LoginProvider|SignIn|Trust|ACPClientMethodTable|Config' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'LoginProvider|SignIn|HostCalls' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing `config/*` test (`config_test.go`) passes unchanged, none of them declaring the capability; the method-table tests pass.

Mutation checks:
- Make `SetValue` decode `Value` into an `any`. `TestSetValueKeepsTheFieldsType` fails. Restore it.
- Make `marshalOr` always call `other`. `TestProbeProviderForAMarshalClientUsesTheService` fails. Restore it.

## Task 3: The session's config and the config forwarder

**Goal:** the agent serves the session's config reads and writes and the MCP sign-in, and a forwarder keeps a marshal client's config mirror current through the barrier.

**Files:** `internal/acp/settings.go`, `internal/acp/settings_test.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **`sessionConfig`.** Encode the service's `Config()` and `Layers()` with `json.Marshal` into the `SessionConfig`'s two raw fields, and copy `Tools()`'s names and flag. An encoding error is returned; it cannot happen once Task 1's tag is in.
2. **The session handlers**, each resolving the session's service with `Lookup` as the host handlers resolve the host:
   - `SessionConfig`: decode `ConfigParams`; when `Marshal` reports true, make sure the session has a config forwarder (step 3); return `sessionConfig(svc)`.
   - `ApplyConfig` and `SaveProjectConfig`: decode `ConfigParams.Config` with `UnmarshalConfigJSON` and call the service's method.
   - `ReloadConfig`: decode it the same way and call `ReloadConfig`; `MCPReconnect` calls `ReconnectMCP`. Both answer `ReloadResult{Reloaded, Error}`, where `Error` is the service's error as an `*acpwire.Error` when it is one, and otherwise `&acpwire.Error{Message: err.Error()}`; neither returns a JSON-RPC error for the service's own failure (decision 6).
   - `SaveVerifyCommands` calls `SaveVerifyCommands(ctx, Build, Test)`.
   - `MCPLogin`: with `Prepare`, calls `PrepareMCPLogin`; otherwise calls `LoginMCP` under `cancellable(ctx)`, reporting the URL as `LoginProvider` does.
3. **The forwarder.** One per session per connection, as `RequestManager.ensureForwarder` and `runForwarder` (`requests.go:250`, `:275`) build and run one: its fence is registered before its goroutine starts, it wakes on a `configInterval` ticker and on its fence, it resolves the service through `Lookup` on each pass (answering a pending fence and stopping when the session is gone), and it sends `session/update` with the session's ID and `acpwire.SessionConfigUpdate{Kind: acpwire.UpdateSessionConfig, Config: …}` only when the encoding differs from the last one it sent. It never sends under the manager's mutex. `Close` cancels every forwarder and makes later starts do nothing. `fenceSession` waits on the forwarder's fence.
4. **Registration and the barrier.** `registerSettings` adds the seven session methods with `srv.Handle`. In `registerHandlers`, append `settings` to the `responseBarrier` call (`host.go:427`), after `requests`.
5. **The method table.** Mark `Config`, `Layers`, `ApplyConfig`, `ReloadConfig`, `ReconnectMCP`, `SaveProjectConfig`, `SaveVerifyCommands`, `PrepareMCPLogin` and `LoginMCP` existing; change `Tools`' row to `session/config` and mark it existing. Rewrite their `ACP:` paragraphs: `Config`, `Layers` and `Tools` say "ACP: session/config (exists). A session_config update refreshes it; the first read subscribes."; `ReloadConfig` and `ReconnectMCP` add "The result carries the flag with the error."; the others say "ACP: session/apply_config (exists)." and so on.
6. **Tests** in `settings_test.go`, with `responseBarrier(statuses, settings)`:
   - `TestTheFirstSessionConfigArrivesBeforeTheResponse`: the first marshal `session/config` returns after exactly one `session_config` update for `s1` was recorded, and its value equals the result.
   - `TestAConfigChangeIsSentWithinTheInterval`: change the state's layers directly; an update carrying the change arrives within 500ms. Three quiet intervals then send nothing.
   - `TestAHostWriteReturnsAfterItsConfigUpdate` (V3): with every `session/update` frame slowed by 50ms, a `config/set_value` for `s1` returns only after a `session_config` update whose layers carry the new value was recorded.
   - `TestApplyConfigInstallsTheDecodedConfig`: a config with a priced preset, applied, is the state's config, its `Pricing` a `*pricing.ModelPricing`.
   - `TestTheReloadFlagTravelsWithItsError`: a `ReloadConfig` dep that fails answers `reloaded` true with the error's message; without a reload path it answers `reloaded` false and no error.
   - `TestAnMCPLoginReportsTheURLAsProgress`: against the service's MCP test server, copied in, the progress value precedes the response.
   - `TestAClientWithoutTheCapabilityGetsNoConfigUpdates` (V6): `session/config` answers, and no `session_config` update is recorded within 200ms.
   - `TestTheSessionCallsRefuseBadParams`.
   - `TestTheConfigForwarderStopsWhenTheSessionGoes`: make `Lookup` report false; within 500ms the forwarder has stopped and its fence is gone.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'SessionConfig|ConfigChange|HostWrite|ApplyConfig|Reload|MCPLogin|ConfigUpdates|SessionCalls|ConfigForwarder|Barrier|Status' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'SessionConfig|ConfigChange|HostWrite' -race -count=3
```

Expected: the new tests pass, three times under `-race`; plan 3a's and 3b's ordering tests pass with the new fence.

Mutation check: drop `settings` from the test server's `responseBarrier`. `TestAHostWriteReturnsAfterItsConfigUpdate` and `TestTheFirstSessionConfigArrivesBeforeTheResponse` fail. Restore it.

## Task 4: The client's host calls

**Goal:** `rpc.Host` names its current session and sends the eighteen host calls.

**Files:** `internal/acpclient/rpc/settings.go` (new), `internal/acpclient/rpc/dial.go`, `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/settings_test.go` (new).

**Steps:**

1. **The current session.** `Host` gains the ID of its current session, guarded by its mutex. `Attach` sets it when its call succeeds (also when it returns an already-attached session). A private `currentID()` returns it, or the error `rpc: no session attached` when there is none.
2. **The host reads.** `ProviderTemplates` and `TrustPrompt` keep their first successful answer. Until they have one, each read sends its method with the current session under a `hostReadTimeout` context; on success it keeps the answer, on failure (or with no session attached) it returns nil or `""` and asks again on the next read.
3. **The host calls.** Each sends its method with the current session's ID and returns the result or the error, as `SetNotice` does. Config values go into their raw fields with `json.Marshal`, and come back through Task 1's helpers (`UnmarshalPresetsJSON` for `PreparePresets`' presets) or `json.Unmarshal` (models, a model list's `Found`).
   - `CanProbeModel` sends `config/probe_model` with `Check` and returns `CanProbe`, or false on any error.
   - `ListModels` returns the models and `Found`, or nil and false on any error.
   - `PrepareProviderLogin` sends `config/login_provider` with `Prepare`. `LoginProvider` sends it without, with `CallWithProgress`, handing each `LoginProgress` value's URL to `show`; it is called on the connection's read goroutine, so `show` must not block.
   - `SaveUserValue` sends the value's JSON encoding as `Value`. A nil value, or one whose dynamic type is a pointer, returns the error `rpc: config/set_value cannot write a <type> value` (with `%T`) without sending: `SaveUserConfigValue` refuses both in process (decision 5).
   - `LoginProvider`'s doc says `show` runs on the connection's read goroutine and must return promptly. The TUI's two callbacks (`connect.go:438`, `mcp.go:101`) start the browser with `exec.Command(…).Start()` and update a panel, so they do.
4. **Tests** in `settings_test.go`, over the scripted agent of `session_test.go`:
   - `TestAHostCallBeforeAnAttachFails`: every host call before `Attach` returns the no-session error without sending; `ProviderTemplates` and `TrustPrompt` return empty.
   - `TestHostCallsNameTheCurrentSession`: after `Attach("s1")`, each call's frame carries `"sessionId":"s1"` and its params; results decode.
   - `TestTheHostReadsAreFetchedOnce`: the first `ProviderTemplates` and `TrustPrompt` send their methods; the second send nothing; a failed first read asks again.
   - `TestLoginProviderHandsTheURLToShow`: a progress value before the response reaches `show` once.
   - `TestPreparePresetsRestoresThePricing`: a scripted result with a priced preset comes back with a `*pricing.ModelPricing`.
   - `TestTheHostCallsReturnCodedErrors`: a coded error comes back with `acpclient.IsCode` true.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included (`rpc` imports `config`, `routing` and `schema`, which it may), three times under `-race`.

Mutation check: make `Attach` leave the current session unset. `TestHostCallsNameTheCurrentSession` fails. Restore it.

## Task 5: The client's session config

**Goal:** `rpc.Session` mirrors the session's config, layers and tools, and sends the ten session calls.

**Files:** `internal/acpclient/rpc/settings.go`, `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/settings_test.go`.

**Steps:**

1. **The mirror.** The session holds one `SessionConfig` entry, absent until the first read, with an updated mark, as the request mirror does, plus the config decoded once per value with `UnmarshalConfigJSON`.
   - `Config()`: an entry returns `config.Clone` of its decoded config, so a caller that changes what it got cannot change the next read. Otherwise create the entry, send `session/config` under a `hostReadTimeout` context, and install the result unless an update arrived during the call, as `pendingEntry` does. On error remove the entry and return a zero config; the next read asks again.
   - `Layers()`: read the entry as `Config` does, and decode its raw layers with `UnmarshalLayersJSON` on each read.
   - `Tools()`: read the entry; return a copy of its names and its flag.
2. **Routing.** `routeNotify` gains a case for `acpwire.UpdateSessionConfig`: decode `SessionConfigUpdate` and hand its value to the attached session it names. The session replaces its entry, decodes the config, marks it updated, and taps `Changed`. An update for a session that has not read its config is dropped.
3. **The calls.** `ApplyConfig`, `SaveProjectConfig` and `ReloadConfig` send the config's JSON as `ConfigParams.Config`. `ReloadConfig` and `ReconnectMCP` return the result's `Reloaded` and, when set, its `Error` as the error. `SaveVerifyCommands` sends its two commands. `PrepareMCPLogin` and `LoginMCP` send `session/mcp_login`, as `PrepareProviderLogin` and `LoginProvider` send theirs.
4. **Tests**:
   - `TestConfigFetchesOnItsFirstRead`, `TestConfigInstallsTheUpdateThatPrecedesTheResponse`, `TestASessionConfigUpdateReplacesTheMirrorAndFiresChanged`: the rules plan 5's tests pin for the request mirror, for the config.
   - `TestConfigReturnsACopy`: changing a provider in the returned config's map leaves the next read unchanged.
   - `TestLayersAndToolsReadTheMirror`: one update serves `Layers` and `Tools` with no call.
   - `TestTheReloadCallsReturnTheFlagWithTheError`: `ReloadResult{Reloaded: true, Error: …}` returns true and an error with that message; `{Reloaded: false}` returns false and nil.
   - `TestTheSessionConfigCallsSendTheirMethods`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, three times under `-race`; plan 3b's, 4b's and 5's client tests pass unchanged.

Mutation check: make the router skip `session_config`. `TestASessionConfigUpdateReplacesTheMirrorAndFiresChanged` fails. Restore it.

## Task 6: The conformance harness, the secrets and the budget

**Goal:** the group's cases run against the service and against the client over a pipe, host and session alike, and agree (V1); a secret crosses the wire in its own call only (V5); a settings call stays inside R10's budget.

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_settings_test.go` (new), `internal/acp/service/config_test.go`, `internal/acp/service/providers_test.go`, `internal/acp/service/logins_test.go`, `internal/acp/service/trust_test.go`, `internal/acp/service/secrets_test.go`.

**Steps:**

1. **The seams.** Add `hostSeam` and the ten session methods to `seam`, as in Contracts.
2. **The arms.** Rename `openPipeSeamTB`'s body into `openPipeArmsTB`: it keeps the host `service.New` returns as `servedHost`, calls `serveSettings(tb, srv, servedHost, served, sessionID)` after `serveRequests`, adds the settings manager to its `responseBarrier` call after `requests`, and returns the client's `rpc.Host` with its session. `openPipeSeamTB` calls it and returns the session. `serveSettings` builds a `SettingsManager` whose `Lookup` and `HostLookup` return the served service and host for the served ID, with `Notify: srv.Notify` and `Marshal: srv.marshalClient`, calls `registerSettings` with a nil `others`, and closes the manager with the test. Add a `hostArms()` beside `seamArms()`: the service arm returns the host and session `service.New(d)` builds; the pipe arm is `openPipeArmsTB`.
3. **The table.** `TestSettingsConformance` runs every case once per arm, as `TestRequestConformance` does. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`newConfigState`, `secretConfig`, `testAuth` and its constructor, and the MCP test server's setup) into the new file:
   - config: `TestConfigReadsTheSessionsConfigAndLayers`, `TestSaveProjectConfigRefreshesTrustThenLayers`, `TestSaveVerifyCommandsRefreshesOnlyLayers`, `TestUserWritesUseTheUserConfigPathAndRefreshLayers`, `TestAFailedLayerReloadKeepsTheLayers`, `TestReloadConfigReportsTheReloadPath`, `TestApplyConfigInstallsWithoutSaving`, `TestReconnectMCPClearsNoticesBeforeTheReload`, `TestPreparePresetsRunsPresetSetup`, `TestSaveProvenanceWritesTheSidecar`;
   - providers: `TestProviderTemplatesListTheCatalog`, `TestProbeProviderListsModels`, `TestProbeProviderReportsFailures`, `TestProbeProviderPreservesModelLimits`, `TestProbeProviderUsesTheAdaptersResolver`, `TestCanProbeModel`, `TestProbeModelProbesOllama`, `TestModelCacheRoundTrip`, `TestModelCacheIgnoresWhatPersistIgnored`;
   - sign-ins: `TestPrepareProviderLoginReportsTheResolver`, `TestLoginProviderRunsTheFlow`, `TestLoginProviderCodesAnUnavailableKeychain`, `TestLoginMCPComposesTheServerAndTheStore`, `TestPrepareMCPLoginSentences`;
   - trust and tools: `TestDecideTrustPassesTheDecision`, `TestDecideTrustRefusesWhatIsNotPending`, `TestTrustPromptIsTheDeps`, `TestToolsReadsTheRuntime`;
   - secrets: `TestConfigReadsCarryNoSecret`, `TestInstallsRestoreTheStoredKey`, `TestSaveProjectConfigRestoresTheSearchKey`, `TestSaveUserProvidersRestoresKeys`, `TestSaveUserValueRestoresAMaskedKey`, `TestSaveUserValueRejectsAnUnmatchedMask`, `TestSaveProviderKeyRejectsAMask`, `TestAnUnmatchedMaskChangesNothing`, `TestProbesUseTheStoredKey`.

   These stay in the service's package: `TestStatusNamesTheConfigFiles` (a status case, plan 3b's group), `TestClosedSessionConfigCallsFail` and `TestAClosedSessionStartsNoMCPLogin` (they drive the in-process host's `swap`), `TestAuthResolverDefaultsToProduction` (it reads the in-process host's own resolver) and `TestMCPEngineRunsAgainstATestServer` (it tests the engine, not the seam).

   Each case builds its `Deps` and opens the arms before acting, as plan 5's cases do. Two kinds of assertion change on both arms, and nothing else: where a case asserts `errors.Is` against a plain error (`config_test.go:329`, `:380`, `providers_test.go:147`, `logins_test.go:91`), it compares the error's message instead, because over the wire only the message of an error that is not an `*acpwire.Error` survives (plan 1), and the TUI reads only the message; an `*acpwire.Error` keeps its code, so `errors.As` on one stays. Where a case reads the model cache's files or the user config file directly, both arms share the directory, so the read stays as it is.
4. **A difference is a bug, never the case's,** as in plan 5: fix the wire type, the handler, the helpers or the client so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The secrets (V5).** `TestASecretCrossesTheWireOnlyInItsCall`: open the pipe arms with the agent's stream wrapped so every byte it reads and writes is copied into a `syncBuffer`, and the server's logger writing into another, copying what `openPipeArmsTB` does rather than changing it. Read `Config()` once (subscribing the mirror), then call `SaveProviderKey` with the key `sk-p3-7a-canary-5d1e`, and `ProbeProvider` with a provider config carrying the typed key `sk-p3-7a-probe-91c4` against an `httptest` endpoint. Then each key appears exactly once in the bytes the agent read (inside its own call's params), and neither appears in the bytes the agent wrote (no `session_config` update, status or response carries it: they carry its mask) or in the log. A `session_config` update written after the save carries the first key's mask.
6. **The budget (R10).** `TestSettingsCallBudget`: the pipe arms over `largeStackSession`, with a home directory in a temporary directory.
   - 30 `ApplyConfig` calls with the session's own config: each call includes its barrier and every fence; the median must be under 1ms.
   - 30 `SaveUserValue` calls alternating a bool field on each arm: the pipe arm's median, less the service arm's, must be under 1ms (the write itself is the service's cost on both arms).

   Log both with `t.Logf` for the Implementation record. Skip under `-short` and with the race detector, as `TestAnswerCallBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'TestSettingsConformance|TestASecretCrossesTheWireOnlyInItsCall|TestSettingsCallBudget|TestRequestConformance|TestStatusConformance|TestTranscriptConformance' -count=1 -v 2>&1 | tail -80
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-7a-suite go test ./internal/acp/ -run 'TestSettingsConformance|TestASecretCrossesTheWireOnlyInItsCall' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; the secrets test passes; the budget test passes and logs both numbers; plans 3b's, 4b's and 5's cases still pass on the renamed arm; the service package passes without the moved tests.

Mutation check: make the agent's `SessionConfig` encode the state's `ConfigSnapshot()` instead of the service's masked `Config()`. `TestASecretCrossesTheWireOnlyInItsCall` and the moved `TestConfigReadsCarryNoSecret` fail on the pipe arm. Restore it.

## Task 7: The docs and full verification

**Goal:** the spec records what plan 7a built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 5 did. Expect, if plan 6b has not merged: 111 rows, 77 of them an existing ACP method (26 more: the sixteen host rows from `TrustPrompt` to `SaveProvenance` other than `ProbeProvider` and `SaveProviderKey`, and `Tools`, `Config`, `Layers`, `ApplyConfig`, `ReloadConfig`, `ReconnectMCP`, `SaveProjectConfig`, `SaveVerifyCommands`, `PrepareMCPLogin` and `LoginMCP`), 32 new in P3 carried by 32 distinct new methods, and 2 carrying none; and 106 registered methods (85 plus the 21). If plan 6b merged first, measure the merged tree and record what it shows. Update the section's numbers and its "What the agent pushes today" list: a marshal client also receives `session_config`.
2. **The Implementation record.** Add a "Plan 7a — config, providers and trust" entry after plan 4b's (after plan 6b's, if it merged first). It names: the current session every host call names, `ServiceHost` and the pair `services` holds; the two host reads kept after their first answer; the settings manager and its methods; `marshalOr` for `config/probe_provider` and `config/set_provider_key`; the config mirror, `configInterval`, `session_config` and `Tools` folded into it; the config package's JSON helpers, the pricing restore, `PathType` and `HoistError`'s tag; the reload result; the sign-ins' progress and cancellation; the barrier's new fence; the harness's host arm and its cases; the secrets test; and both budget numbers.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "settings" in its list, after "requests" (after "controls", if plan 6b merged first).
4. **Main may have moved.** If plan 6b merged while this branch ran, merge `main` into the branch before the full verification, and resolve the conflicts as "Integration with plan 6b" says.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-7a-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-7a-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, and now `TestSettingsCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 6b

Plan 6b adds the control manager. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`marshalOr`'s declaration** is the same in both plans, in the same place, so it merges cleanly. If it does not, keep one copy.
- **`host.go`'s `registerHandlers`:** plan 6b builds its manager after `turns`, moves seven `Handle` lines into its `others` map after the `session/work_*` lines, appends `controls` to the barrier and closes it in the cleanup; this plan builds its manager after the request manager, moves the two `config/*` lines into its own `others` map after the remaining `config/*` lines, appends `settings` to the barrier and closes it. The barrier's order is `turns, works, watches, statuses, sheets, requests, controls, settings`.
- **`conformance_test.go`:** both add methods to `seam`; plan 6b adds a `serveControls` line to the pipe arm and its manager to the arm's `responseBarrier`, which after this plan live in `openPipeArmsTB`.
- **`rpc/session.go`:** both add a case to `routeNotify` for their update kind; this plan also sets the current session in `Attach`.
- **`acpclient/methods.go` and `acpclient.go`:** different rows and different methods' docs; this plan also adds one sentence to `Host`'s own doc.
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts.

## Self-review

- **Spec coverage.** R2's host-call flush: decision 1, `TestAHostWriteReturnsAfterItsConfigUpdate`. R2's cancellation and progress: `TestCancellingASignInEndsTheFlow`, `TestLoginProviderReportsTheURLAsProgress`, `TestAnMCPLoginReportsTheURLAsProgress`. R3's `Config` row and host reads: Tasks 3 to 5. R8 and V5: `TestASecretCrossesTheWireOnlyInItsCall`. R9 and V6: `TestProbeProviderWithoutTheCapabilityReachesItsOtherHandler`, `TestSetProviderKeyForAMarshalClientRejectsAMask`'s second half, `TestAClientWithoutTheCapabilityGetsNoConfigUpdates`. R10: `TestSettingsCallBudget`. V1: Task 6. V8: Task 7.
- **What could go wrong.**
  - **A config value changes on the way.** The two fields that do not survive JSON are fixed in Task 1 and pinned by its round-trip tests; `SaveUserValue`'s value keeps its field's type through `PathType`.
  - **A host call with no session.** The client refuses it before sending; in embedded mode `launch` attaches before the TUI runs (plan 8).
  - **A secret in an update.** The mirror encodes the service's masked reads, and the secrets test reads every byte the agent writes.
  - **A `show` that blocks** would stall the connection's read goroutine (R2 calls it there). The TUI's callbacks start the browser without waiting for it and update a panel; Task 4 writes the rule into the method's doc.
  - **Plan 6b.** The shared files and their resolutions are listed above.
