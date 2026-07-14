# /connect and /models: OpenCode-style Provider & Model Setup

**Date:** 2026-07-14
**Status:** Approved (design)
**Scope:** `internal/app/tui/connect/` (new), `internal/app/tui/probe/` (new), `internal/app/tui/model.go`, `internal/commands/commands.go`, `internal/app/tui/settings/discover.go` (reduced)

## Problem

Setting up a model and provider in Marshal today is still too hard. The existing setup path lives inside `/settings` — a deep drill-down form — and requires three conceptual layers before you can chat: a `[providers.<name>]` entry, a named model preset, and an agent profile with per-role slots. The Phase-1 provider wizard (template picker + test connection + pickers) improved the form but did not change the conceptual model: a user still has to hand-build a preset and wire it into a profile.

OpenCode's `/connect` + `/models` collapses all of that to "pick a provider, enter a key, pick a model, go." This spec brings that fast path to Marshal while keeping the preset/profile/routing layer as an optional advanced tier for power users and the swarm.

## Goal

A `/connect` and `/models` fast path where picking a provider + model is enough to start chatting. Named presets and per-role routing remain as an advanced layer (used by `/settings`, the swarm, and the SDD workflow); the default config works with zero presets.

## Non-goals

- Removing or deprecating presets, agent profiles, or role-based routing.
- Changing the swarm / SDD routing machinery (they keep reading presets/profiles).
- OAuth or console-managed provider flows (Marshal is local-first with no hosted auth backend).
- Headless `/connect`/`/models` CLI subcommands or ACP wiring (out of scope).

## Decisions (from brainstorming)

