# P2 plan 7a: Providers and sign-ins — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `301e19c8` (plan 6 merged). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding the declared types. The `acpwire` declarations pass T2 (`TestWireFieldsFollowTheRules`) in a scratch tree at the base.
- `inproc` importing `commands`, `credentials`, `llm/provider`, `llm/provider/modelcache` and `oauth`, and `settings`, `connect`, `presetflow` and `probe` importing `acpclient`, were compiled at the base: no import cycle. `TestClientImportsStayOnTheWireSide`, `TestAdapterDoesNotImportApp` and `TestTUIStaysOnTheClientSeam` still pass. `llm/schema` imports no `marshal/internal` package, so the thinking helpers can move into it.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2 (calls and progress), C3 (secrets), C10 (config, providers and trust), Migration order item 7a, its revision record entry "2026-11-05, against `301e19c8` (plan 6 merged)", and tests T1, T2, T4 and T5.
Previous plan: [plan 6, notes, notices and commands](2026-11-04-p2-6-notes-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI builds no provider client and opens no credential store. Model probes, the model cache, the provider catalog and both OAuth sign-ins go through the client seam.
- `acpwire` gains `ProviderTemplate` and `ModelCapabilities`.
- `acpclient.Host` gains `ProviderTemplates`, `ProbeProvider`, `CanProbeModel`, `ProbeModel`, `ListModels`, `SaveModels`, `ForgetModels`, `RenameModels`, `PrepareProviderLogin` and `LoginProvider`. `acpclient.Session` gains `PrepareMCPLogin` and `LoginMCP`.
- `inproc` gains `Deps.Auth` and `Deps.ModelCacheDir`, and builds every provider client the TUI used to build.
- The thinking-option helpers move to `llm/schema`, and `UniqueName` moves to `strutil`.
- `WithAuthResolver`, the connect wizard's `Opts.Auth`, the settings browser's `WithAuthResolver` and `WithDataDir`, and the panels' `AuthResolver` accessors are deleted. So is the model's unused `WithDataDir`.

**Non-goals:**
- **No config reads or writes move.** `m.state.Config`, every `config.Save*`, `persistAndReload`, `applyConnectDone` and the reload calls stay until plan 7b. That includes the reload that reconnects an MCP server after its sign-in (`reconnectMCPServer`).
- **No preset setup.** The connect wizard's `prepareCmd` keeps loading the models.dev catalog and the provenance file and running `presetsetup.Apply` until plan 7b. Only its capability probe moves. `WithModelCache` stays for that and for the provenance save, its last two readers.
- **No trust, skill-gate settings or tool lookup.** Plan 7c.
- **No change to what users see.** Every probe, picker entry, error and note reads as it does today (decision 12 lists the only timing differences).
- **No ACP behaviour change.** `config/probe_provider` and ACP's `/mcp auth` are untouched. No web change, no changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-7a-suite`. Create the directory first: `mkdir -p /private/tmp/p2-7a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** At `301e19c8`, all 117 packages passed, `web/bridge` included. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **The TUI builds provider clients in six places.** Model lists go through `probe.ProviderWithAuth` (`probe/probe.go:45`), called from `probeProviders` (`model.go:6460`), `probeThinkingOptions` (`model_options.go:205`), the settings browser's `probeCmd` (`settings/browser.go:72`) and the connect wizard's `runProbe` (`connect/connect.go:1326`). Capability probes build a client in the settings browser's `startConfirm` (`settings/browser.go:543`) and in the wizard's `prepareCmd` (`connect/connect.go:630`). Only `OllamaNative` can probe a model's capabilities (`ollama_native.go:514`).
- **The model cache is touched in four places:** the seed in `New` (`model.go:1346` to `1356`), `persistDiscovered` (`model.go:6536`), `evictDiscovered` (`model.go:6556`) and the provider rename in the settings browser (`settings/frames_collections.go:79` to `90`). An entry is keyed by provider name and by a hash of the provider's config that covers its literal key (`modelcache.go:51`).
- **There are two OAuth sign-ins:** the wizard's Codex login (`enterOAuthLogin`, `connect/connect.go:937`, which runs `oauthLogin` at `:446`) and `/mcp auth` (`handleMCPCommand`, `mcp.go:21`). `oauth.Engine.Authorize` calls the engine's `Open` and then the display's `ShowURL`, once each and in that order (`oauth.go:548` to `551`). Nothing calls a display's `Wait`.
- **A thinking budget margin changes no probe.** It shapes chat requests only. The TUI passed the session's margin, or the wizard's or browser's copy of it.
- **The connect wizard's sign-in has no Esc cancel.** `oauthLogin` runs under a five-minute timeout of its own, and a cancelled wizard drops the late result (`handleOAuthDone`). `/mcp auth` cancels through the panel's context.
- **`Update` has a value receiver** (`model.go:1603`). A command that runs off the Update goroutine captures the host, the session and the context values, never `m`.

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **Plan 7 is three plans.**
   - **7a (this plan):** the provider catalog, probes, the model cache and the two sign-ins.
   - **7b:** `Session.Config` with literal keys redacted, `Session.Layers`, the `Host` writes with the reload agent side, and preset setup.
   - **7c:** the trust prompt, the skill-gate settings and the agents panel's tool lookup.

   Providers go first because 7b's redacted read leaves the TUI without literal keys. Everything that needs a key must already be agent side by then: the probes and the sign-ins.
2. **A probe takes the provider's config from the client.** A probe often runs on a config that is not saved yet: the wizard's typed key, or a provider being edited in the settings browser. So `ProbeProvider`, `CanProbeModel` and `ProbeModel` take the name and the config, as `config/probe_provider` already takes `config` with its `apiKey`. C3's list of calls that carry a secret gains the probes. The adapter builds the client with `Deps.Auth`, with `Deps.ModelCacheDir` as its data directory (the directory the TUI passed), and with the current session's thinking budget margin.
3. **Capability probing has a check.** The settings browser decides on the Update goroutine whether to show "detecting…", by building the client and asking whether it can probe (`settings/browser.go:543` to `549`). `CanProbeModel` answers that synchronously, and `ProbeModel` runs the probe. A client that cannot be built makes `CanProbeModel` false. `ProbeModel` then returns zero capabilities and the error, which the wizard ignores, as it does today.
4. **The model cache is four explicit calls, keyed agent side.** `ListModels`, `SaveModels`, `ForgetModels` and `RenameModels`.
   - The TUI keeps its in-session map and every newest-attempt guard. A stale probe still moves neither the map nor the disk.
   - The adapter looks up the provider's config itself. The entry's hash covers the literal key, which the TUI's config loses in 7b.
   - `SaveModels` ignores an empty list, and a provider the session's config no longer has, as `persistDiscovered` did.
   - Without a cache directory, all four do nothing, and `ListModels` finds nothing.
5. **Thinking options stay a TUI computation.** `ResolveThinkingOptions` and `PreserveThinkingOptions` are pure over `schema` types. They move to `llm/schema` with `cloneThinkingOptions`, which is exported there as `CloneThinkingOptions`. The spec's "ListModels includes the thinking options" is dropped.
6. **The catalog is one read.** `ProviderTemplates` returns every template with every field, sorted by ID as `provider.All` sorts them. The wizard and the settings browser look a template up by ID in that list. `strutil.UniqueName` replaces `provider.UniqueName`.
7. **A sign-in is a check followed by a call, and reports its URL through a function.**
   - **The checks.** `PrepareProviderLogin` and `PrepareMCPLogin` run what runs today before a sign-in starts: build the Codex engine; resolve the MCP server and open the token store. They return the error with today's text, so a failure still shows at once and nothing starts.
   - **The calls.** `LoginProvider` and `LoginMCP` run the flow and return when it ends. Each takes `show func(url string)`, which the adapter calls once, with the authorization URL.
   - **Opening the browser.** The engine's `Open` stays nil agent side. The TUI's `show` opens the browser with the TUI's opener (C12) and then records the URL for its panel: the engine's order.
   - **P3.** Over ACP, the call's progress notification feeds `show`. C2 gains this rule.
   - **Cancellation.** The context cancels a flow. A cancelled flow's error still wraps `context.Canceled`, as the TUI checks today.
8. **The MCP reconnect stays in the TUI until 7b.** After a successful sign-in, `reconnectMCPServer` still reloads through the TUI's config reloader.
9. **The resolver moves into the adapter.**
   - `Deps.Auth` holds it. Nil means `provider.DefaultAuthResolver()`, exactly the substitution `tui.New` made, and `Run` passes `rt.AuthResolver`.
   - `inproc.Host.AuthResolver` returns it. With the new `tui.Model.Host`, it lets `app`'s test check that the runtime's resolver reached the adapter: `TestRunPassesAuthResolverToTUI` becomes `TestRunPassesAuthResolverToTheSeam`.
10. **Dead code goes.**
    - The model's `dataDir` field and `WithDataDir`: nothing reads the field.
    - The settings browser's `badgeForTemplate`: it has no caller.
    - `presetflow.ProbeCapabilitiesCmd`: only tests call it.
    - `probe.Provider`'s default-resolver form.
11. **`ErrorData` gains nothing.** Plan 6 expected plan 7 to add the provider's fields for `CodeProviderError`. No TUI code reads them.
12. **Two timing differences are accepted.** No user-visible text changes, but:
    - the probe timeout (`probe.Timeout`, five seconds) now also bounds building the client, which today happens before the timer starts;
    - a settings capability probe builds the client twice: once for the check, once for the probe.
13. **The allowlist loses 14 entries across 9 files, and five keys go entirely.** T1b gains no names: no `State` read moves. Tasks 1 and 3 to 6 list the entries.
14. **The new ACP methods are named**:
    - `config/provider_templates`;
    - `config/probe_model`, for both `CanProbeModel` and `ProbeModel`;
    - `config/models_list`, `config/models_save`, `config/models_forget` and `config/models_rename`;
    - `config/login_provider`, for both provider sign-in methods;
    - `session/mcp_login`, for both MCP methods.

    `ProbeProvider` uses `config/probe_provider`, which exists.

**Execution:**
- Branch first: `git switch -c p2-7a-providers` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-7a-providers: record the plan 7 revisions and plan 7a`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-7a-providers: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.** `providers.go` (new):

```go
// ProviderTemplate is one entry of the provider catalog the connect wizard
// offers (C10). Auth is "" for an API key or "oauth" for a browser sign-in.
type ProviderTemplate struct {
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	Type              string   `json:"type"`
	BaseURL           string   `json:"baseUrl,omitempty"`
	Local             bool     `json:"local,omitempty"`
	ToolCalling       bool     `json:"toolCalling,omitempty"`
	StructuredOutput  bool     `json:"structuredOutput,omitempty"`
	TemperatureLocked bool     `json:"temperatureLocked,omitempty"`
	Auth              string   `json:"auth,omitempty"`
	KeyEnv            string   `json:"keyEnv,omitempty"`
	KeyHint           string   `json:"keyHint,omitempty"`
	Models            []string `json:"models,omitempty"`
}

// ModelCapabilities is what a per-model probe reports beyond the provider's
// model list. A nil flag or a zero window means the probe could not tell.
type ModelCapabilities struct {
	ToolCalling   *bool `json:"toolCalling,omitempty"`
	ContextWindow int   `json:"contextWindow,omitempty"`
	Reasoning     *bool `json:"reasoning,omitempty"`
}
```

`TestProvidersJSON` (Task 1) pins these encodings:

| Value | JSON |
|---|---|
| `ProviderTemplate{ID: "openai-codex", Label: "OpenAI (ChatGPT subscription)", Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api", Auth: "oauth", ToolCalling: true, StructuredOutput: true, KeyHint: "Uses your ChatGPT subscription via browser login — no API key required.", Models: ["gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"]}` | `{"id":"openai-codex","label":"OpenAI (ChatGPT subscription)","type":"openai_codex","baseUrl":"https://chatgpt.com/backend-api","toolCalling":true,"structuredOutput":true,"auth":"oauth","keyHint":"Uses your ChatGPT subscription via browser login — no API key required.","models":["gpt-6-astra","gpt-5.6-sol","gpt-5.6-terra","gpt-5.6-luna"]}` |
| `ProviderTemplate{ID: "lmstudio", Label: "LM Studio (local)", Type: "openai_compatible", BaseURL: "http://localhost:1234/v1", Local: true}` | `{"id":"lmstudio","label":"LM Studio (local)","type":"openai_compatible","baseUrl":"http://localhost:1234/v1","local":true}` |
| `ModelCapabilities{}` | `{}` |
| `ModelCapabilities{ToolCalling: &true, ContextWindow: 32768}` | `{"toolCalling":true,"contextWindow":32768}` |
| `ModelCapabilities{ToolCalling: &false, Reasoning: &false}` | `{"toolCalling":false,"reasoning":false}` |

**`internal/llm/schema`, after Task 1.** `thinking.go` (new), moved from `llm/provider/thinking_options.go` with their bodies unchanged:

```go
// ResolveThinkingOptions keeps capability policy out of the rendering layer.
// Explicit configuration wins; discovery wins over conservative fallbacks.
// nil means unknown, and an empty Levels list means known unsupported.
func ResolveThinkingOptions(providerType, model string, override *ThinkingOptions, models []ModelInfo) *ThinkingOptions

// CloneThinkingOptions copies options, dropping "", "default" and repeated
// levels. It returns nil for nil.
func CloneThinkingOptions(options *ThinkingOptions) *ThinkingOptions

// PreserveThinkingOptions retains cached metadata when a model's enrichment
// fails. Explicit empty controls are authoritative and are never replaced.
func PreserveThinkingOptions(models, cached []ModelInfo) []ModelInfo
```

**`internal/strutil`, after Task 1.** Moved from `llm/provider/templates.go` with its body unchanged:

```go
// UniqueName returns base, or base-2, base-3 and on: the first that existing
// does not hold.
func UniqueName(base string, existing map[string]bool) string
```

**`internal/acpclient`, after Task 2.** `Host` gains, after `NewSession`:

```go
	// ProviderTemplates lists the provider catalog, sorted by ID, each with
	// every field.
	//
	// ACP: config/provider_templates (new in P3).
	ProviderTemplates() []acpwire.ProviderTemplate

	// ProbeProvider lists the models pc's endpoint reports, for a provider
	// named name. pc need not be saved, and may carry a literal key (C3). It
	// touches no cache: SaveModels does that.
	//
	// ACP: config/probe_provider (exists).
	ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error)

	// CanProbeModel reports whether ProbeModel can tell anything about a model
	// of pc: false when pc's client cannot be built or cannot probe a model.
	//
	// ACP: config/probe_model (new in P3).
	CanProbeModel(ctx context.Context, name string, pc config.ProviderConfig) bool

	// ProbeModel probes one model's capabilities. A client that cannot probe
	// returns zero capabilities and no error; one that cannot be built returns
	// zero capabilities and the error.
	//
	// ACP: config/probe_model (new in P3).
	ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error)

	// ListModels returns the cached model list for the provider called name,
	// when the cache holds one for the provider's current config that is still
	// fresh.
	//
	// ACP: config/models_list (new in P3).
	ListModels(ctx context.Context, name string) ([]schema.ModelInfo, bool)

	// SaveModels caches models for the provider called name, keyed by its
	// current config. An empty list, or a name the session's config does not
	// have, changes nothing.
	//
	// ACP: config/models_save (new in P3).
	SaveModels(ctx context.Context, name string, models []schema.ModelInfo) error

	// ForgetModels drops the cached list for the provider called name.
	//
	// ACP: config/models_forget (new in P3).
	ForgetModels(ctx context.Context, name string) error

	// RenameModels moves the cached list of the provider called from to the
	// name to. A from the cache does not hold changes nothing.
	//
	// ACP: config/models_rename (new in P3).
	RenameModels(ctx context.Context, from, to string) error

	// PrepareProviderLogin checks that the browser sign-in for the provider
	// called name can start, and returns why not.
	//
	// ACP: config/login_provider (new in P3).
	PrepareProviderLogin(ctx context.Context, name string) error

	// LoginProvider runs the browser sign-in for the provider called name and
	// stores its tokens. It calls show once with the authorization URL, which
	// the client opens, and returns when the sign-in ends.
	//
	// ACP: config/login_provider (new in P3). show is fed by the call's
	// progress notification.
	LoginProvider(ctx context.Context, name string, show func(url string)) error
```

`Session` gains, at the end:

```go
	// PrepareMCPLogin checks that the OAuth sign-in for the session's MCP
	// server called server can start: the server resolves and the token store
	// opens. Its error is the sentence the client shows.
	//
	// ACP: session/mcp_login (new in P3).
	PrepareMCPLogin(ctx context.Context, server string) error

	// LoginMCP runs the OAuth sign-in for the MCP server called server and
	// stores its tokens. It calls show once with the authorization URL, which
	// the client opens, and returns when the sign-in ends. A cancelled
	// sign-in's error wraps context.Canceled and stores nothing.
	//
	// ACP: session/mcp_login (new in P3). show is fed by the call's progress
	// notification.
	LoginMCP(ctx context.Context, server string, show func(url string)) error
```

`acpclient.go` imports `llm/schema` for these signatures, which `TestClientImportsStayOnTheWireSide` allows.

`methods.go` gains these rows, the `Host` ones after `NewSession`'s row and the `Session` ones at the end:

```go
	{Iface: "Host", Name: "ProviderTemplates", ACP: "config/provider_templates"},
	{Iface: "Host", Name: "ProbeProvider", ACP: "config/probe_provider", Exists: true},
	{Iface: "Host", Name: "CanProbeModel", ACP: "config/probe_model"},
	{Iface: "Host", Name: "ProbeModel", ACP: "config/probe_model"},
	{Iface: "Host", Name: "ListModels", ACP: "config/models_list"},
	{Iface: "Host", Name: "SaveModels", ACP: "config/models_save"},
	{Iface: "Host", Name: "ForgetModels", ACP: "config/models_forget"},
	{Iface: "Host", Name: "RenameModels", ACP: "config/models_rename"},
	{Iface: "Host", Name: "PrepareProviderLogin", ACP: "config/login_provider"},
	{Iface: "Host", Name: "LoginProvider", ACP: "config/login_provider"},

	{Iface: "Session", Name: "PrepareMCPLogin", ACP: "session/mcp_login"},
	{Iface: "Session", Name: "LoginMCP", ACP: "session/mcp_login"},
```

**`internal/acpclient/inproc`, after Task 2.** `Deps` gains, after `NewSession`:

```go
	// Auth is the runtime's credential and OAuth dependency: every provider
	// client and sign-in the adapter builds uses it. Nil means
	// provider.DefaultAuthResolver(), the substitution tui.New used to make.
	Auth provider.AuthResolver
	// ModelCacheDir is the data directory of the model cache and of the
	// clients the probes build. Empty means no cache: ListModels finds
	// nothing, and the other cache calls do nothing.
	ModelCacheDir string
```

`Host` gains:

```go
// AuthResolver returns the resolver the adapter builds with: Deps.Auth, or the
// production default when it was nil. Never nil. It exists for app's wiring
// test and goes with the adapter in P3.
func (h *Host) AuthResolver() provider.AuthResolver
```

`Session` gains an `auth provider.AuthResolver` field, which `newSession` sets from `Deps.Auth` with the same default.

Unexported pieces the steps name:
- `providers.go` (new): `templateWire(provider.ProviderTemplate) acpwire.ProviderTemplate` and `capsWire(provider.ModelCapabilities) acpwire.ModelCapabilities`, the two conversions; `(h *Host) client(name string, pc config.ProviderConfig) (provider.Provider, error)`, which builds a probe's client; `(h *Host) currentState() *session.State`, the current session's state read under `h.mu`.
- `logins.go` (new): `urlDisplay`, an `oauth.Display` whose `ShowURL` calls the `show` it holds and whose `Wait` returns nil; `mcpEngine(srv config.MCPServerConfig, store credentials.Store, logger *slog.Logger) *oauth.Engine`; `mcpLoginError(name string, err error) error`, which turns a store failure into the sentence the client shows.

**The sentences `PrepareMCPLogin` returns** (Task 2; each is today's note, `mcp.go:30` to `45`, word for word):

| Failure | Error |
|---|---|
| The server does not resolve | `commands.ResolveOAuthServer`'s error, unchanged |
| The store reports `credentials.ErrKeyringUnavailable` | An `*acpwire.Error` with `CodeKeyringUnavailable` and the message `OAuth tokens are stored in the OS keychain, which is unavailable here. Run Marshal on a machine with a working Keychain, Credential Manager, or Secret Service, then retry /mcp auth <name>.` |
| Any other store failure | `Cannot open the credential store: <the store's error>` |

`PrepareProviderLogin` returns the resolver's error with its text unchanged. One that wraps `credentials.ErrKeyringUnavailable` comes back as an `*acpwire.Error` with `CodeKeyringUnavailable` and the same message.

**`internal/app/tui`, after Tasks 3 to 6.**

```go
// probe/probe.go. Provider probes host for the models pc's endpoint reports,
// under Timeout, and returns a ResultMsg carrying fieldID and name. A nil host
// yields a ResultMsg whose Err is errNoHost.
func Provider(host acpclient.Host, fieldID, name string, pc config.ProviderConfig) tea.Cmd

// presetflow/probe.go. CapabilityProbedMsg.Caps becomes acpwire.ModelCapabilities.
func ProbeCapabilitiesCmdWithCancel(host acpclient.Host, providerName string, pc config.ProviderConfig, modelID string, requestID uint64) (tea.Cmd, context.CancelFunc)

// settings/browser.go. Replaces WithAuthResolver and WithDataDir.
func WithHost(host acpclient.Host) BrowserOption

// connect/connect.go. Opts gains Host and loses Auth; DataDir stays for preset
// setup until plan 7b. PreparedMsg.Caps becomes acpwire.ModelCapabilities, and
// Model.template an acpwire.ProviderTemplate.
type Opts struct {
	// ...existing fields...
	Host acpclient.Host
}

// model.go. Returns the host the model was built with.
func (m Model) Host() acpclient.Host
```

`errNoHost` is `probe`'s unexported error, "probe: no client host".

## Task 1: Wire types and pure helpers

**Goal:** `acpwire` holds the provider types, the thinking-option helpers live in `llm/schema`, and `UniqueName` lives in `strutil`, with every caller moved.

**Files:** `internal/acpwire/providers.go` (new), `providers_test.go` (new); `internal/llm/schema/thinking.go` (new), `thinking_test.go` (new); `internal/strutil/strutil.go`, `strutil_test.go`; `internal/llm/provider/thinking_options.go`, `anthropic.go`, `templates.go`, `templates_test.go`; `internal/app/tui/model.go`, `model_options.go`, `connect/connect.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`acpwire/providers.go`.** Add the Contracts types.
2. **`schema/thinking.go`.** Move `ResolveThinkingOptions`, `cloneThinkingOptions` (as `CloneThinkingOptions`) and `PreserveThinkingOptions` from `provider/thinking_options.go:19` to `53` and `150` to `166`, with their bodies and comments, the `schema.` qualifiers dropped.
   - In `provider/thinking_options.go`, `ThinkingOptionsFor` (`:220`) calls `schema.ResolveThinkingOptions`.
   - In `provider/anthropic.go:60`, the clone is `schema.CloneThinkingOptions`.
   - Nothing else in `provider` calls them. `go build ./...` finds any caller missed.
3. **`strutil.UniqueName`.** Move `UniqueName` from `provider/templates.go:260` into `strutil/strutil.go`. Move its two tests (`templates_test.go:235` to `250`) into `strutil_test.go`, renamed `TestUniqueNameNoCollision` and `TestUniqueNameWithCollision` as they are.
4. **The TUI's callers.**
   - `model.go:2191`, `:6493` and `:6529` call `schema.PreserveThinkingOptions`.
   - `model_options.go:196` calls `schema.ResolveThinkingOptions`. `model_options.go` then no longer imports `llm/provider`.
   - `connect/connect.go:1386` calls `strutil.UniqueName`.
5. **Allowlist.** Delete `model_options.go`'s `marshal/internal/llm/provider`. The entry keeps its two `config` names.
6. **Tests:**
   - **`acpwire/providers_test.go`** (new): **`TestProvidersJSON`** encodes the five Contracts values and compares the bytes with the table, in the table style of `TestNotesJSON`;
   - **`schema/thinking_test.go`** (new):
     - **`TestResolveThinkingOptionsPrefersTheOverride`**: an override with levels `["", "default", "low", "low", "high"]` returns a copy with `["low", "high"]`, and changing the copy leaves the override alone;
     - **`TestResolveThinkingOptionsFallsBack`**: with no override, a discovered model's options are returned; with neither, `openai_codex` gets levels `["low", "medium", "high"]` and mode `effort`, and any other type gets nil;
     - **`TestPreserveThinkingOptionsKeepsCachedControls`**: a model without options takes the cached entry's; a model whose options are set with an empty level list keeps them; the input slice is unchanged.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'func ResolveThinkingOptions\|func PreserveThinkingOptions\|func UniqueName' internal/llm/provider/
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/acpwire/ ./internal/llm/... ./internal/strutil/
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/llm/... ./internal/strutil/ ./internal/app/tui/...
```

Expected: the `grep` prints nothing. Every listed test passes, `TestWireFieldsFollowTheRules` and `TestTUIStaysOnTheClientSeam` included.

## Task 2: The host serves providers and sign-ins

**Goal:** the adapter serves the twelve new methods over `Deps.Auth` and `Deps.ModelCacheDir`. Nothing in the TUI calls them yet.

**Files:** `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `providers.go` (new), `providers_test.go` (new), `logins.go` (new), `logins_test.go` (new); `internal/app/tui/seam_test.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add the Contracts methods with their doc comments, and the Contracts rows.
2. **`inproc/inproc.go`.** `Deps` gains `Auth` and `ModelCacheDir` (Contracts), and `Session` gains `auth`. Add `Host.AuthResolver`: `Deps.Auth`, or `provider.DefaultAuthResolver()` when nil. **`inproc/events.go`:** `newSession` sets `auth` the same way.
3. **`inproc/providers.go`** (new):
   - **`currentState`**: the current session's state, read under `h.mu`.
   - **`client`**: `provider.NewFromConfigWithAuth` with the name, pc, `Deps.ModelCacheDir`, the current state's `ConfigSnapshot().Agent.ThinkingBudgetMargin`, an empty session ID and `h.AuthResolver()`.
   - **`ProviderTemplates`**: `templateWire` of each `provider.All()` entry, in its order. `templateWire` copies all twelve fields.
   - **`ProbeProvider`**: build the client, then return its `Models(ctx)`. A build error is returned as it is.
   - **`CanProbeModel`**: true when the client builds and implements `provider.CapabilityProber`.
   - **`ProbeModel`**: a build error returns zero capabilities and the error. A client that is not a prober returns zero capabilities and nil. Otherwise return `capsWire` of `ProbeCapabilities(ctx, model)`, which copies the three fields.
   - **`ListModels`**: false when `ModelCacheDir` is empty or the current state's config has no provider called name. Otherwise `modelcache.Load(dir).Lookup(name, pc, modelcache.DefaultTTL, time.Now())`. This is the seed's read (`model.go:1349` to `1356`).
   - **`SaveModels`**: nil and no write when the directory is empty, the list is empty, or the config has no such provider. Otherwise store `modelcache.Entry{ConfigHash: modelcache.HashProvider(pc), Models: models, FetchedAt: time.Now()}` under the name and return `modelcache.Save`'s error. This is `persistDiscovered` (`model.go:6536`).
   - **`ForgetModels`**: `evictDiscovered` (`model.go:6556`). Delete the entry when present, and save only then.
   - **`RenameModels`**: the settings rename (`settings/frames_collections.go:81` to `89`). When the cache holds `from`, move its entry to `to` and return `Save`'s error. Otherwise nil.
   - The config is always read through `ConfigSnapshot()`.
4. **`inproc/logins.go`** (new):
   - **`PrepareProviderLogin`**: call `h.AuthResolver().CodexOAuthEngine(name)` and return its error. One that wraps `credentials.ErrKeyringUnavailable` becomes an `*acpwire.Error` with `CodeKeyringUnavailable` and the same message (Contracts).
   - **`LoginProvider`**: get the engine the same way and return any error unchanged. Otherwise return `engine.Authorize(ctx, &urlDisplay{show: show})` unchanged. The engine's `Open` is never set: `show` is how the client hears of the URL.
   - **`PrepareMCPLogin`**: `errSessionClosed` on a closed session. Resolve the server with `commands.ResolveOAuthServer` over the state's `ConfigSnapshot().MCP.Servers`, and return its error unchanged. Then open the store with `s.auth.OpenStore("marshal")`, and return `mcpLoginError(server, err)` on failure (Contracts table).
   - **`LoginMCP`**: `errSessionClosed` on a closed session. Resolve and open as `PrepareMCPLogin` does, returning the same errors. Then build `mcpEngine(srv, store, s.state.Logger())` and return `Authorize(ctx, &urlDisplay{show: show})` unchanged.
   - **`mcpEngine`**: the engine `mcp.go:68` builds: the server's URL, client name "marshal", the store and the logger, with `Open` left nil.
5. **`tui/seam_test.go`.** `fakeHost` embeds `acpclient.Host`, so the new methods compile. Its tests call only `SharedFilesystem` and `NewSession`.
6. **Tests.** A test resolver, `testAuth`, lives in `logins_test.go`. It has an in-memory store, a store error, an engine error, the services and engine names it was asked for, and an engine cache by name, as the TUI's `mcp_test.go:345` and `connect_test.go:1810` resolvers do. Hosts are built with `inproc.New(inproc.Deps{State: newState(t, "s1"), Auth: auth, ModelCacheDir: dir})`.
   - **`inproc/providers_test.go`** (new):
     - **`TestProviderTemplatesListTheCatalog`**: the list has as many entries as `provider.All()`, in its order. Each entry's twelve fields equal the template's, and the `openai-codex` entry encodes as in Contracts;
     - **`TestProbeProviderListsModels`**, **`TestProbeProviderReportsFailures`** and **`TestProbeProviderPreservesModelLimits`**: `probe_test.go`'s `TestProviderSuccess`, `TestProviderNon200`, `TestProviderConnectionRefused` and `TestProviderPreservesModelLimits`, moved, with `ProbeProvider` in place of `probe.Provider`;
     - **`TestProbeProviderUsesTheAdaptersResolver`**: a Codex provider whose engine fails with an error wrapping `credentials.ErrKeyringUnavailable`: the error wraps it, and `testAuth` was asked for exactly one engine, named after the provider (`probe_test.go:165`, moved);
     - **`TestAuthResolverDefaultsToProduction`**: with no `Deps.Auth`, `AuthResolver()` equals `provider.DefaultAuthResolver()`; with one, it is that one;
     - **`TestCanProbeModel`**: an `ollama` provider with a base URL is true; an `openai_compatible` one is false; a Codex one whose engine fails is false;
     - **`TestProbeModelProbesOllama`**: an `httptest` server answers `POST /api/show` with capabilities `["completion", "tools"]` and model info `{"qwen3.context_length": 40960}`. `ProbeModel` returns tool calling true, reasoning false and a window of 40960. An `openai_compatible` provider returns zero capabilities and nil, and a Codex one whose engine fails returns zero capabilities and the error;
     - **`TestModelCacheRoundTrip`**, with a provider "local" in the state's config:
       - `SaveModels` then `ListModels` returns the models, and `modelcache.Load` holds an entry whose hash is `HashProvider` of the config;
       - after the state's provider gets a new base URL, `ListModels` finds nothing;
       - `RenameModels("local", "renamed")` moves the entry, and renaming a missing name does nothing;
       - `ForgetModels` removes the entry;
     - **`TestModelCacheIgnoresWhatPersistIgnored`**: `SaveModels` with an empty list, or with a name the config lacks, writes no file. With no `ModelCacheDir`, all four calls return nil and `ListModels` returns false;
   - **`inproc/logins_test.go`** (new):
     - **`TestPrepareProviderLoginReportsTheResolver`**: with an engine error wrapping `ErrKeyringUnavailable`, the error is coded `keyring_unavailable` with the resolver's message; with a plain engine error, the error is that one; with no error, nil, and the engine was asked for under the provider's name;
     - **`TestLoginProviderRunsTheFlow`**: `connect_test.go`'s `TestOAuthLoginCommandRunsAgainstTestOwnedServer` (`:1971`), moved. Its pinned engine is seeded into `testAuth`'s cache under "codex-test". `LoginProvider` is called with a `show` that records into a mutex-guarded slice. The test drives the loopback callback from the URL `show` received. `LoginProvider` returns nil, `show` was called once, the token is in the store, and the engine's `Open` is still nil;
     - **`TestPrepareMCPLoginSentences`**: a table over the Contracts sentences. An unconfigured name returns `ResolveOAuthServer`'s error for it; a store error of `ErrKeyringUnavailable` returns the coded keychain sentence naming the server; a store error "injected store failure" returns "Cannot open the credential store: injected store failure"; a configured OAuth server with a working store returns nil, having opened the store once, for "marshal". The server is `"linear": {URL: "https://mcp.example.com/sse", Auth: "oauth", Trust: "unrestricted"}`, as in `mcp_test.go:393`;
     - **`TestMCPEngineRunsAgainstATestServer`**: `mcp_test.go`'s `TestMCPAuthAuthorizeCommandUsesStubBrowser` (`:497`), moved. It builds the engine with `mcpEngine` over the test-owned metadata server, sets `CacheDir` and `LoopbackTimeout` as that test does, and runs `Authorize` with a `urlDisplay` whose `show` records. The test cancels once a URL arrives. The error is nil or wraps `context.Canceled`, `show` saw exactly one URL containing "/authorize?", and no token was stored;
     - **`TestAClosedSessionStartsNoMCPLogin`**: after `host.swap`, the old session's `PrepareMCPLogin` and `LoginMCP` return `CodeSessionClosed`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/acpclient/...
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/
go vet ./internal/acpclient/...
```

Expected: every listed test passes, `TestMethodTableCoversTheInterfaces`, `TestMethodDocsNameTheirACPMethod`, `TestAdapterDoesNotImportApp` and `TestClientImportsStayOnTheWireSide` included. T4 passes on both sides: `config/probe_provider` is registered, and the eight new names are not. `tui` passes unchanged.

## Task 3: Model probes go through the host

**Goal:** every model-list probe asks the host, and the settings browser and the connect wizard hold the host.

**Files:** `internal/app/tui/probe/probe.go`, `probe/probe_test.go`; `model.go`, `model_options.go`, `model_test.go`, `boundary_allowlist_test.go`; `settings/browser.go`, `settings/state.go`, `settings/browser_test.go`; `connect/connect.go`, `connect/connect_test.go`; `internal/app/app.go`.

**Steps:**

1. **`probe/probe.go`.** `Provider` takes the host (Contracts), and `ProviderWithAuth` goes.
   - The command makes a `Timeout` context from `context.Background()` and calls `host.ProbeProvider` with the name and pc.
   - It returns `ResultMsg{FieldID, Provider: name, Models}`, or the same with `Err` on failure: today's result.
   - A nil host returns the `ResultMsg` with `errNoHost`.
   - `probe` drops `llm/provider` and imports `acpclient`. `IsLocalhost` is unchanged.
2. **The model's probes.** `probeProviders` (`model.go:6460`) and `probeThinkingOptions` (`model_options.go:205`) call `probe.Provider(m.host, …)` with the same field ID, name and config. Nothing else around them changes: the remote gate, the generation counters and the result wrappers stay.
3. **The settings browser.**
   - `settings/browser.go` gains `WithHost` (Contracts), which sets the state's new `host acpclient.Host` field.
   - `probeCmd` (`:71`) calls `probe.Provider(s.host, fieldID, name, pc)`.
   - The state's `dataDir`, `SetDataDir` and `auth`, and `WithDataDir`, `WithAuthResolver`, `AuthResolver` and `probeProviderFor` stay until Task 5.
   - `openSettingsBrowser` (`model.go:1135`) passes `settings.WithHost(m.host)` beside its current options.
4. **The connect wizard.**
   - `Opts` gains `Host` (Contracts), and the model keeps it in a `host` field.
   - `runProbe` (`connect/connect.go:1326`) calls `probe.Provider(m.host, "connect", m.providerName, m.providerCfg)`.
   - `openConnect` and `openModels` (`model.go:6380` and `:6401`) pass `Host: m.host`.
   - `Opts.Auth` stays until Task 6.
5. **`app.go`.** The `deps` literal (`app.go:2735`) gains `Auth: rt.AuthResolver` and `ModelCacheDir: rt.DataDir`. `WithAuthResolver` and `WithModelCache` are still passed, for the surfaces Tasks 4 to 6 move.
6. **Test isolation.** `newTestModel` and `newTestModelInRepo` (`model_test.go:6561` and `:6582`) put a `newMemAuth()` in their `Deps` as `Auth`, beside the `WithAuthResolver` they keep until Task 6. Every probe a TUI test runs through `newTestModel` then still uses an in-memory store.
7. **Allowlist.** Delete `probe/probe.go`'s key: it no longer imports `llm/provider`.
8. **Tests:**
   - **`probe/probe_test.go`.** `TestIsLocalhost` stays. Delete the rest with the file's `memAuth`:
     - Task 2 moved `TestProviderSuccess`, `TestProviderNon200`, `TestProviderConnectionRefused`, `TestProviderPreservesModelLimits`, `TestProviderWithAuthUsesInjectedResolver` and `TestProviderWrapperSelectsProductionAdapter` to `inproc`;
     - `TestProviderTimeout` becomes `TestProviderBoundsTheCall` below;
     - `TestProviderWithAuthNilResolverFailsClosed` has no successor: the adapter never builds a client with a nil resolver.

     Add:
     - **`TestProviderAsksTheHost`**: a fake host (an embedded `acpclient.Host` with `ProbeProvider` recording its name and config and returning two models) gives a `ResultMsg` with the field ID, the name and both models;
     - **`TestProviderReportsTheHostsError`**: an error from the fake is the result's `Err`;
     - **`TestProviderBoundsTheCall`**: with `Timeout` set to 50ms for the test, a fake that blocks until its context is done returns an error wrapping `context.DeadlineExceeded`;
     - **`TestProviderWithoutAHost`**: a nil host gives `errNoHost`;
   - **`model_test.go`.** `TestProbeProvidersUsesInjectedResolver` (`:6788`) builds its model with `testModelWith(inproc.Deps{State: …, Auth: auth})` instead of setting `m.auth`. It asserts the same: one engine request, through the adapter;
   - **`settings/browser_test.go`.** `TestBrowserAuthResolverReachesFrameProbes` (`:926`) and `TestProbeAllProvidersUsesInjectedResolver` (`:959`) build an `inproc` host over a test state with `Deps.Auth: auth` and pass it with `WithHost`, or set `s.host`. They keep their assertions. The first drops its check that `WithAuthResolver` is retained, and the second its `SetDataDir` call: Task 5 deletes both;
   - **`connect/connect_test.go`.** No test runs `runProbe`: they deliver results with `wizardResult` (`:186`). Tasks 5 and 6 need a host in nearly every wizard, so add the helpers now:
     - **`testHost(t, auth)`** returns an `inproc` host over `session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})`, with `Deps.Auth` set to auth, or to a `newMemAuth()` when auth is nil;
     - **`newTestWizard(t, opts)`** sets `opts.Host` to `testHost(t, nil)` when it is nil, and returns `New(opts)`;
     - every `New(Opts{…})` in the file becomes `newTestWizard(t, Opts{…})`. That is 85 sites, all in functions that hold `t`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'ProviderWithAuth' internal/
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/...
```

Expected: the `grep` prints nothing. Every test passes, the goldens and `TestTUIStaysOnTheClientSeam` included.

## Task 4: The model cache goes agent side

**Goal:** the seed, the write-through, the eviction and the rename go through the host's cache calls. The TUI keeps its map and its guards.

**Files:** `internal/app/tui/model.go`, `model_test.go`, `settings/frames_collections.go`, `settings/frames_collections_test.go`, `boundary_allowlist_test.go`, `seam_test.go`.

**Steps:**

1. **The seed** (`model.go:1346` to `1356`). For each name in the state's providers, ask `m.host.ListModels(m.ctx, name)` and keep a list it returns. The `m.modelCacheDir` guard goes: without a cache directory, the host returns nothing.
2. **`persistDiscovered`** calls `m.host.SaveModels(m.ctx, name, models)` and ignores the error, as it does today. Its guards move into the host (Task 2).
3. **`evictDiscovered`** calls `m.host.ForgetModels(m.ctx, name)` and ignores the error.
4. **The rename** (`settings/frames_collections.go:81` to `89`). When the state has a host, call `s.host.RenameModels(context.Background(), k, v)`. Return its error wrapped as `rename model cache: %w`, the text it has today. Without a host, skip the cache, as an empty `dataDir` did. The rename no longer reads `s.dataDir`; Task 5 deletes it.
5. **`WithModelCache`.** Its doc becomes "WithModelCache sets the data directory preset setup reads the models.dev catalog and its provenance from. Plan 7b deletes it." Production still passes it (`app.go:2802`).
6. **Allowlist.** Delete `model.go`'s `marshal/internal/llm/provider/modelcache` and `settings/frames_collections.go`'s `marshal/internal/llm/provider/modelcache`.
7. **Tests:**
   - **`seam_test.go`** gains **`testModelCached(t, state, dir, opts...)`**: `testModelWith(inproc.Deps{State: state, ModelCacheDir: dir, Auth: newMemAuth()}, opts...)`;
   - **`model_test.go`.** The ten `WithModelCache(dir)` sites (`:8430`, `:8583`, `:8686`, `:8711`, `:8734`, `:8760`, `:8786`, `:8816`, `:8889` and the "no cache" test at `:9050`) build with `testModelCached` instead, passing an empty directory for `TestNoCacheDirIsHarmless`. Their assertions read the cache file with `modelcache.Load` as today, and stay unchanged;
   - **`settings/frames_collections_test.go`.** No test covers the rename's cache move today. Add **`TestRenameProviderMovesItsCachedModels`**, in the style of `TestProviderRenameCascadesToPresetsAndDiscovered` (`:139`):
     - the state's host is an `inproc` host over a cache directory, and the cache holds an entry for "ollama". Renaming the provider to "local" through the name field leaves `modelcache.Load` with an entry under "local" and none under "ollama";
     - with the cache file made read-only (`0o444`, restored in `t.Cleanup`), the rename returns an error that starts with "rename model cache:".

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'modelcache\.' internal/app/tui --include='*.go' | grep -v _test.go
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the `grep` prints nothing. Every test passes.

## Task 5: Capability probes and the catalog go through the host

**Goal:** the settings browser and the wizard probe a model's capabilities through the host and read the catalog from it. The settings browser holds no resolver.

**Files:** `internal/app/tui/presetflow/probe.go`, `presetflow/probe_test.go`; `settings/browser.go`, `settings/state.go`, `settings/frames_collections.go`, `settings/browser_test.go`, `settings/frames_collections_test.go`; `connect/connect.go`, `connect/connect_test.go`; `textfield/paste_test.go`; `model.go`, `model_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`presetflow/probe.go`.**
   - `ProbeCapabilitiesCmdWithCancel` takes the host, the provider's name and config, the model and the request ID (Contracts). It keeps its `CapabilityProbeTimeout` context and its cancel function.
   - The command calls `host.ProbeModel` and returns `CapabilityProbedMsg` with the name, the model, the request ID and the capabilities. An error leaves the capabilities zero, as a non-prober did.
   - `CapabilityProbedMsg.Caps` becomes `acpwire.ModelCapabilities`.
   - Delete `ProbeCapabilitiesCmd` (decision 10).
   - `presetflow` drops `llm/provider`.
2. **The settings browser's capability probe** (`startConfirm`, `settings/browser.go:530`). After the confirm state is set up, return nil when the state has no host or `CanProbeModel` is false. Otherwise set `detectingCaps` and dispatch `ProbeCapabilitiesCmdWithCancel(s.host, name, pc, model, b.capProbeID)`, keeping its cancel. `handleCapabilityProbed` reads `msg.Caps.ToolCalling` and `msg.Caps.ContextWindow` as before.
3. **The wizard's capability probe** (`prepareCmd`, `connect/connect.go:604`).
   - Capture the host with the other values.
   - Inside the command, when the host is set, call `ProbeModel` under the same 10-second timeout with the provider's name, config and chosen model. Keep the capabilities and drop the error.
   - The fold into the reported model and everything after it are unchanged.
   - `PreparedMsg.Caps` becomes `acpwire.ModelCapabilities`. `done` reads its `ContextWindow` and `ToolCalling` as before.
4. **The catalog in the wizard.**
   - `New` reads `opts.Host.ProviderTemplates()` once into a `templates` field when the host is set.
   - `enterPickTemplate` (`:499`) ranges over it.
   - The template pick (`:898`) and `providerTemplate` (`:1420`) look up by ID through a `lookupTemplate(id)` helper over the list.
   - `m.template` becomes an `acpwire.ProviderTemplate`. The literals at `:901` and `:906` build that type.
   - `badgeForTemplate` (`:863`) takes the wire type.
5. **The catalog in the settings browser.** The provider model picker's template lookup (`settings/frames_collections.go:273`) calls `ProviderTemplates` on the state's host and looks up the ID in it. With no host it finds nothing, as an unknown template did. Delete the dead `badgeForTemplate` (`:305`).
6. **The settings browser's resolver and data directory go.** Delete `probeProviderFor`, `WithAuthResolver`, `AuthResolver` and the state's `auth`, along with its default in `newState`. Delete the state's `dataDir`, `SetDataDir` and `WithDataDir` too: once the capability probe and the rename go through the host, nothing in `settings` reads the directory. `openSettingsBrowser` stops passing `settings.WithAuthResolver` and `settings.WithDataDir`. `settings` drops `llm/provider`.
7. **`model.go`.** Its `presetflow.CapabilityProbedMsg` forward (`:2205`) is unchanged.
8. **Allowlist.** Delete four entries:
   - `presetflow/probe.go`'s key;
   - `settings/browser.go`'s `marshal/internal/llm/provider`. Its `config` entries stay;
   - `settings/state.go`'s key;
   - `settings/frames_collections.go`'s key, which now holds nothing.
9. **Tests:**
   - **`presetflow/probe_test.go`.** The fake providers become a fake host whose `ProbeModel` returns set capabilities, or blocks until cancelled. **`TestProbeCapabilitiesCmdCallsTheHost`**: the message carries the name, the model and the capabilities. **`TestProbeCapabilitiesCmdCarriesRequestID`**: request ID 42 comes back, and the cancel function ends a blocking probe. **`TestProbeCapabilitiesCmdTreatsAnErrorAsUnknown`**: an error gives zero capabilities;
   - **`settings/browser_test.go`.**
     - `TestBrowserEscCancelsCapabilityProbeAndRejectsLateResult` (`:316`) gets a host over a test state, since an `ollama` provider can probe, and passes it with `WithHost`. Its assertions stay;
     - delete `TestProbeProviderForUsesInjectedResolver`, `TestProbeProviderForNilResolverFailsClosed`, `TestRebuiltBrowserRetainsAuthResolver` and `TestNewBrowserDefaultsToProductionResolver`. The adapter's tests cover what they pinned;
     - add **`TestRebuiltBrowserKeepsTheHost`**: two browsers built with the same `WithHost` hold the same host;
   - **`connect/connect_test.go`.** The 47 template picks (`ollama`, `openrouter`, `openai-codex`, `kimi`) read the catalog from the host `newTestWizard` supplies, and need no change. `TestPrepareCmdDoesNotMutateLivePresetMap` and `TestPrepareCmdSkipsCatalogWhenDenied` keep their assertions: their provider cannot probe a model, so the capabilities stay zero, as they were under the default resolver;
   - **`textfield/paste_test.go`.** The "connect api key input" case (`:48`) picks `openrouter`, so its wizard gets `Host` from an `inproc` host over a test state. That pick builds no client, so the case needs no resolver;
   - **`settings/frames_collections_test.go`.** `TestSettingsModelPickerUsesStoredTemplateID` (`:187`) sets the state's host from a `testHost(t)` helper, added beside the file's other helpers: an `inproc` host over a test state, with the browser tests' `newMemAuth()` as its resolver;
   - **`model_test.go`.** `TestOpenSettingsBrowserForwardsAuth` (`:6770`) becomes **`TestOpenSettingsBrowserForwardsTheHost`**: the docked browser holds `m.host`. The browser gains an unexported `hostForTest` accessor used only by this test.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn '"marshal/internal/llm/provider"' internal/app/tui/settings internal/app/tui/presetflow --include='*.go' | grep -v _test.go
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the `grep` prints nothing. Every test passes.

## Task 6: Sign-ins go through the seam

**Goal:** the Codex sign-in and `/mcp auth` run agent side. The TUI holds no resolver, and `app` hands the runtime's resolver to the adapter alone.

**Files:** `internal/app/tui/connect/connect.go`, `connect/connect_test.go`; `mcp.go`, `mcp_test.go`, `model.go`, `model_test.go`, `approval_test.go`, `callers_test.go`, `doctor_secret_test.go`, `boundary_allowlist_test.go`; `internal/app/app.go`, `app_test.go`.

**Steps:**

1. **The Codex sign-in** (`connect/connect.go`).
   - `enterOAuthLogin` (`:937`) sets the step and its texts as today. It then calls `m.host.PrepareProviderLogin(ctx, m.providerName)` under a `context.Background()`. An error sets `m.err` to its text and returns no command, as the engine failure did. On success it makes the `oauthDisplay` and returns `oauthLogin(m.host, m.providerName, m.oauthDisplay, m.openBrowser)`.
   - `oauthLogin` (`:446`) takes the host instead of the engine. It keeps its nil-opener fallback and its five-minute timeout. The command calls `LoginProvider` with a `show` that calls the opener with the URL and then `display.ShowURL`. It returns `OAuthDoneMsg{Provider, Err}` as today.
   - `codexOAuthEngine` goes.
   - `oauthDisplay` keeps `ShowURL` and `URL`; its `Wait` goes, since nothing calls it.
   - `Opts.Auth`, the `auth` field, its default in `New` and `AuthResolver` go. `OpenBrowserFn` and the opener's default stay.
   - `connect` drops `llm/provider` and `oauth`. It keeps `presetsetup` and `modelcatalog` until 7b.
2. **`/mcp auth`** (`mcp.go`).
   - After the usage check, `handleMCPCommand` calls `m.sess.PrepareMCPLogin(m.ctx, name)`. An error is noted with its text, and the command returns with the viewport refreshed and no command. This covers the resolve failure and both store failures, with their texts unchanged (Contracts).
   - On success, the display, the panel, the cancel and `mcpAuthServer` are set up as today.
   - `authorizeMCPCmd` takes the session, the server name, the display, the opener and the cancel. Its command calls `LoginMCP` with a `show` that calls the opener (or `mcpauth.OpenBrowser` when nil) and then `disp.ShowURL`. It returns `mcpauth.AuthDoneMsg{Err}` and cancels on return, as today.
   - `handleMCPCommand` passes `m.sess` and `m.openBrowser`.
   - `mcpOAuthEngine` goes, and `mcp.go` drops `commands`, `credentials` and `oauth`.
   - `handleMCPAuthDone` and `reconnectMCPServer` are unchanged (decision 8).
3. **The model's resolver goes** (`model.go`).
   - Delete `WithAuthResolver` (`:696`), `AuthResolver` (`:716`), the `auth` field (`:474` to `480`) and its default in `New` (`:1328` to `1330`).
   - `openConnect` and `openModels` stop passing `Auth`.
   - Add `Host` (Contracts).
   - Delete the unused `dataDir` field and `WithDataDir` (`:686`) (decision 10).
   - `model.go` drops `llm/provider`.
4. **`app.go`.** Stop passing `tui.WithAuthResolver` and `tui.WithDataDir` (`app.go:2778` to `2784`). Move the comment about the resolver onto the `deps` literal's `Auth` line, reworded: the adapter builds every provider client and sign-in the TUI asks for, so an injected resolver must reach it.
5. **Allowlist.** Delete six entries:
   - `connect/connect.go`'s `marshal/internal/llm/provider` and `marshal/internal/oauth`, leaving its `presetsetup` and `modelcatalog` entries;
   - `mcp.go`'s key, with its three entries;
   - `model.go`'s `marshal/internal/llm/provider`.
6. **Tests:**
   - **The TUI's resolver options.** Every `WithAuthResolver(newMemAuth())` goes: eleven sites, five in `approval_test.go`, one each in `callers_test.go` and `doctor_secret_test.go`, and four in `model_test.go`. Two of the four are `newTestModel` and `newTestModelInRepo`, which already put the resolver in `Deps` (Task 3). The other nine build their models with `testModelWith` and a `Deps` holding `Auth: newMemAuth()`. `WithOpenBrowser` stays everywhere: opening the browser is the TUI's job (C12);
   - **`mcp_test.go`.**
     - `mcpAuthModel` (`:387`) builds its model over a `Deps` with the given resolver, and with the server set in the state's config as today.
     - `TestMCPAuthUsesInjectedStore`, `TestMCPAuthUnavailableStorePropagates`, `TestMCPAuthOtherStoreErrorPropagates` and `TestMCPAuthInjectedErrorNeverFallsBackToRealStore` keep their assertions: the notes, no command, no panel, and one store request.
     - Move `TestMCPAuthAuthorizeCommandUsesStubBrowser` to `inproc` (Task 2). In its place, **`TestMCPAuthCommandOpensTheURL`** uses a fake session that embeds the adapter's session and overrides `LoginMCP`: it calls `show` with "https://auth.example/authorize?x=1" and then blocks until its context is done. Running `authorizeMCPCmd` with a recording opener and a real `mcpauth.Display`, then cancelling, leaves the opener with that one URL and the display holding it. The message's error wraps `context.Canceled`;
   - **`connect/connect_test.go`.**
     - Every `Opts{…, Auth: x, …}` becomes `Opts{…, Host: testHost(t, x), …}`. That is the nine sites from `:948` to `:1128`, plus `oauthTestModel` (`:1883`) and the sites at `:1896` and `:1954`. `newTestWizard` leaves a host that is already set alone.
     - `TestNewInjectsAuthDependency` becomes **`TestNewKeepsTheHostAndOpener`**: the wizard holds the host it was given, and an omitted opener is replaced by the production one, so it is not nil.
     - `TestEnterOAuthLoginUsesInjectedResolver` and `TestEnterOAuthLoginSurfacesResolverError` keep their assertions: one engine request, and the failure in `m.err`. `PrepareProviderLogin` makes that one request.
     - `TestOAuthLoginWithUnavailableStoreFailsClosed` keeps its assertions: no command, and an error.
     - `TestOAuthLoginWiresBrowserOpener` (`:1100`) is replaced by **`TestOAuthLoginOpensTheURL`**. Run `oauthLogin` against a host whose resolver holds `TestLoginProviderRunsTheFlow`'s pinned engine, and drive the callback. The opener saw the one authorization URL, and the display holds it.
     - `TestOAuthLoginCommandRunsAgainstTestOwnedServer` moved to `inproc` (Task 2), so delete it here.
     - `TestOAuthLoginNilOpenerFallsBackToProduction` becomes a check that `oauthLogin` with a nil opener still returns a command. The fallback to `mcpauth.OpenBrowser` is unexported, so cover it by reading the code, as the old test's comment did;
   - **`model_test.go`.**
     - `TestOpenConnectForwardsAuthAndBrowser` and `TestOpenModelsForwardsAuth` (`:6728` and `:6750`) become **`TestOpenConnectForwardsTheHostAndBrowser`** and **`TestOpenModelsForwardsTheHost`**. The wizard holds `m.host`, read through an unexported `hostForTest` accessor on `connect.Model` that only these tests use, and the opener is set.
     - **`TestModelHostIsTheOneItWasBuiltWith`**: `Host()` returns the host given to `New`;
   - **`app_test.go`.** `TestRunPassesAuthResolverToTUI` (`:6169`) becomes **`TestRunPassesAuthResolverToTheSeam`**. Its program runner reads `model.(tui.Model).Host().(*inproc.Host).AuthResolver()` and asserts it is the injected resolver. The no-credential-path assertion stays.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'AuthResolver' internal/app/tui --include='*.go' | grep -v _test.go
grep -rn '"marshal/internal/\(llm/provider\|oauth\|credentials\)"' internal/app/tui --include='*.go' | grep -v _test.go
TMPDIR=/private/tmp/p2-7a-suite go test ./internal/app/tui/... ./internal/app/ ./internal/acpclient/...
go vet ./internal/app/... ./internal/acpclient/...
```

Expected: both `grep`s print nothing. Every test passes, `TestTUIStaysOnTheClientSeam` and `TestRunPassesAuthResolverToTheSeam` included.

## Task 7: Verification and docs

**Goal:** the full suite is green, and the spec and `AGENTS.md` record plan 7a.

**Files:** `docs/daemon/p2-client-seam.md`, `AGENTS.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 7a entry to the Implementation record, after plan 6's. It lists:
   - the `acpwire` types;
   - the twelve methods with their `Methods` rows: one on an existing ACP method and eleven on eight new ones;
   - `Deps.Auth`, `Deps.ModelCacheDir` and `Host.AuthResolver`;
   - the thinking helpers moved to `llm/schema`, and `UniqueName` moved to `strutil`;
   - that the TUI's probes, model cache, catalog and sign-ins go through the seam, with the TUI keeping its discovery map and guards;
   - the options and accessors deleted;
   - the allowlist's 14 entries across 9 files, five keys entirely, and no T1b names.
2. **`AGENTS.md`.** In "Inject auth explicitly", the sentence that names the TUI's "probe/login surfaces" becomes: it reaches provider construction, the OAuth refresh workers, the MCP managers, and the client seam's adapter, which builds the probes and sign-ins the TUI asks for.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-7a-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-7a-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `301e19c8`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `301e19c8`, all 117 packages passed. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository with a local provider (LM Studio or Ollama) and check by eye:
- `/connect` lists the provider templates as before, with their local and remote badges;
- picking a local template probes it and lists its models;
- `/models` opens the model picker at once with the cached list, and Ctrl+R in the picker re-probes;
- in `/settings`, picking an Ollama model for a role shows "detecting…" while it probes, and a non-Ollama model does not;
- renaming a provider in `/settings` keeps its models listed in `/models` without a re-probe;
- `/mcp auth` with no such server notes the same sentence as before. With an OAuth server configured, it opens the browser and shows the URL, and Esc cancels with "Authorization cancelled; no credentials were saved.";
- if a ChatGPT login is at hand: `/connect`, then "OpenAI (ChatGPT subscription)", opens the browser, and finishing the login moves on to the model step.

## Integration notes

- **Plan 7b** moves the config reads and writes.
  - `Session.Config` redacts literal keys (C3). The probes then get a config without its key for a saved provider. 7b decides how `ProbeProvider`, `CanProbeModel` and `ProbeModel` fill in the stored key: for example, from the session's config when the client's config names a saved provider and carries no key. The cache calls already key agent side.
  - 7b moves preset setup: the models.dev catalog, the provenance, `presetsetup.Apply`, and the connect accept with its provenance save. That deletes `WithModelCache` and `connect.Opts.DataDir`, and takes `presetsetup` and `modelcatalog` off `connect.go` and `model.go`.
  - 7b moves `reconnectMCPServer`'s reload agent side.
- **Plan 7c** moves the trust prompt, the skill-gate settings and the agents panel's tool lookup.
- **P3:**
  - the twelve methods send their ACP methods. `ProbeProvider` sends `config/probe_provider` with `{config}`, and its result gains the fields `schema.ModelInfo` carries beyond `id` and `contextWindow`;
  - `LoginProvider` and `LoginMCP` feed `show` from the call's progress notification. ACP's `/mcp auth` command, which already emits the URL as a session update, can move onto `session/mcp_login`;
  - `CanProbeModel` becomes a call on the wire rather than a local check, so the settings browser's "detecting…" decision waits on it;
  - `Host.AuthResolver` goes with the adapter.

## Self-review

- **Every task verifiable alone.** Task 1 moves pure code and adds types. Task 2 adds adapter calls nothing uses. Tasks 3 to 6 each move one kind of call — model probes, the cache, capability probes with the catalog, and the sign-ins — and leave the older path in place for the surfaces a later task moves. Each keeps T1 and the goldens green, and trims exactly the allowlist entries it makes stale.
- **Cold executor.** Each step names the function, what it calls after the change, and what it does with the result. The probe and cache rules, the sign-in check and its sentences, the order of opening and showing the URL, and the test migrations' forms are spelled out. Each new test names its setup and the behaviour it pins.
- **Contracts.** The wire types and their encodings, the moved helpers, the twelve methods and their rows, `Deps`' fields, `Host.AuthResolver`, the unexported helpers the steps name, and the TUI's changed signatures are declared there.
- **Anchors.** Every path, symbol and line was checked at `301e19c8`.
- **Spec.** C2, C3, C10 and the Migration order are updated, with the revision record "2026-11-05, against `301e19c8` (plan 6 merged)" holding decisions 1 to 14.
- **No placeholders.**

## Recorded deviations

These were made during execution and the post-execution review, and are correct
as built:

- **Tasks 5 and 6 export `HostForTest()`** instead of the unexported `hostForTest`
  the steps name. The TUI's own tests (`model_test.go`, package `tui`) read the
  accessor, and an unexported method on `settings.BrowserPanel` or
  `connect.Model` is unreachable from there.
- **Task 5 also changed `internal/app/tui/settings/state_test.go`**, a file
  outside its list: `TestStateDataDirDefaultsEmpty` pinned the `dataDir` field
  the task deletes.
- **Task 4 keeps `frames_collections.go`'s allowlist key.** The step said it
  "becomes empty" there, but the file still used `provider.Lookup` and
  `provider.ProviderTemplate` until Task 5, which deletes the key.
- **Review fixes.** `LoginProvider` codes an unavailable keychain the way
  `PrepareProviderLogin` and the MCP pair do, so a client sees the same sentence
  whichever call surfaces it; the seam's `ProbeProvider` comment records that a
  provider client may refresh its own cache while listing (OpenAI Codex), rather
  than claiming no cache is touched; `connect.enterOAuthLogin` fails with a note
  on a nil host instead of dereferencing it; and `TestLoginMCPComposesTheServerAndTheStore`
  runs the real `Session.LoginMCP` composition, which only `mcpEngine`'s direct
  test covered.
