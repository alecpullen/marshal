# P2 plan 7b: Config through the seam — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `341f1941` (plan 7a merged). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding declarations with exactly the tags given.
- `inproc` importing `app/presetsetup` and `llm/modelcatalog` creates no import cycle: neither package's dependencies, checked with `go list -deps` at the base, include `acpclient`, `tui`, `internal/app` or `viewmodel`.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C3 (secrets), C5 (status), C10 (config, providers and trust), Migration order item 7, its revision record entry "2026-11-05, against `341f1941` (plan 7a merged)", and tests T1, T1b, T4 and T5.
Previous plan: [plan 7a, providers and sign-ins](2026-11-05-p2-7a-providers-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI reads its config through the session, and writes and reloads config only through the seam. Preset setup runs agent side. The bridge reads a long session's frames.
- `acpwire` gains `PresetSetup`, and `SessionStatus` gains `UserConfigPath` and `ProjectConfigPath`.
- `acpclient.Host` gains six write calls and `acpclient.Session` gains seven methods: two reads, three reload calls and two project writes.
- `inproc.Deps` gains the runtime's reloader, layer reloader and trust refresh, and the home directory and user config path.
- The TUI's config options go: `WithConfigReloader`, `WithConfigLayers`, `WithLayerReloader`, `WithTrustRefresh` and `WithModelCache`. So do `connect.Opts.DataDir`, and the settings browser's and agents roster's layers and project paths.
- The bridge's frame cap rises to 64 MiB, and an oversized frame fails the child instead of wedging it.

**Non-goals:**
- **No redaction.** `Session.Config` and `Session.Layers` return literal keys, as the state does today. Plan 7d redacts them (decision 1).
- **No trust prompt, tool lookup or skill-gate settings.** Plan 7c.
- **The skills panel is untouched.** It reads and writes `p.state.Config` directly and goes with the rest of the panel in plan 8. So T1b does not gain `Config` yet.
- **No change to what users see.** Every note, receipt, error and saved file reads as it does today. Decision 11 lists the only behaviour differences.
- **No ACP behaviour change:** no handler is added or changed. The bridge change is internal to `web/bridge`. No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-7b-suite`. Create the directory first: `mkdir -p /private/tmp/p2-7b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** At `341f1941`, all 117 packages passed, `web/bridge` included. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **Where the TUI writes config.** In `model.go`: `persistAndReload` (`:882`), `/set`'s global branch (`handleSetCommand`, `:1030`), the verify commands at a run's start (`castlist.StartMsg`, `:2265`), `applyConnectDone` (`:6841`) and `switchModelPreset` (`:6984`). Also `handleModelOptionsChanged` and `savePresetsAndReload` (`model_options.go:78`, `:125`), `submitDoctorSecret` (`doctor_secret.go:100`), the settings browser's `flushChanges` (`settings/browser.go:608`), and the agents roster's `persistNow` and `maybePersist` (`agents/panel.go:696`, `:710`). The skills panel's autoload write (`skills/panel.go:160`) is plan 8's.
- **The panels' project saves are repeated at once.** The browser's non-global save and both roster saves write the project file, then send `settings.ChangedMsg`. The model's handler (`model.go:1855`) then calls `persistAndReload`, which saves the same config with the same layers again, and ignores the panel's `SaveErr`. This plan keeps both saves (decision 6).
- **Every reload installs the TUI's config.** `reloadAgentRuntime` (`app.go:2953`) builds from the config it is given rather than from disk. So does `RebaseInteractionConfig`, which `applyNewConfig` (`model.go:824`) calls after every save, failed or not. Only `reloadAgentRuntime` itself carries `SessionRouting` over.
- **Layers are refreshed in four places**, always through `reloadLayers` (`model.go:924`): after `persistAndReload`'s save, after `/set`'s and the browser's global saves (after their reload), and after `savePresetsAndReload`'s save. `adoptLayers` (`:938`) installs the result in the state, in the provenance pointer and in an open panel. The pointer and the state always hold the same snapshot, because both are set together at startup (`runtime.go:1785`, `app.go:2766`) and on every refresh.
- **The trust hash is refreshed in one reachable place:** `persistAndReload`, after its save. The `settings.ChangedMsg` handler's call guards on `!msg.GlobalTarget` inside the `msg.Saved` branch, and the browser sets `Saved` only on a global save, so it never runs.
- **Production always has a reloader, layers and a home.** `Run` wires all three (`app.go:2766` to `2783`). Only tests build a model without them. A model without a reloader skips the reload and the notice clearing that follows it. A model without layers shows no provenance line.
- **The tests reach these hooks often.** About 35 TUI tests assign `m.configReloader`, seven pass `WithConfigReloader`, and others set `m.layerReloader`, `m.configLayers` or `m.modelCacheDir`. `testenv.Run` points `MARSHAL_CONFIG_DIR` at a temporary directory, so in most tests the user config path does not depend on the home. Tests that blank it (`TestDoctorFixSavesKeyAndReloads`, `model_test.go:9127`, and others) expect the file under the home they passed to `WithHomeDir`.
- **`Update` has a value receiver** (`model.go:1603`). A command that runs off the Update goroutine captures the host, the session and the context values, never `m`.

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **Plan 7 gains a fourth plan: 7d, secrets.** Redaction needs rules of its own, and nothing else in 7b depends on it:
   - a write must keep a stored key it does not name, including through a provider rename;
   - a cleared key must stay cleared;
   - the settings browser shows a key's last four characters and "key stored";
   - probes of a saved provider need its stored key.

   So 7b moves every config read, write and reload with literal keys still in place, and 7d redacts `Config` and `Layers` behind it. 7c and 7d depend only on 7b, and touch different TUI files.
2. **Reads.** `Session.Config` returns the state's `ConfigSnapshot`, and `Session.Layers` returns the state's `Layers`. Both map to one new ACP method, `session/config`. Each TUI function reads `Config()` once, as plan 2a's rule does for `Status()`. A function that changes the config and then reads the baseline reads it once before the change.
3. **Writes keep their shape.** Each config save the TUI makes becomes one seam call that runs that save agent side, with the same arguments minus the path.
   - User-global writes are `Host` calls: `SaveUserValue`, `SaveUserProviders`, `SaveUserPresets`, `SaveProviderKey` and `SaveProvenance`.
   - Project writes are `Session` calls, because they follow the session's working directory and diff against its layers: `SaveProjectConfig` and `SaveVerifyCommands`.
   - The TUI keeps its own order of saves, reloads and notes, its `configSavePending` flag, and its busy check ("Stop the active turn before applying settings").
   - So the spec's "after writing, the adapter reloads … and returns the path written, the new config and its diagnostics" is replaced. A write does not reload, because a connect makes three writes before one reload, and the settings browser two. The spec's busy code is not added: the TUI's own check runs first, as today.
4. **Layers and trust follow the writes agent side.**
   - After any successful write, the adapter refreshes the current session's layers through `Deps.ReloadLayers`, keeping the old snapshot when the reload reports false, as `reloadLayers` did.
   - After a successful `SaveProjectConfig`, it also refreshes the trust hash through `Deps.TrustRefresh`, before the layers, as `persistAndReload` did.
   - So the TUI's `reloadLayers`, `adoptLayers` and trust refresh go. The panels' layer snapshots go too: they existed only for the panels' own project saves, which now use the session's layers. Those layers held the same value.
5. **Reloads are three session calls.**
   - `ApplyConfig` installs a config in the session without saving or rebuilding: `RebaseInteractionConfig`, the agent half of `applyNewConfig`. `applyNewConfig` keeps its TUI half (`configViewsChanged`).
   - `ReloadConfig` rebuilds the runtime from a config through `Deps.ReloadConfig`. It reports false, and does nothing, when the session has no reload path. The TUI's `configReloader != nil` checks become that report, so a model without a reloader still skips the notice clearing.
   - `ReconnectMCP` is the reload that follows an MCP sign-in. It clears the provider and config notices, then reloads the session's current config, and reports false without a reload path, before clearing anything. The order is `reconnectMCPServer`'s: a rebuild that fails sets a fresh notice, which must survive.
6. **The panels' duplicate project saves stay.** The settings browser and the agents roster still save the project file themselves before the model saves it again. Removing the first save would change when the browser's retry gesture retries. Each panel now holds the session for that save (`settings.WithSession`, and the roster's constructor), and saves nothing without one, as the roster did with an empty path. Their project-path parameters go.
7. **Paths come from the status.** `SessionStatus.UserConfigPath` and `ProjectConfigPath` name the files the writes change. The adapter fills them from `Deps.UserConfigPath` (or the home's user config) and the state's working directory. Either is empty when it cannot be found. The TUI uses them:
   - for the connect summary's "save to:" line;
   - for the paths in its notes;
   - for the settings browser's user config path, which gates its user-global writes.

   The TUI keeps its own `userHome` lookup as `relPath`'s base, and as the gate in `/set`'s global branch, `applyConnectDone`, the model options and `/doctor`, so every note and fallback reads as before. `WithHomeDir` stays for plan 8's panels.
8. **`SaveUserValue` takes `any`.** It carries what the TUI passes today: the result of `config.LookupPath`, a bool or a string list. C3's signature rule gains this one exception. P3 sends the value as JSON, and the agent converts it by the field's type.
9. **Preset setup is one host call, and provenance is opaque.**
   - `PreparePresets` runs what the wizard's `prepareCmd` ran after its capability probe. It loads the models.dev catalog when the config's consent allows, reads the provenance sidecar, and runs `presetsetup.Apply` on a copy of the config's presets.
   - It returns the presets, plus a `PresetSetup` with the updated sidecar as raw JSON, the names it manages, and the three counts.
   - The wizard keeps the probe, which already goes through the host, and folds its result into the reported models before the call.
   - `applyConnectDone` reads the managed names from the setup, and hands the raw sidecar back to `SaveProvenance`. An unreadable sidecar gives no raw value, so nothing is saved and every preset stays unmanaged, as today.
   - `PreparedMsg.ProvenanceErr` is deleted: nothing reads it.
10. **The data directory is unchanged.** `PreparePresets` reads the catalog and the sidecar from `Deps.ModelCacheDir` as given. `SaveProvenance` falls back to `config.DataDir` of the home when it is empty. That is exactly the wizard's `dataDir` and `applyConnectDone`'s fallback.
11. **Accepted behaviour differences.** No user-visible text changes, but:
    - three writes that did not refresh the layers now do: the panels' own project saves, `SaveVerifyCommands`, and a model-options change saved while busy. The panels' project saves also refresh the trust hash. In each case the layers become current sooner. The model's save followed the panel's at once and refreshed both anyway;
    - `/set`'s global branch and the browser's global save refresh the layers before the reload instead of after it. The reload reads no layers;
    - `flushPendingModelOptions` with no reload path treats the change as activated. With a nil reloader it panicked, which only a test could reach;
    - the `settings.ChangedMsg` handler's unreachable trust refresh is deleted.
12. **`SaveProviderKey` maps to the existing `config/set_provider_key`.** The adapter writes a credential-only update, as `/doctor` does today. ACP's handler writes the provider's full entry. P3 reconciles the two.
13. **The allowlist loses 21 entries across 6 files, and four keys go entirely.** The four are `connect/connect.go`, `doctor_secret.go`, `model_options.go` and `settings/browser.go`. T1b gains `Layers`, `SetLayers` and `RebaseInteractionConfig`. `Config` waits for plan 8, because the skills panel still reads `p.state.Config`. Tasks 3 to 7 list their entries.
14. **The new ACP methods are named:**
    - `session/config`, for `Config` and `Layers`;
    - `session/apply_config`, `session/reload_config` and `session/mcp_reconnect`;
    - `session/save_project_config` and `session/save_verify_commands`;
    - `config/set_value`, `config/save_providers`, `config/save_presets`, `config/prepare_presets` and `config/save_provenance`.

    `SaveUserProviders` and `SaveUserPresets` do not reuse `config/set_providers` and `config/set_presets`. Those merge entries and reject keys; the TUI's writes replace the section against a baseline, keys included.
15. **The bridge's frame fix rides along** (decided with the user, 2026-11-05). It is independent of the rest, so it is Task 1.

**Execution:**
- Branch first: `git switch -c p2-7b-config` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-7b-config: record the plan 7 revisions and plan 7b`. The main checkout holds other untracked files (`docs/daemon/p3-acp-transport.md`, `docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-7b-config: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire`)

A new file, `config.go`:

```go
// PresetSetup is what preset setup proposes after a connect, apart from the
// presets themselves (C10, plan 7b).
type PresetSetup struct {
	// Provenance is the updated models.dev provenance sidecar. It is opaque to
	// the client, which hands it back to SaveProvenance unchanged. It is nil
	// when the sidecar could not be read; then nothing may be saved.
	Provenance json.RawMessage `json:"provenance,omitempty"`
	// Managed names the presets the provenance manages, sorted. It is empty
	// when Provenance is nil.
	Managed  []string `json:"managed,omitempty"`
	Prepared int      `json:"prepared,omitempty"`
	Matched  int      `json:"matched,omitempty"`
	Fallback int      `json:"fallback,omitempty"`
}
```

`SessionStatus` (`status.go`) gains two fields, after `Notice`:

```go
	// UserConfigPath and ProjectConfigPath are the files the config writes
	// change: the user config, and the session's project config. Each is
	// empty when the agent cannot locate it.
	UserConfigPath    string `json:"userConfigPath,omitempty"`
	ProjectConfigPath string `json:"projectConfigPath,omitempty"`
```

Encodings:
- a full setup: `{"provenance":{"version":1,"presets":{}},"managed":["ollama/qwen2.5-coder:7b"],"prepared":2,"matched":1,"fallback":1}`;
- `PresetSetup{}`: `{}`;
- the two status fields: `"userConfigPath":"/home/u/.config/marshal/config.toml","projectConfigPath":"/repo/.marshal/config.toml"`.

### Interface (`internal/acpclient`)

`acpclient.go` imports `encoding/json` and `llm/routing` too. `Host` gains, after `LoginProvider`:

```go
	// SaveUserValue writes value at the dotted TOML path tomlPath of the user
	// config, leaving the rest of the file alone. value is a bool, string,
	// integer or string list, or what config.LookupPath returns for one.
	//
	// ACP: config/set_value (new in P3). The value travels as JSON, and the
	// agent converts it by the field's type.
	SaveUserValue(ctx context.Context, tomlPath string, value any) error

	// SaveUserProviders writes the user config's [providers] section,
	// credentials included. Entries on disk that are in neither providers nor
	// baseline survive: another process wrote them after baseline was taken.
	//
	// ACP: config/save_providers (new in P3).
	SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig) error

	// SaveUserPresets writes the user config's [models.presets] section,
	// merged against baseline as SaveUserProviders is.
	//
	// ACP: config/save_presets (new in P3).
	SaveUserPresets(ctx context.Context, presets, baseline map[string]routing.ModelPreset) error

	// SaveProviderKey stores key as the literal API key of the provider called
	// name in the user config, keeping the entry's other fields.
	//
	// ACP: config/set_provider_key (exists). Its handler writes the provider's
	// full entry; this call writes only the credential.
	SaveProviderKey(ctx context.Context, name, key string) error

	// PreparePresets proposes presets for the models a connected provider
	// reported: cfg's presets with every reported model's preset created or
	// refreshed from the models.dev catalog (read only when cfg's consent
	// allows) and the provenance sidecar. cfg is not changed.
	//
	// ACP: config/prepare_presets (new in P3).
	PreparePresets(ctx context.Context, cfg config.Config, provider, baseURL string, models []schema.ModelInfo) (map[string]routing.ModelPreset, acpwire.PresetSetup, error)

	// SaveProvenance writes the provenance sidecar PreparePresets returned.
	// A nil or empty provenance writes nothing.
	//
	// ACP: config/save_provenance (new in P3).
	SaveProvenance(ctx context.Context, provenance json.RawMessage) error
```

`Session` gains, after `LoginMCP`:

```go
	// Config is the session's live config. Until plan 7d redacts them, it
	// carries literal keys.
	//
	// ACP: session/config (new in P3); refreshed by a session_status update.
	Config() config.Config

	// Layers is the session's config layers, with provenance: the snapshot
	// the last write or startup loaded. The zero value means none was loaded.
	//
	// ACP: session/config (new in P3).
	Layers() config.Layers

	// ApplyConfig installs cfg as the session's config without saving it or
	// rebuilding the runtime. A manual mode selection survives, as a reload's
	// does.
	//
	// ACP: session/apply_config (new in P3).
	ApplyConfig(ctx context.Context, cfg config.Config) error

	// ReloadConfig rebuilds the runtime from cfg. It reports false, and does
	// nothing, when the session has no reload path; the error is the
	// rebuild's.
	//
	// ACP: session/reload_config (new in P3).
	ReloadConfig(ctx context.Context, cfg config.Config) (bool, error)

	// ReconnectMCP rebuilds the runtime from the session's current config, so
	// an MCP server that has just been signed in to connects. It clears the
	// provider and config notices first, so a fresh notice the rebuild sets
	// survives. It reports false, and does nothing, when the session has no
	// reload path.
	//
	// ACP: session/mcp_reconnect (new in P3).
	ReconnectMCP(ctx context.Context) (bool, error)

	// SaveProjectConfig writes cfg's project-scoped values to the session's
	// project config, diffed against the session's layers, so that no
	// user-global value is baked into the project file.
	//
	// ACP: session/save_project_config (new in P3).
	SaveProjectConfig(ctx context.Context, cfg config.Config) error

	// SaveVerifyCommands writes the plan run's verify commands to the
	// session's project config.
	//
	// ACP: session/save_verify_commands (new in P3).
	SaveVerifyCommands(ctx context.Context, build, test string) error
```

`Methods` (`methods.go`) gains thirteen rows. The six `Host` rows go after `LoginProvider`'s, and the seven `Session` rows at the end, in declaration order:

| Iface | Name | ACP | Exists |
|---|---|---|---|
| Host | SaveUserValue | config/set_value | false |
| Host | SaveUserProviders | config/save_providers | false |
| Host | SaveUserPresets | config/save_presets | false |
| Host | SaveProviderKey | config/set_provider_key | true |
| Host | PreparePresets | config/prepare_presets | false |
| Host | SaveProvenance | config/save_provenance | false |
| Session | Config | session/config | false |
| Session | Layers | session/config | false |
| Session | ApplyConfig | session/apply_config | false |
| Session | ReloadConfig | session/reload_config | false |
| Session | ReconnectMCP | session/mcp_reconnect | false |
| Session | SaveProjectConfig | session/save_project_config | false |
| Session | SaveVerifyCommands | session/save_verify_commands | false |

### Adapter (`internal/acpclient/inproc`)

`Deps` gains, after `ModelCacheDir`:

```go
	// ReloadConfig is the runtime's config reloader: it rebuilds the runtime
	// from cfg. Nil means the session has no reload path: ReloadConfig and
	// ReconnectMCP report false and change nothing.
	ReloadConfig func(cfg config.Config) error
	// ReloadLayers re-reads the config layers from disk. False means the read
	// failed, and the session keeps the layers it has. Nil means a write
	// refreshes no layers.
	ReloadLayers func() (config.Layers, bool)
	// TrustRefresh advances the permanent-trust record's config hash for dir
	// after a project save. Nil means no hash is refreshed.
	TrustRefresh func(dir string)
	// HomeDir is the home directory the user config is found under. Empty
	// means os.UserHomeDir().
	HomeDir string
	// UserConfigPath is the user config file. Empty means
	// config.UserConfigPath of the home directory.
	UserConfigPath string
```

`ModelCacheDir`'s doc gains: it is also where preset setup reads the models.dev catalog and the provenance sidecar.

A new file, `config.go`, holds every method above and these unexported helpers, named so that the steps can refer to them:
- `userConfigPath(d Deps) (string, error)`: `d.UserConfigPath` when set; otherwise `config.UserConfigPath` of `d.HomeDir`, or of `os.UserHomeDir()` when that is empty. A home that cannot be found is the error `locate home directory: <err>`, the TUI's own wording (`model.go:871`).
- `refreshLayers(state *session.State, reload func() (config.Layers, bool))`: does nothing when either is nil. Otherwise it installs the reloaded layers with `SetLayers` when the reload reports true.

`Session` keeps the five new `Deps` values as fields, set in `newSession` (`events.go:20`) as the other `Deps` values are.

### TUI

- `tui.Model` gains `Session() acpclient.Session`, beside `Host()` (`model.go:682`), for `app`'s wiring test.
- `settings.WithSession(sess acpclient.Session) BrowserOption` sets the session the browser's project save goes through.
- `settings.NewBrowser(cfg config.Config, query string, opts ...BrowserOption)` loses its `cfgPath` parameter.
- `agents.NewRosterPanel(cfg config.Config, sess acpclient.Session, arg string, dispatch DispatchFn)` and `agents.NewRosterPanelWithRegistry(cfg config.Config, sess acpclient.Session, arg string, dispatch DispatchFn, reg *registry.Registry)` take the session in place of `cfgPath`. A nil session saves nothing.
- `connect.DoneMsg.Setup acpwire.PresetSetup` replaces `DoneMsg.Provenance`.
- `connect.PreparedMsg.Setup acpwire.PresetSetup` replaces `PreparedMsg.Provenance`, `ProvenanceErr` and `Result`.

**Deleted:**
- `tui.ConfigReloader`, `WithConfigReloader`, `WithConfigLayers`, `WithLayerReloader`, `WithTrustRefresh` and `WithModelCache`;
- the model's `configReloader`, `configLayers`, `layerReloader`, `trustRefresh` and `modelCacheDir` fields;
- `reloadLayers`, `adoptLayers` and `projectConfigPath`;
- the browser's and roster's `SetLayers`, `Layers` and `layers` fields;
- the browser's `cfgPath` field and its `os.UserHomeDir` fallback in `NewBrowser`;
- `connect.Opts.DataDir` and the wizard's `dataDir` field.

### Pinned strings

- Bridge: the error delivered to pending requests on a scanner error starts `bridge: read child output: ` and wraps the scanner's error.
- The TUI's notes keep their text. The tests that pin them today keep passing unchanged.

## Task 1: The bridge reads large frames

**Goal:** a child frame up to 64 MiB reaches its caller. A larger one fails the child cleanly instead of wedging the connection.

**Files:** `web/bridge/child.go`, `web/bridge/child_test.go`.

**Steps:**

1. **The cap.** `maxFrameBytes` (`child.go:23`) becomes a variable, 64 MiB. Rewrite its comment:
   - it bounds one inbound frame from the child;
   - a long session's `session/stack` result or first `stack_patch` passes 2 MiB (2,000 calls measured about 2.67 MB), so the cap sits well above that and matches P3's pipe (`p3-acp-transport.md` R2);
   - it is a variable so tests can shrink it, as `restartBackoff` is.
2. **The failure.** In `readLoop` (`:442`), after the scan loop ends, check the scanner's error.
   - If there is one and the Child is not stopping (`c.stopping`, read under `c.mu`):
     - fail every pending request with `failPending`, with an error that starts `bridge: read child output: ` and wraps the scanner's error;
     - then end the current generation with `killCurrent`.
   - `supervise`'s `Wait` then returns, and it respawns the child and calls `OnRestart`, as for any exit.
   - When stopping, return as today: `Stop` and `Detach` close the pipe themselves, and their pending requests get `errStopped`.
   - Update `readLoop`'s doc comment to match.
3. **The helper.** In `runHelperChild` (`child_test.go:53`), a request whose method is `test/big` is answered, in any mode, with a result string of `params.bytes` `x` characters.
4. **Tests:**
   - `TestChildReadsFramesAboveOneMiB`: a "normal" child answers `test/big` with 3 MiB, and the result arrives whole.
   - `TestChildOversizedFrameFailsTheChild`:
     - set `maxFrameBytes` to 1 MiB and restore it afterwards, as `TestChildStopDuringRestartReturns` does with `restartBackoff`;
     - start a "normal" child with an `OnRestart` that signals a channel;
     - ask `test/big` for 2 MiB. The request returns within five seconds with an error for which `errors.Is(err, bufio.ErrTooLong)` holds;
     - `OnRestart` runs, and a following `ping` request succeeds, as `TestChildRestartOnExit` checks.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-7b-suite go test ./web/bridge/ -run 'TestChild' -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1
```

Expected: both pass. Mutation check: with step 2 reverted, `TestChildOversizedFrameFailsTheChild` fails on its deadline. With the cap back at 1 MiB, `TestChildReadsFramesAboveOneMiB` fails. Restore both.

## Task 2: The seam carries config

**Goal:** the wire types, the thirteen methods and the adapter exist, and `Run` wires the adapter's new `Deps`. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/config.go` (new), `internal/acpwire/status.go`;
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/config.go` (new), `internal/acpclient/inproc/config_test.go` (new), `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`;
- `internal/app/app.go`, `internal/app/app_test.go`;
- `internal/app/tui/model.go` (the `Session` accessor only).

**Steps:**

1. **Wire and interface.** Add the declarations in Contracts: `PresetSetup`, the two status fields, the thirteen methods with their doc comments, and their `Methods` rows.
2. **`Deps` and the session.** Add the five `Deps` fields and extend `ModelCacheDir`'s doc. `newSession` copies them onto the session.
3. **Status.**
   - `Session.Status` (`inproc.go:274`) fills `UserConfigPath` from `userConfigPath`, empty on its error.
   - It fills `ProjectConfigPath` with `config.ProjectConfigPath` of the state's working directory, empty when that is empty.
4. **Reads.** `Config` returns `ConfigSnapshot()` and `Layers` returns `Layers()`. A closed session keeps answering both, as `Status` does.
5. **Reload calls.** Each call fails with the closed-session error on a closed session, as the other calls do.
   - `ApplyConfig` calls the state's `RebaseInteractionConfig` and returns its error.
   - `ReloadConfig` returns false and no error without `Deps.ReloadConfig`. Otherwise it calls it with cfg and returns true and its error.
   - `ReconnectMCP` returns false, and clears nothing, without a reloader. Otherwise:
     - it clears the provider and the config notices, as the session's `ClearNotice` does for each;
     - it calls the reloader with the state's `ConfigSnapshot()`;
     - it returns true and the reloader's error.

     Its doc comment carries `reconnectMCPServer`'s reason for the order (`mcp.go:61` to `65`): a rebuild that fails sets a fresh notice, which clearing afterwards would wipe.
6. **Project writes.** Both fail with the closed-session error when closed. The path is `config.ProjectConfigPath` of the state's working directory.
   - `SaveProjectConfig` calls `config.SaveProjectConfig(path, cfg, state.Layers())`. On success it calls `Deps.TrustRefresh` with the working directory when set, then `refreshLayers`.
   - `SaveVerifyCommands` calls `config.SaveVerifyCommands(path, build, test)`. On success it calls `refreshLayers` only.
7. **User writes.** Each resolves the path with `userConfigPath` and returns its error unchanged. Each calls the matching `config` function, then `refreshLayers` on the host's current state (`currentState`, `providers.go:17`) on success:
   - `SaveUserValue` calls `config.SaveUserConfigValue(path, tomlPath, value)`;
   - `SaveUserProviders` calls `config.SaveUserConfigProviders(path, providers, baseline)`, with exactly one baseline argument, as every TUI caller passes one;
   - `SaveUserPresets` calls `config.SaveUserConfigPresets(path, presets, baseline)` the same way;
   - `SaveProviderKey` calls `config.SaveUserConfigProviderAPIKey(path, name, config.ProviderConfig{APIKey: key})`, the credential-only update `/doctor` makes today.
8. **Preset setup.** `PreparePresets` follows `connect.prepareCmd` (`connect/connect.go:600`) from its catalog load on.
   - Copy cfg's preset map before anything changes it, as `prepareCmd` does.
   - Load the catalog with `modelcatalog.NewCache(dir).Load` under a 30-second timeout, where `dir` is `Deps.ModelCacheDir`. Load it only when `cfg.Privacy.ModelsDevEnrichment` is set and true. Ignore its error, as today.
   - Read the sidecar with `presetsetup.LoadProvenance(dir)`. On an error, use a nil provenance.
   - Run `presetsetup.Apply` on the copy.
   - Return the copy's presets and a `PresetSetup`. With a provenance, it carries the provenance's JSON as `Provenance` and its preset names, sorted, as `Managed`. In every case it carries the result's three counts. The error is always nil in-process.
9. **Provenance.** `SaveProvenance` does nothing for a nil or empty value.
   - Otherwise it decodes the value into a `presetsetup.Provenance` and returns a decoding error as is.
   - It saves with `presetsetup.SaveProvenance(dir, &p)`, where `dir` is `Deps.ModelCacheDir`. When that is empty, `dir` is `config.DataDir` of the home; a home that cannot be found is `userConfigPath`'s error.
10. **`tui.Model.Session`** returns the model's session.
11. **`Run`** (`app.go:2735`) sets the new `Deps`:
    - `ReloadConfig` is `configReloader`;
    - `ReloadLayers` is `layerReloaderFor(homeDir, workingDir, state.Trusted)`, the value `WithLayerReloader` gets;
    - `TrustRefresh` is `trustRefresh`;
    - `HomeDir` is `homeDir`.

    The TUI options stay wired until Tasks 3 to 7 delete them.
12. **Adapter tests** (`config_test.go`). Each builds `inproc.New` over a `session.New` state with a temporary working directory:
    - `TestConfigReadsTheSessionsConfigAndLayers`: `Config` and `Layers` return what the state holds, and keep answering after the session closes.
    - `TestStatusNamesTheConfigFiles`:
      - with `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME` blanked and `Deps.HomeDir` set to `h`, `UserConfigPath` is `config.UserConfigPath(h)`, and `ProjectConfigPath` is the working directory's `.marshal/config.toml`;
      - with `Deps.UserConfigPath` set, that path wins.
    - `TestSaveProjectConfigRefreshesTrustThenLayers`:
      - with `ReloadLayers` returning a marked snapshot and `TrustRefresh` recording its calls, a save writes the project file, calls `TrustRefresh` with the working directory, and leaves the marked snapshot in `Layers`, in that order;
      - a working directory under a regular file makes the save fail, and neither hook runs.
    - `TestSaveVerifyCommandsRefreshesOnlyLayers`: the file gains the commands, the layers are refreshed, and `TrustRefresh` is not called.
    - `TestUserWritesUseTheUserConfigPathAndRefreshLayers`, with `Deps.UserConfigPath` a temporary file:
      - `SaveUserValue` writes its key;
      - `SaveUserProviders` keeps an entry that is on disk but in neither map;
      - `SaveUserPresets` writes a preset;
      - `SaveProviderKey` keeps the entry's base URL and stores the key;
      - each refreshes the session's layers.
    - `TestAFailedLayerReloadKeepsTheLayers`: a `ReloadLayers` that reports false leaves the previous snapshot in place after a write.
    - `TestReloadConfigReportsTheReloadPath`: without a reloader it returns false, and the config is unchanged. With one, it passes cfg and returns true and the reloader's error.
    - `TestApplyConfigInstallsWithoutSaving`: the state's config changes and no file is written.
    - `TestReconnectMCPClearsNoticesBeforeTheReload`:
      - with a provider notice set, the reloader sees no notice when called, and sets a fresh config notice that is still there afterwards;
      - without a reloader, the call returns false and the notice stays.
    - `TestPreparePresetsRunsPresetSetup`:
      - with consent unset and no catalog, two reported models give two presets, the setup's `Managed` names both, its raw provenance decodes as a `presetsetup.Provenance`, and the given cfg's map is unchanged;
      - a corrupt sidecar file gives a nil `Provenance` and an empty `Managed`.
    - `TestSaveProvenanceWritesTheSidecar`: the raw value lands at `presetsetup.ProvenancePath(dir)`, and nil writes nothing.
    - `TestClosedSessionConfigCallsFail`: after a swap, the five session calls return the closed-session error.
13. **`app` test.** `TestRunWiresTheConfigSeam`, built like `TestRunPassesAuthResolverToTheSeam` (`app_test.go:6168`), with the trust resolver answering permanent. Before `Run`, seed a permanent trust record for the working directory with a stale hash, through `trust.NewStore(config.DataDir(home)).SetTrust`. Inside the program runner, through `model.(tui.Model).Session()`:
    - `Status().UserConfigPath` is `config.UserConfigPath(home)`;
    - `ReloadConfig` with `Config()` returns true. Its error is not checked: the fixture has no provider;
    - `SaveVerifyCommands("go build ./...", "go test ./...")` succeeds, and `Layers().Merged` then carries them as its `[sdd.verify]` build and test, which proves `ReloadLayers` is wired;
    - `SaveProjectConfig(ctx, Config())` succeeds, and the store's `StoredConfigHash` for the directory then equals `trust.ConfigHashFor(dir)`, which proves `TrustRefresh` is wired.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/... ./internal/app/
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/ -run 'TestRunWiresTheConfigSeam|TestRunPassesAuthResolverToTheSeam' -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass. `TestWireFieldsFollowTheRules`, `TestMethodTableCoversTheInterfaces`, `TestMethodDocsNameTheirACPMethod` and `TestClientImportsStayOnTheWireSide` pass with the new declarations. Mutation check: drop the `ReloadLayers` line from `Run`'s `Deps`, and `TestRunWiresTheConfigSeam` fails. Restore it.

## Task 3: Reads go through the session

**Goal:** every TUI read of the config and its layers goes through the session, apart from the skills panel's.

**Files:**
- `internal/app/tui/model.go`, `model_options.go`, `doctor_secret.go`, `mcp.go`, `status.go`, `view.go`, `selection.go`, `history.go`, `sdd_scaffold.go`, `commands_dispatch.go`;
- `internal/app/tui/model_test.go`, `layers_refresh_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **The reads.** Every `m.state.Config` read becomes a read of `m.sess.Config()`, one per function (decision 2), in the files above. That includes `New`'s reads of its local `state.Config` (`model.go:1272`, `:1308`, `:1349`); `New` reads `m.sess.Config()` once after the options run.
   - A function that copies the config and later reads the baseline, such as `applyConnectDone`, `switchModelPreset` and `handleModelOptionsChanged`, reads it once at the top and uses that value for both.
   - Rewrite every comment that names `m.state.Config` or `state.Config` to name the session's config. These are at `model.go:288`, `:1115` and `:6931` to `6933`, and `model_options.go:88` to `89`.
   - `skills/panel.go` is untouched.
2. **Layers.**
   - `refreshDiagnostics` (`model.go:848`) and the doctor's diagnose (`commands_dispatch.go:316`) call `config.Diagnose` with `m.sess.Config()` and `m.sess.Layers()`.
   - `persistAndReload`'s save keeps passing the state's layers until Task 5, read through `m.sess.Layers()`.
   - `openSettingsBrowser` and `openAgentsRoster` pass `m.sess.Layers()` to `SetLayers` until Task 4.
3. **Provenance.** `provenanceForPath` (`model.go:1086`) reads `m.sess.Layers()`.
   - It returns "" when the snapshot is the zero value (`reflect.ValueOf(layers).IsZero()`), exactly when a model without `WithConfigLayers` returned "".
   - Delete `WithConfigLayers`, the `configLayers` field and `adoptLayers`' write through it.
   - `Run` stops passing it, and the `configLayers` variable at `app.go:2765` goes.
   - `adoptLayers` keeps setting the state's layers and the open panel's until Task 5.
4. **Tests.** A test that set `m.configLayers` sets the state's layers with `SetLayers` instead, and checks `m.sess.Layers()`. That covers `TestLayerReloaderFailureDoesNotOverwriteConfigLayers` (`model_test.go:5672`) and `layers_refresh_test.go`. Tests keep writing `m.state.Config` to arrange their fixtures: T1b reads non-test files only.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'state\.Config\b\|state\.Layers()' internal/app/tui --include='*.go' | grep -v '_test.go' | grep -v '^internal/app/tui/skills/'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/ -count=1
```

Expected: the grep prints nothing. The packages pass. The allowlist is unchanged: no file loses an agent-side import yet.

## Task 4: The panels save through the seam

**Goal:** the settings browser and the agents roster write config only through the host and the session.

**Files:**
- `internal/app/tui/settings/browser.go` and its tests;
- `internal/app/tui/agents/panel.go`, `internal/app/tui/agents/panel_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/model_test.go`;
- `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **The browser's options.**
   - Add `WithSession`.
   - `NewBrowser` loses `cfgPath` and its `os.UserHomeDir` fallback, and the `cfgPath` field goes. `WithUserConfigPath` is the only source of `userConfigPath`; rewrite its comment, which names the fallback.
   - Delete the browser's `SetLayers`, `Layers` and `layers` field.
2. **The browser's writes** (`flushChanges`, `browser.go:608`). Every branch, check and receipt stays; only the calls change.
   - **No host.** The browser reads the host from `b.reg.st.host`. It treats a nil host as an empty `userConfigPath` wherever it checks that path, so without one a global row falls through to the project save and a provider or preset change fails with today's `user config path is unavailable` message.
   - **The global branch.** `config.SaveUserConfigValue` becomes the host's `SaveUserValue`, with `context.Background()`.
   - **Providers and presets.** `SaveUserConfigProviders` and `SaveUserConfigPresets` become `SaveUserProviders` and `SaveUserPresets`, with the same maps and baselines.
   - **The project save** becomes the session's `SaveProjectConfig` with `newCfg`. Without a session, nothing is saved and the save counts as succeeding, as the roster's empty path did.
3. **The roster.** The constructors take the session in place of `cfgPath`.
   - `persistNow` and `maybePersist` (`panel.go:696`, `:710`) capture the session when the command is built. Inside the command they call its `SaveProjectConfig` with `context.Background()`, and put the error in `SaveErr`.
   - A nil session emits the `ChangedMsg` without saving, as the empty path did.
   - Delete `SetLayers`, `Layers` and the `layers` field.
4. **The model.**
   - `openSettingsBrowser` (`model.go:1101`) passes `WithSession(m.sess)`, and `WithUserConfigPath(st.UserConfigPath)` whenever that path is not empty. That replaces the `userHome` lookup there.
   - `openAgentsRoster` (`model.go:1127`) passes `m.sess`.
   - Both stop calling `SetLayers`. `adoptLayers` stops handing layers to an open panel; the interface check and its comment go.
5. **Test helpers.**
   - In `settings`, add `testSeam(t, workDir, userConfigPath string) (acpclient.Host, acpclient.Session)`. It builds `inproc.New` over a `session.New` state in `workDir`, with `Deps.UserConfigPath` and a `memAuth`-style resolver as `testHost` uses.
   - In `agents`, add `testSession(t, workDir)` the same way.
6. **Test migrations.**
   - Every `NewBrowser` call drops its path argument.
   - A browser test that checks or blocks the project file passes `WithSession` from `testSeam` over the directory it used. A blocked save puts the working directory under a regular file, so the project directory cannot be made. The tests at `browser_test.go:76`, `:140` and `:216` are three of them.
   - A test that checks the user file keeps `WithUserConfigPath` and adds `WithHost` from `testSeam` with the same path.
   - Every roster constructor call passes nil for the path, or a `testSession` when the test checks the file (`panel_test.go:203`, `:308` and `:754`). A file checked under `dir/.marshal/config.toml` is now found there through the session's working directory `dir`.
   - TUI tests that build either panel directly migrate the same way.
7. **Allowlist.** `settings/browser.go`'s key goes: all five entries. `agents/panel.go` loses `config.SaveProjectConfig`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/...
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/settings/ ./internal/app/tui/agents/ -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/ -count=1
```

Expected: all pass, `TestTUIStaysOnTheClientSeam` included. Mutation check: make the browser's project save skip the session call, and the migrated test at `browser_test.go:76` fails. Restore it.

## Task 5: The model saves through the seam

**Goal:** the model writes no config file. Layers and the trust hash are refreshed agent side.

**Files:**
- `internal/app/tui/model.go`, `model_options.go`, `doctor_secret.go`;
- `internal/app/tui/model_test.go`, `model_options_test.go`, `layers_refresh_test.go`, `doctor_secret_test.go`, `provider_error_test.go`;
- `internal/app/tui/moved_reads_test.go`, `internal/app/tui/boundary_allowlist_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **`persistAndReload`** (`model.go:882`). The save becomes `m.sess.SaveProjectConfig(m.ctx, cfg)`. The failure path, `configSavePending` and the reload are unchanged. The trust refresh and `reloadLayers` after it go: the adapter now does both (decision 4).
2. **`/set`'s global branch** (`model.go:1030`).
   - Keep the `userHome` gate and its fallback to `persistAndReload`.
   - The save becomes `m.host.SaveUserValue(m.ctx, tomlPath, value)`, and the `reloadLayers` call after the reload goes.
   - The two success notes use `relPath(home, st.UserConfigPath)`, from a `Status()` read after the save.
3. **`reportSetResult`** (`model.go:1066`) uses `st.ProjectConfigPath` where it called `projectConfigPath`.
4. **The `settings.ChangedMsg` handler** (`model.go:1855`).
   - In the `Saved` branch, the trust refresh and `reloadLayers` go.
   - The notes' paths come from the status: `ProjectConfigPath`, or `UserConfigPath` with the home as base when `GlobalTarget` is set, as today.
   - The global-failure branch's path is `st.UserConfigPath`. Today's lookup gave "" when the home could not be found, and so does this.
5. **The verify commands** (`castlist.StartMsg`, `model.go:2265`) become `m.sess.SaveVerifyCommands`. The success note names `relPath(st.WorkingDir, st.ProjectConfigPath)`.
6. **`applyConnectDone`** (`model.go:6841`). Its three saves become the host's:
   - `SaveUserProviders` and `SaveUserPresets`, with today's baselines from the config read once at the top;
   - `SaveUserValue` for `privacy.models_dev_enrichment`, with `*msg.EnrichmentConsent`.

   The `userHome` gate stays and `userPath` goes. The provenance save stays as it is until Task 7.
7. **`switchModelPreset`** (`model.go:6984`). Its presets save becomes `SaveUserPresets`.
8. **Model options** (`model_options.go`).
   - `savePresetsAndReload` loses its path parameter. Its save becomes `SaveUserPresets`, and its `reloadLayers` call goes.
   - `handleModelOptionsChanged` keeps its `userHome` gate, and its busy-path save becomes `SaveUserPresets` too.
9. **`/doctor`'s key** (`doctor_secret.go:100`). The save becomes `m.host.SaveProviderKey(m.ctx, provider, value)`, under the same `userHome` gate and the same notes.
10. **Deletions.**
    - Delete `reloadLayers`, `adoptLayers`, `projectConfigPath`, `WithLayerReloader`, `WithTrustRefresh` and the `layerReloader` and `trustRefresh` fields, with their comments.
    - `Run` stops passing both options. The `Deps` from Task 2 now carry them alone.
    - `relPath` stays.
11. **Test helpers** (`model_test.go`).
    - Add `newTestModelDeps(t, edit func(*inproc.Deps)) Model`. It builds what `newTestModel` (`:6560`) builds, but calls `edit` on the `Deps` before the adapter is built, when `edit` is not nil.
    - `newTestModel(t)` becomes `newTestModelDeps(t, nil)`.
    - Both, and `newTestModelInRepo`, use one temporary home, as both `Deps.HomeDir` and `WithHomeDir`.
    - A test that builds with `testModelWith` and passes `WithHomeDir(h)` also sets `Deps.HomeDir` to `h`. `TestDoctorFixSavesKeyAndReloads` (`:9127`) is one.
12. **Test migrations.**
    - A test that set `m.layerReloader` passes `Deps.ReloadLayers` through `newTestModelDeps` instead. A closure that needs the state reads `d.State` inside `edit`.
    - `TestReloadLayersRefreshesOpenDockPanel` is deleted: no panel holds layers now, and Task 2's adapter tests cover the refresh.
    - `TestReloadLayersNoReloaderIsNoop` becomes a check that a `/set` without `Deps.ReloadLayers` leaves the state's layers unchanged.
    - `TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues` keeps its end-to-end check, with `Deps.ReloadLayers` reading the temporary home's layers.
13. **T1b.** `movedStateReads` gains `Layers` and `SetLayers` at plan `7b`. Add a comment: the model reads the layers through the session, and the adapter refreshes them after every write.
14. **Allowlist.**
    - `model.go` loses `config.ProjectConfigPath`, `config.SaveProjectConfig`, `config.SaveUserConfigPresets`, `config.SaveUserConfigProviders`, `config.SaveUserConfigValue`, `config.SaveVerifyCommands` and `config.UserConfigPath`. `config.DataDir` and `presetsetup` stay until Task 7.
    - `model_options.go`'s key goes, and so does `doctor_secret.go`'s.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'config\.Save\|config\.UserConfigPath\|config\.ProjectConfigPath' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints only `skills/panel.go`'s lines and comments;
- the packages pass, `TestMovedStateReadsStayMoved` included.

Mutation check: make `SaveUserValue` skip `refreshLayers`, and `TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues` fails: the project save then diffs against the user layer from before the global save. Restore it.

## Task 6: Reloads go through the session

**Goal:** the TUI installs and reloads config only through the session.

**Files:**
- `internal/app/tui/model.go`, `model_options.go`, `mcp.go`;
- `internal/app/tui/model_test.go`, `mcp_test.go`, `model_options_test.go`, `provider_error_test.go`;
- `internal/app/tui/moved_reads_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **`applyNewConfig`** (`model.go:824`) calls `m.sess.ApplyConfig(m.ctx, cfg)`, ignoring its error as `RebaseInteractionConfig`'s was ignored. It then calls `configViewsChanged` as today.
2. **Every `m.configReloader` use** becomes `m.sess.ReloadConfig(m.ctx, cfg)`. That covers `persistAndReload`, `/set`'s global branch, the `settings.ChangedMsg` handler's `Saved` branch, and `savePresetsAndReload`.
   - Where the code checked `m.configReloader != nil`, it now acts on the result: an error takes today's error path; a true result with no error runs `afterRuntimeReload` as today; false skips both.
   - The `m.setReg = nil` before each reload may run whatever the result: `applyNewConfig` clears it anyway.
3. **`flushPendingModelOptions`** (`model_options.go:158`) calls `ReloadConfig` too. An error keeps the pending change for a retry, as today. Otherwise the change is activated, including when no reload ran (decision 11).
4. **`reconnectMCPServer`** (`mcp.go:54`).
   - Read `cfg := m.sess.Config()`, then call `m.sess.ReconnectMCP(m.ctx)`. The notice clearing moves into that call.
   - A false result returns at once, as the nil reloader did.
   - Otherwise set `m.setReg` to nil. On an error, `applyNewConfig(cfg)` and today's note; on success, `applyNewConfig(cfg)` and `refreshDiagnostics`.
   - Keep the function's comment about why the rebuild is needed. Delete its notice-ordering comment: the adapter's `ReconnectMCP` carries it (Task 2).
5. **Deletions.** Delete `ConfigReloader`, `WithConfigReloader` and the `configReloader` field. `Run` stops passing it.
6. **Test migrations.**
   - A test that set `m.configReloader`, or passed `WithConfigReloader`, passes `Deps.ReloadConfig` instead. Use `newTestModelDeps`, or the `Deps` it already gives `testModelWith`.
   - A test that changes the reloader partway through gives `Deps.ReloadConfig` a function that calls through a variable the test sets.
   - `m.configReloader = nil` (`mcp_test.go:319`) becomes no reloader.
   - A closure that wrote `m.state.Config` writes the state from `d.State`.
7. **T1b.** `movedStateReads` gains `RebaseInteractionConfig` at plan `7b`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'configReloader\|ConfigReloader' internal/app/tui --include='*.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass. `TestMCPAuthDoneReconnectsRuntime`, `TestReconnectKeepsAFreshMCPFailureNotice`, `TestReconnectMCPServerWithoutReloaderIsSafe` and the provider-notice tests (`provider_error_test.go`) are among them.

Mutation check: make `ReconnectMCP` clear the notices after the reload, and `TestReconnectKeepsAFreshMCPFailureNotice` fails. Restore it.

## Task 7: Preset setup runs agent side

**Goal:** the connect wizard and the model load no catalog and touch no provenance file.

**Files:**
- `internal/app/tui/connect/connect.go`, `internal/app/tui/connect/connect_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/model_test.go`;
- `internal/app/tui/boundary_allowlist_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **`prepareCmd`** (`connect.go:600`).
   - Keep the copy of the presets, the capture of the values the command needs, and the capability probe with its folding into the reported models.
   - Replace the catalog load, the sidecar read and `Apply` with `host.PreparePresets(ctx, cfg, providerName, baseURL, models)`, under a 30-second timeout.
   - Return a `PreparedMsg` carrying its presets, the setup and the probe's caps.
   - On an error, the message carries the copied presets and a zero setup.
   - With no host, it carries the copy and a zero setup without calling anything, as the probe is skipped today.
2. **The messages.** `PreparedMsg` and `DoneMsg` take `Setup` in place of their provenance and result fields (Contracts).
   - The summary (`connect.go:715` to `718`) reads the counts from `m.prepared.Setup`.
   - The done message (`:1357` to `1358`) passes `m.prepared.Setup`.
3. **Options.**
   - Delete `Opts.DataDir`, its comment and the `dataDir` field.
   - The model's `openConnect` and `openModels` (`model.go:6335`, `:6356`) stop passing it.
   - `openConnect` sets `CfgPath` from `m.sess.Status().UserConfigPath`, in place of its `userHome` lookup.
4. **`applyConnectDone`** (`model.go:6841`).
   - The managed set comes from `msg.Setup.Managed`.
   - The provenance save becomes `m.host.SaveProvenance(m.ctx, msg.Setup.Provenance)` when that is not empty, with today's warning on its error. The `dataDir` fallback goes, since the adapter falls back the same way (decision 10).
5. **Deletions.** Delete `WithModelCache` and the `modelCacheDir` field. `Run` stops passing it.
6. **Test migrations.**
   - A connect test that set `Opts.DataDir` builds its host with `Deps.ModelCacheDir` set to that directory instead. Add `testHostIn(t, dataDir)` beside `testHost` (`connect_test.go:200`).
   - A test that built a `PreparedMsg` or `DoneMsg` with a `*presetsetup.Provenance` passes a `Setup`. Its `Provenance` is the provenance's JSON, and its `Managed` holds the provenance's preset names.
   - `TestApplyConnectDoneWarnsOnProvenanceFailure` (`model_test.go:7075`) sets the blocking directory as `Deps.ModelCacheDir` through `newTestModelDeps`.
7. **Allowlist.** `connect/connect.go`'s key goes. `model.go` loses `config.DataDir` and `marshal/internal/app/presetsetup`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'presetsetup\|modelcatalog\|DataDir\|modelCacheDir\|WithModelCache' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7b-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass. The connect tests and `TestApplyConnectDone*` are among them.

Mutation check: make `applyConnectDone` ignore `msg.Setup.Managed`, and `TestApplyConnectDoneRefreshesManagedPreset` fails. Make `PreparePresets` return no `Managed` names, and `TestPreparePresetsRunsPresetSetup` fails. Restore both.

## Task 8: Verification and docs

**Goal:** the full suite is green, and the spec and `AGENTS.md` record plan 7b.

**Files:** `docs/daemon/p2-client-seam.md`, `AGENTS.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 7b entry to the Implementation record, after plan 7a's. It lists:
   - the wire type and the two status fields;
   - the thirteen methods and their `Methods` rows: one on an existing ACP method, twelve on eleven new ones;
   - the five `Deps` fields and `tui.Model.Session`;
   - that the TUI's config reads, writes and reloads go through the seam apart from the skills panel's, and that the adapter refreshes the layers and the trust hash;
   - that preset setup runs agent side, with the provenance opaque to the TUI;
   - the bridge's frame cap and failure;
   - the options, fields and parameters deleted;
   - the allowlist's 21 entries across 6 files, four keys entirely, and T1b's three names.
2. **`AGENTS.md`.** Under "Test isolation and credentials", the "Isolate the environment" bullet gains one sentence: a TUI test that checks a config file the seam writes sets `inproc.Deps.HomeDir` or `UserConfigPath`, because the TUI's `WithHomeDir` does not reach the adapter.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p2-7b-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-7b-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `341f1941`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `341f1941`, all 117 packages passed. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository with a local provider (LM Studio or Ollama) and check by eye:
- `/set shell.allow_network on` notes the change with the project path, and `/settings` shows it;
- in `/settings`, a global-target row (a provider's API key env) saves to the user config, and the note names it;
- in `/settings`, renaming a provider keeps its models and its key;
- `/connect` to a local provider shows "save to:" with the user config, prepares presets, and switching saves and reloads ("✓ Switched to model: …");
- `/agents`, changing a role's preset, saves the project file;
- with a provider whose key env var is missing, `/doctor`'s fix saves the key ("✓ Saved API key for …");
- quitting and relaunching does not re-prompt for folder trust after a `/settings` change in a permanently trusted project.

## Integration notes

- **Plan 7c** moves the trust prompt (`WithTrustPrompt`, `trustpanel`), the agents panel's tool lookup and, unless plan 8 takes them with the rest of the skills panel, the skill-gate settings.
- **Plan 7d** redacts `Session.Config` and `Session.Layers`.
  - Every config the TUI hands back (`ApplyConfig`, `ReloadConfig`, `SaveProjectConfig`, `SaveUserProviders`) must then have its stored keys restored agent side.
  - A provider rename in the settings browser must carry the old name, so the key follows it.
  - A cleared key must stay cleared, which the user-value write already makes explicit.
  - The settings browser's "key stored" label, its masked last four characters and the wizard's `keySourceLabel` need the key's source and hint from the read.
  - The probes need a saved provider's stored key.
  - `SaveUserValue` then carries a secret when its path is a key's (C3).
- **Plan 8** moves the skills panel, whose autoload write and `p.state.Config` reads are the last direct config uses. It adds `Config` to T1b.
- **P3:**
  - the thirteen methods send their ACP methods;
  - `Config` and `Layers` become a mirror fed by `session_status`;
  - `ReloadConfig`'s "no reload path" result is always true over the pipe;
  - `SaveUserValue`'s value is converted by the field's type;
  - `SaveProviderKey` reconciles with `config/set_provider_key`'s full-entry write;
  - T3 has no test yet: P3's config plan should round-trip `config.Config` and `config.Layers`.

## Self-review

- **Every task verifiable alone.**
  - Task 1 is independent.
  - Task 2 adds adapter calls nothing uses yet, and wires them in `Run`.
  - Task 3 moves reads only.
  - Task 4 moves the panels' writes while the model still refreshes layers itself, so a panel's save is never followed by stale layers.
  - Task 5 moves the model's writes and deletes the TUI's layer and trust hooks, now that every write refreshes agent side.
  - Task 6 moves the reloads.
  - Task 7 moves preset setup.

  Each keeps T1 and the goldens green, and trims exactly the allowlist entries it makes stale.
- **Cold executor.** Each step names the function, what it calls after the change, and what stays. The no-reload-path rule, the notice order, the layer and trust refresh points, the provenance round trip and the test helpers are spelled out, and each new test names its setup and the behaviour it pins.
- **Contracts.** The wire type and its encodings, the thirteen methods and their rows, `Deps`' fields, the adapter's helpers, and the TUI's changed signatures are declared there.
- **Anchors.** Every path, symbol and line was checked at `341f1941`.
- **Spec.** C3, C5, C10 and the Migration order are updated, with the revision record "2026-11-05, against `341f1941` (plan 7a merged)" holding decisions 1 to 15.
- **No placeholders.**