1. **Fast path + keep presets.** Picking a model via `/models` works with zero presets; presets/profiles stay as the advanced layer.
2. **New `/connect` + `/models` slash commands.** Match OpenCode's mental model. `/settings` stays for advanced tuning.
3. **Direct agent model.** Picking a model sets `config.Agent.Provider` + `config.Agent.Model` directly; the existing legacy router path (`internal/llm/routing/router.go` `legacyRoute`) already honors this, so no preset is required.
4. **API key inline + env var.** `/connect` prompts for an API key inline (masked) and writes `api_key` and/or `api_key_env` (the template's suggested env var). Local providers need no key. No hosted auth.
5. **Live probe + cached fallback.** `/models` probes each provider's `/v1/models` (5s timeout, existing `probeProvider`) on first open, caches per session, falls back to the template's curated `Models` list.
6. **Project-local persistence.** Selections write to `.marshal/config.toml` via the existing `config.SaveProjectConfig`, consistent with `/settings` and trust gating.

## Phase 1: Overlay architecture & command wiring

### New commands

Two new slash commands registered in `internal/commands/commands.go` alongside the existing `/model`:

- **`/connect`** — "Add or reconnect a provider" — opens the connect overlay.
- **`/models`** — "Pick a model from connected providers" — opens the models overlay.

The existing `/model` stays as the preset-cycling shortcut for power users; both `/model` and `/models` are listed in `/help`.

### Overlay integration

The single-shot `picker` modal pattern already in `tui.Model` (the `pickerModel` + `pickerCommand` fields, `model.go:491–528`) cannot express multi-step flows. So we introduce a lightweight **connect overlay state machine** that wraps the existing `picker` and a masked `textinput` prompt.

A new `internal/app/tui/connect/` package holds a Bubble Tea `Model` that drives a small state machine (detailed in Phase 4). The TUI's main `Model` gains:

- `connectModel *connect.Model`
- `connectOpen bool`
- `discovered map[string][]string` — a shared discovery cache at the TUI level, populated by the same probe helper, reused across `/connect` re-opens and `/models`.

While `connectOpen`, key messages route to `connectModel.Update` (the same focus-trap pattern as the settings overlay and the picker modal). Non-key messages (ticks, agent events) keep flowing so background work continues.

The connect `Model` emits two terminal messages upward:
- `connect.DoneMsg{Provider, Model}` — success → the TUI model persists, rebuilds the route, and closes the overlay.
- `connect.CancelledMsg` — Esc → close the overlay.

### Chaining

- `/connect` completing the API-key step + probe auto-advances to the model-pick step scoped to the just-added provider.
- `/models` invoked with no providers configured shows a single "Add a provider…" item that, on pick, transitions to the `/connect` template-pick step — mirroring OpenCode's `DialogModel` → `DialogProvider` chaining.

## Phase 2: `/connect` flow

### Step 1 — Pick template

A `picker` overlay titled "Connect a provider" listing `provider.All()` templates. Each item:

- `Label` = `template.Label` (e.g. "Ollama (local)", "OpenRouter")
- `Detail` = `template.BaseURL`
- `Badge` = "local" or "remote" (reuse `badgeForTemplate`)
- `Value` = `template.ID`

A "Custom (OpenAI-compatible)" entry (already in the template catalog as `openai_compatible`) is always present. `allowCustom = true` lets a user type an arbitrary provider id.

Picking a template → advance to step 2.

### Step 2 — Credentials

An inline `textinput` prompt. The title and hint adapt per template:

- **Local providers** (`template.Local == true`): skip this step entirely — no key needed. Advance straight to step 3.
- **Remote providers with a `KeyEnv`** (OpenRouter, Groq, OpenAI): title "API key", subtitle shows `template.KeyHint` (e.g. "Get a key at https://openrouter.ai/keys"). The entered value is stored as `api_key` (literal, masked) if non-empty; the template's `KeyEnv` is also written to `api_key_env` so a user who prefers env vars can leave the prompt blank and set the env var themselves — leaving no secret on disk. Footer: `[↵] save  [Esc] cancel`.
- **Custom provider**: two prompts — first "Base URL" (plain `textinput`), then "API key" (masked, optional). No `KeyEnv`.

### Step 3 — Probe + name

Builds the `ProviderConfig` from the template + entered key, auto-names it via `provider.UniqueName(template.ID, existingKeys)`, and runs `probe.Provider` (5s timeout). While probing, the overlay shows a spinner line "Connecting…". On success it advances to step 4.

On failure it shows the error inline (muted red) with affordances:
- `[r] retry` — re-run the probe.
- `[Esc] cancel` — close without writing.
- `[s] skip` — write the provider entry without a confirmed probe. Always available for `Local == true` templates (Ollama may simply not be running yet); available for remote providers too, since the key may be valid and `/v1/models` may not exist on that backend.

### Step 4 — Pick model

- If the probe returned models, a `picker` titled "Select model" lists discovered model ids, `allowCustom = true` for exotic/local-fork models. Picking one (or skipping → "use model id later") emits `connect.DoneMsg{Provider: name, Model: modelID}`.
- If the user skipped the probe, this step shows the template's curated `Models` list if non-empty, else a single "Enter model id manually" item using `allowCustom`.

Happy-path keystroke count: Ollama = pick template → pick model (2); OpenRouter = pick template → paste key → pick model (3).

## Phase 3: `/models` flow

### Opening

1. Gather configured providers (`sortedKeys(cfg.Providers)`).
2. **Zero providers**: show a `picker` with a single item "Add a provider…" → on pick, transition to the `/connect` template-pick step.
3. **One or more providers**: build the model list.

### Model list assembly

The shared discovery cache on `tui.Model` (`discovered map[string][]string`) feeds the list. On first `/models` open:

- For each provider with a cached list, include those models immediately (badge "◉ discovered").
- For each provider with **no** cached list, kick off a background `probe.Provider` per provider (batched as `tea.Cmd`s). Each probe result updates the shared cache and the picker items refresh live. While in flight, that provider's section shows a single spinner row "probing…". The picker stays interactive — the user can pick from already-discovered providers while others probe.
- **Remote providers gated behind `privacy.remote_providers_allowed`**: a remote provider with the flag off shows a single row "✗ blocked (enable in /settings)" instead of probing. Local providers probe freely.

### Picker layout

Items are grouped by provider using the existing `picker.Item.Group` field (the picker already renders group headers in the unfiltered view). Each item:

- `Label` = model id
- `Detail` = provider name
- `Badge` = "◉ discovered" / "◯ catalog" / "● now"
- `Group` = provider label
- `Value` = `provider|model` (encoded so the pick handler knows both)

The currently-active model (from `state.ActiveRoute()`) gets a "● now" badge. `allowCustom = true` preserves the free-text escape hatch.

### Fallback when probe fails

If a probe fails and the provider has a template with curated `Models`, those are shown with badge "◯ catalog". If neither probe results nor template models exist, that provider's section shows "Test connection to discover" — picking it triggers a re-probe (reuses the same `probe.Provider` Cmd).

### Picking a model

`PickedMsg{Value: "provider|model"}` → the TUI model handler. `/models` is **authoritative** for the active route (unlike the session-only `/model` preset switch, which doesn't persist):

1. Sets `cfg.Agent.Provider = provider`, `cfg.Agent.Model = model`.
2. Sets `cfg.Profile.Default = ""` so the **legacy router path** (`router.go:100 legacyRoute`) takes effect for the single-agent loop. The router resolves profile routes first (preferred over legacy), so unsetting the default profile pointer is what makes `Agent.Provider`/`Agent.Model` the single source of truth. Any existing `[agent_profiles.*]` and `[models.presets.*]` entries are **kept in config** (deleted nothing), so:
   - The **non-expert case** (no presets/profiles configured): `/models` "just works" immediately.
   - The **power-user case** (presets/profiles configured, e.g. for asymmetric swarm routing): `/models` opts into a single model for *everything* — the single-agent loop and the swarm now run the picked model for all roles. The presets/profiles remain in the file and can be re-activated by re-setting `[profile] default` in `/settings`. This matches decision 1: the fast path is pick-a-model-and-go; presets/profiles are the advanced layer you opt back into.
3. Calls `configReloader(newCfg)` to rebuild the agent runtime (same path `/settings` save uses).
4. Persists via `config.SaveProjectConfig(projectConfigPath, newCfg)` so it survives restart. Footer on the `/models` picker: `saved to project config` (distinguishes it from `/model`'s `session only — /settings to persist`).
5. Confirms in the transcript: "Switched to model: qwen2.5-coder:7b (ollama)".

### Difference from the existing `/model` command

| | `/model` (existing) | `/models` (new) |
|---|---|---|
| Scope | Session-only, no file write | Persisted to `.marshal/config.toml` |
| Target | Named presets only | Any live model from any connected provider |
| Routing mechanism | Builds synthetic "switched" profile | Unsets profile default → legacy route |
| Requires presets | Yes (`Models.Presets`) | No |

## Phase 4: Connect overlay internals & shared helpers

### Package layout

A new `internal/app/tui/connect/` package holds the state-machine `Model`. It depends only on `picker`, `provider`, `config`, and the shared probe helper — no settings dependency.

### Shared probe extraction

`probeProvider` and `isLocalhost` currently live in `internal/app/tui/settings/discover.go`. To avoid the connect package importing settings (wrong dependency direction), they move to a new `internal/app/tui/probe/probe.go`:

- `func Provider(fieldID, name string, pc config.ProviderConfig) tea.Cmd` — same logic (build throwaway provider via `provider.NewFromConfig`, call `Models(ctx)` with 5s timeout, return `ResultMsg`).
- `type ResultMsg struct { FieldID, Provider string; Models []string; Err error }` — exported so both settings and connect consume it.
- `func IsLocalhost(baseURL string) bool` — moved from settings.

`internal/app/tui/settings/discover.go` is reduced to re-exports or updated to call `probe.Provider`/`probe.ResultMsg` directly; the settings test (`discover_test.go`) is updated accordingly.

### State machine

```go
type Model struct {
    step       step            // pickTemplate | baseURL | apiKey | probing | pickModel | done | cancelled
    picker     *picker.Model   // reused for pickTemplate and pickModel steps
    input      textinput.Model // reused for apiKey/baseURL prompts (masked for keys)
    title      string
    subtitle   string          // hint line (e.g. KeyHint URL)
    footer     string
    template     provider.ProviderTemplate
    providerName string
    providerCfg  config.ProviderConfig
    models     []string         // discovered or catalog fallback
    probeErr   error
    cfg        config.Config    // read-only copy: existing-provider keys, privacy flag
    width, height int
}

type step int
const (
    stepPickTemplate step = iota
    stepBaseURL            // custom providers only
    stepAPIKey             // skipped for local
    stepProbing
    stepPickModel
    stepDone
    stepCancelled
)

type DoneMsg struct {
    Provider string
    Model    string
}
type CancelledMsg struct{}
```

### Message flow

The connect `Model.Update` handles `picker.PickedMsg`, `picker.CancelledMsg`, `tea.KeyPressMsg` (for the input steps), and `probe.ResultMsg` (the probe callback, from the shared `probe` package). It never touches `tui.Model` state directly — it only emits `DoneMsg`/`CancelledMsg` upward. The TUI model handles `DoneMsg` (persist + reload) and `CancelledMsg` (close overlay).

### TUI integration

Three new fields on `tui.Model`:

- `connectModel *connect.Model`
- `connectOpen bool`
- `discovered map[string][]string` (shared discovery cache, populated by `probe.Provider`).

The existing picker-modal block (`model.go:491–528`) gains two cases ahead of the current `picker.PickedMsg` handler:

- `connect.DoneMsg` → persist provider+model, call `configReloader`, close overlay, add transcript confirmation.
- `connect.CancelledMsg` → close overlay.

### Keybinding

Marshal already has `Alt+M` cycling presets. We add `Ctrl+P` (currently unbound) as the `/models` shortcut, documented in the `?` help overlay. `/connect` stays slash-command-only (a setup action, not a frequent toggle).

## Phase 5: Error handling & local-first constraints

### Local-first gating

- `/models`'s per-provider background probes skip any remote provider when `cfg.Privacy.RemoteProvidersAllowed == false`, rendering a "✗ blocked (enable in /settings)" row instead. Local providers probe freely.
- `/connect`'s probe step is always allowed for localhost `BaseURL`. For remote templates, the probe runs only if `RemoteProvidersAllowed` is on; otherwise the overlay shows a warning "Remote providers are disabled — enable in /settings → Privacy" with a `[s] skip` option that still writes the provider entry.

### Default config stays clean

`config.Default()` gains **no** providers and **no** preset — the fast path writes `Agent.Provider` + `Agent.Model` only. First-run: `marshal` starts with the agent loop disabled (as today), and the transcript shows the existing "provider not configured" hint **with an added nudge**: "Run `/connect` to add a provider, or `/models` to pick a model." This replaces the current hint pointing solely at `/settings`.

### No secrets in plaintext

The API-key prompt reuses the settings masked-scalar pattern. `config.SaveProjectConfig` already stores `api_key` literals (masked in `/settings`); the connect overlay uses the same `masked: true` textinput. When a template carries `KeyEnv`, the overlay writes `api_key_env` and treats the entered key as optional — storing it only if non-empty, so a user who sets the env var themselves leaves no secret on disk.

### Error rendering

Following the tui-design anti-pattern rules (no modal dialogs for transient errors):

- Probe failures render **inline** in the overlay as a muted red line under the relevant step, not a modal. Affordances: `[r] retry`, `[Esc] cancel`, plus `[s] skip` for the probe step.
- Invalid custom provider id / duplicate name → inline error under the input prompt, input stays focused (matches OpenCode's re-prompt-on-invalid pattern).
- Save failures (disk write, permission) → the overlay closes and the error surfaces as a transcript system message (the same path `/settings` save errors use).

### Spinner timing

Per tui-design: the probe spinner shows only after **200ms** to avoid flash on fast local probes. The overlay renders a braille spinner (`⠋⠙⠹…`, 80ms) during the probe step and the per-provider rows in `/models`, gated on a `probingSince` timestamp.

## Phase 6: Testing

- `internal/app/tui/probe/probe_test.go` — `Provider` against an `httptest` server (known `/v1/models` body, timeout, non-200); `IsLocalhost` for each address form. Mirrors the existing `discover_test.go`, updated/replaced.
- `internal/app/tui/connect/connect_test.go` — state-machine transitions: pickTemplate→apiKey→probing→pickModel→DoneMsg; local template skips apiKey; probe failure shows retry/skip; custom provider prompts BaseURL; PickedMsg with allowCustom; Esc at each step emits CancelledMsg; remote provider blocked when privacy off.
- `internal/app/tui/model_test.go` (extend) — `/connect` opens overlay; `/models` with zero providers shows "Add a provider…"; `/models` PickedMsg sets `Agent.Provider`/`Agent.Model`, calls configReloader, persists via SaveProjectConfig; `Ctrl+P` opens `/models`; `connect.DoneMsg`/`CancelledMsg` close overlay; background work continues while overlay open.
- `internal/app/tui/settings/discover_test.go` — updated to use `probe.Provider`/`probe.ResultMsg` (or deleted if fully subsumed by the probe package test).

## Architecture & data flow

New files:

- `internal/app/tui/probe/probe.go` — `Provider`, `ResultMsg`, `IsLocalhost` (extracted from settings).
- `internal/app/tui/probe/probe_test.go`
- `internal/app/tui/connect/connect.go` — the state-machine `Model`, `DoneMsg`/`CancelledMsg`.
- `internal/app/tui/connect/connect_test.go`

Modified files:

- `internal/commands/commands.go` — register `/connect`, `/models` (handlers open the overlay; the actual work is in the TUI model, like `/settings`).
- `internal/app/tui/model.go` — `connectModel`/`connectOpen`/`discovered` fields; `connect.DoneMsg`/`CancelledMsg` handlers in the picker first block; `/connect` + `/models` command dispatch; `Ctrl+P` hotkey; first-run hint nudge.
- `internal/app/tui/settings/discover.go` — reduced to call `probe.Provider`/`probe.ResultMsg` (or deleted).
- `internal/app/tui/settings/frames_collections.go` — updated imports for the moved probe helper.

**Data flow:** `/connect` or `/models` builds a `connect.Model` and opens it. The connect `Model` drives its state machine, using `picker` and `textinput` for each step, and `probe.Provider` for discovery. It emits `DoneMsg` upward only. The TUI model, on `DoneMsg`, updates `cfg.Agent` (provider + model), clears any synthetic "switched" profile, calls `configReloader(newCfg)` to rebuild the agent runtime, and `config.SaveProjectConfig` to persist. `/models` reuses the same `connect.Model` pre-positioned at the pick-model step, feeding the shared `discovered` cache from background `probe.Provider` Cmds per provider.

## Design constraints honored

- **Local-first:** `config.Default()` adds no providers and no presets. Remote discovery is gated behind `privacy.remote_providers_allowed`. The default config still has no providers and no remote assumptions.
- **Provider-flexible:** all flows go through the generic `openai_compatible` factory; the template catalog is a UX convenience, not a new provider type.
- **Tool-safe:** the connect overlay mutates only in-memory state until `DoneMsg`; persistence goes through the existing `SaveProjectConfig` → file write path.
- **The TUI renders only** — routing, policy, and prompt logic stay out of the view layer. The connect `Model` emits a `DoneMsg`; the TUI model (still "view") merely relays it to `configReloader` + `SaveProjectConfig`, the same persistence seam `/settings` already uses — no routing logic moves into the overlay.