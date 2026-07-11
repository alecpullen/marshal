# Provider Wizard and Settings Expansion

**Date:** 2026-07-12
**Status:** Approved
**Scope:** `internal/app/tui/settings/`, `internal/llm/provider/`, minor touch points in `internal/app/tui/model.go`/`view.go`

## Problem

Adding a provider in Marshal's settings TUI today is *type a name → fill five blank fields by hand*. There is no provider template catalog, no live model discovery, and no connection check. The Provider/Model fields in the Agent and Model Presets frames are free-text scalars, so users must remember exact provider names and model ids. The settings overlay also lacks duplicate/reorder, reset-to-defaults, and a save-time diff preview.

The goal is an opencode-style "add a provider easily" flow plus broader settings polish.

## Phasing

**Approach A** (approved): one spec, two phases.

- **Phase 1 — Provider experience.** Template catalog, add-provider wizard, `kindAction` row primitive, live model discovery, test connection, provider/model pickers in Agent + Presets. Delivers the headline flow on its own.
- **Phase 2 — Settings polish.** Duplicate + reorder, reset-to-defaults, save diff preview. Independent; reuses `kindAction`. Can slip without blocking Phase 1.

Phase 2 reuses the `kindAction` primitive introduced in Phase 1, so the two share a single spec but split into two implementation plans.

## Current state (grounding)

- Settings = sidebar + drill-down `fieldList` with four row kinds: `kindToggle`, `kindScalar`, `kindEnum`, `kindDrill`. Adding a provider = type a name → blank entry → drill in and fill `Type/BaseURL/APIKeyEnv/APIKey/ToolCalling` by hand.
- `OpenAICompatible.Models(ctx)` already does `GET {base_url}/models` — live discovery is largely built (`internal/llm/provider/openai_compatible.go:90`).
- A reusable fuzzy `picker` (groups/badges/detail) exists in `internal/app/tui/picker`, used by `/model`.
- Settings already has an overlay pattern (`overlaySearch`/`overlayHelp`) for modal layers.
- `ProviderConfig` is generic (`type = "openai_compatible"` is the only supported value); no provider templates exist.
- The model `catalog` package is a curated Go map of known context windows; unknown models resolve to `(0, 0)`.

## Phase 1 design

### 1. Provider template catalog

A new table in `internal/llm/provider/templates.go` describing well-known OpenAI-compatible endpoints. It is a Go map (like the model `catalog`) — no network.

```go
type ProviderTemplate struct {
    ID          string   // "ollama", "lmstudio", "openrouter", "groq", "custom"
    Label       string   // "Ollama (local)"
    Type        string   // "openai_compatible"
    BaseURL     string   // default base_url
    Local       bool     // localhost — always allowed regardless of privacy
    ToolCalling bool     // default tool_calling flag
    KeyEnv      string   // suggested api_key_env ("" for local/no-auth)
    KeyHint     string   // "Get a key at https://openrouter.ai/keys"
    Models      []string // curated fallback model ids for the picker
}
```

Seed entries: `ollama`, `lmstudio`, `openrouter`, `groq`, `openai`, `openai_compatible` (custom).

### 2. Add-provider wizard

In the Providers frame, `a` no longer opens the bare "type a name" prompt. It opens a settings-level **overlay picker** (reusing the `picker` package) listing templates + a "Custom (OpenAI-compatible)" entry. Picking a template:

1. Creates the `[providers.<name>]` entry pre-filled from the template (Type, BaseURL, ToolCalling, and `APIKeyEnv` when the template suggests one).
2. Auto-names it from the template ID, suffixing `-2`, `-3` on collision (`ollama`, `openrouter-2`).
3. Drills straight into the new entry's detail frame.

**Name field.** The provider detail frame gains a "Name" field at the top — a scalar that renames the map key (delete old key, insert under new key, error on collision/empty). This closes the gap that names were previously fixed at add time, so the auto-named entry is trivially renamed inline.

Entry order remains alphabetical via the existing `sortedKeys` helper; the wizard does not manage order.

### 3. New `kindAction` row primitive

The headline features (test connection, live model discovery, future duplicate/reorder/reset) all need the same missing primitive: a row that triggers an action on Enter rather than editing a value. Today `field` has `toggle/scalar/enum/drill`; there is no "button" row.

```go
// kindAction: Enter triggers act() — a side-effectful, possibly async action.
type field struct {
    ...
    // kindAction
    act       func() tea.Cmd  // returns a Cmd that emits an actionResultMsg
    actLabel  func() string   // right-cell label, e.g. "ok", "…", "failed"
    // pendingResult labels the row while its Cmd is in flight
    pendingLabel string
}
```

- `View()`: renders the action row's right cell as the `actLabel()` output, styled (muted while pending, success/error colors when done).
- `Update()` / `fieldList`: Enter calls `act()`, stores the returned `tea.Cmd` as a new `pushCmd`-style request the pane forwards up to the settings `Model`, which forwards it to Bubble Tea. The resulting `actionResultMsg` carries the row's `field.id` so the right target row gets the result label.
- `dirty()`: action rows are never dirty (they don't touch config directly). Test Connection and Refresh Models mutate config only through their own explicit setters, after a user saves.
- Footer hint for an action row: `[↵] run`.

One primitive covers test connection, refresh model list, and (Phase 2) duplicate / reset-to-defaults. Keeps `fieldList` switch logic clean and the footer key hint uniform.

### 4. Live model discovery + test connection

Both features are the first consumers of `kindAction` and share one mechanism.

**Probe function.** A single helper builds a throwaway provider from the in-progress `ProviderConfig` (via the existing `factory.NewFromConfig`) and calls `Models(ctx)` with a **5s timeout**:

```go
// internal/app/tui/settings/discover.go
func probeProvider(name string, pc config.ProviderConfig) tea.Cmd {
    return func() tea.Msg {
        // ... build provider, call Models with 5s timeout ...
        return probeResultMsg{Provider: name, Models: ids, Err: err}
    }
}
```

```go
type probeResultMsg struct {
    Provider string    // the [providers.<name>] key probed
    Models   []string  // model ids on success; nil on error
    Err      error     // nil on success
}
```

It is a private settings-package message (like `SavedMsg`/`CancelledMsg`), dispatched by the settings `Model.Update` to the active pane.

**Test connection = discovery.** One action does both: a "Test connection" `kindAction` row at the bottom of each provider detail frame runs `probeProvider`. The `actLabel()` reflects state:

- idle: `↵ test`
- in-flight: `…`
- ok: `✓ ok (12 models)` (success color)
- fail: `✗ connection refused` (error color, truncated)

On success the returned model list is written to the discovery cache. Pressing the row again later refreshes the list (e.g. after `ollama pull`), so "test connection" and "refresh models" are the same row.

**Discovery cache.** The settings `state` gains:

```go
type state struct {
    ...
    discovered map[string][]string // provider name → model ids, session-local
}
```

This cache is **not persisted to config** — it lives only for the settings session and feeds the pickers (Section 5). It is invalidated for a provider when that provider's `BaseURL` or `APIKey`/`APIKeyEnv` field is edited (stale-URL guard), so changing the endpoint clears the old list until the next probe.

**Privacy gating.** An `isLocalhost(baseURL)` helper (`127.0.0.1`, `localhost`, `0.0.0.0`, `::1`). The test-connection action row is always enabled for localhost providers. For remote providers, when `cfg.Privacy.RemoteProvidersAllowed == false` the row renders disabled with `actLabel = "✗ blocked (enable Remote providers in Privacy)"` and `act()` is a no-op. This keeps the local-first guarantee: the settings UI never silently reaches the public internet unless the user has explicitly opted in.

### 5. Provider/Model pickers in Agent + Model Presets frames

The provider detail frame's `BaseURL`/`APIKey`/`APIKeyEnv`/`ToolCalling` rows stay as `kindScalar`/`kindToggle` — only the picker touch-points change.

**New row kind: `kindPicker`.** A `scalar`-like row whose value is constrained to a known set, but that set is large and **lazily populated**. Like `kindEnum` (right-cell shows `value ▾`, Enter opens a dropdown) but it reuses the fuzzy `picker` overlay rather than the inline `picking` dropdown, since provider/model lists are long:

```go
// kindPicker
options func() []picker.Item  // current candidate set (empty = loading)
onPick  func(string) error    // validate + apply the chosen value
pending func() bool           // true while a discovery lookup is in flight
```

- `fieldList.openRow`: `kindPicker` emits a `pushPickerRequest{field.id}` the pane forwards up. The settings `Model` opens the `picker` overlay (a third `overlayKind`: `overlayPicker`) with the field's `options()`.
- `picker.PickedMsg`: handled in settings `Model.Update`, resolves the `field.id`, calls the row's `onPick(value)`. On error it falls through to the inline-error path. Esc/`picker.CancelledMsg` closes the overlay.
- `View()`: right cell shows `value ▾`, plus a `…`/spinner hint when `pending()` is true.

**Wiring the three frames:**

| Frame | Row | Old | New |
|---|---|---|---|
| Agent | Provider | `kindScalar` (free text) | `kindPicker` — options = `sortedKeys(cfg.Providers)`, label `<empty — add a provider>` when none |
| Agent | Model | `kindScalar` (free text) | `kindPicker` — options from `state.discovered[provider]` if cached, else the template's curated `Models` fallback, marked `◉ discovered` / `◯ catalog`; shows `<refresh first>` when neither is available |
| Model Presets | Provider | `kindScalar` | `kindPicker` (same as Agent Provider) |
| Model Presets | Model | `kindScalar` | `kindPicker` (same as Agent Model, scoped to that preset's Provider) |

**Empty-state behavior** is the key UX guardrail:

- No providers configured → Provider picker shows one synthetic item "Add a provider…", which on pick pushes the Providers frame (drill) and triggers the wizard (Section 2). This keeps the local-first, no-provider-assumption default coherent.
- Provider set but no models discovered → Model picker shows the template's curated list if present, else a single item "Test connection to discover" that triggers the probe for that provider (reusing the probe action) and reopens the picker. So a user can always recover from a cold cache.

**Free-text escape hatch.** Each `kindPicker` overlay still accepts the typed filter as a custom value: if the filter has no exact match, the top row is "Use '<filter>'" and `onPick` accepts it. This preserves the current free-text ability for exotic/local-fork models without adding a second row kind.

## Phase 2 design

Phase 2 is settings polish; all three reuse the `kindAction` primitive from Phase 1 so no new row kinds are needed.

### 6. Duplicate + reorder

Two new field-list operations, surfaced as footer-key actions (not rows):

- **Duplicate (`y` / "yank-then-paste" pattern).** `fieldList` gains `yankFn func()` / `pasteFn func() error` closures set by the owning frame when the cursor row is duplicatable (collection entries: a provider, a preset, a hook, a permission rule). Pressing `y` on a provider row yanks a *copy of its config*; pressing `p` pastes it as `<name>-copy` (or `<name>-copy-2` on collision) and drills in. For presets it copies the whole `ModelPreset` and prompts nothing — the copy is editable inline. For hooks/permissions (slice-backed) it duplicates the entry in place.
- **Reorder (`shift+up` / `shift+down`).** Only meaningful for **ordered** collections. The slice-backed collections (`hooks.entries`, `permissions.rules`, `project.languages`, `indexing.ignore`) are inherently ordered, so their frames gain `moveUp`/`moveDown` closures; the map-backed collections (providers, presets, mcp servers) are not reorderable and the keys are disabled there. Reordering mutates the working config immediately (consistent with every other edit — everything is guarded by the single Ctrl+S transaction).

Footer hints grow to: `… [y] duplicate [shift↑↓] move [d] delete …` only when the row supports them, so non-ordered entries show nothing misleading.

### 7. Reset to defaults

A "Reset <section> to defaults" `kindAction` row appended at the bottom of **every** root frame's field list. It restores that section's subtree of the working config to `config.Default()` and is fully undo-able until save (dirty flag flips, Esc-twice prompts discard). Per-section rather than global reset keeps the blast radius small and matches the sidebar-section mental model. The action row's label reflects confirmation state:

- idle: `reset to defaults`
- after first Enter: `again to confirm` (warn color) — the same pending-confirm idiom as the top-level Esc: a second consecutive Enter applies; moving off the row (`j`/`k`) or `Esc` disarms and relabels back to idle
- applied: label → `✓ reset`

### 8. Save diff preview

`Ctrl+S` no longer saves immediately. It computes a structured diff between `state.snapshot` and `state.cfg` (per-path: `providers.foo.api_key changed`, `added providers.bar`, `removed presets.x`, `privacy.remote_providers_allowed: false → true`), and opens a read-only `overlayDiff` (a fourth overlay kind) listing the changes with `+`/`-`/`~` prefixes in success/error/muted colors. The footer shows `[↵] save  [Esc] cancel`. Enter commits (the existing `saveCmd` path); Esc returns to editing without saving.

**Diff computation.** A small `configdiff` helper walks the two configs struct-by-struct (the `reflect`-based `dirty()` check already proves the two are structurally comparable; the diff reuses that comparability but produces human-readable lines instead of a bool). Secrets (`api_key` fields) render as `~ providers.x.api_key: •••• → ••••••••` (masked, never the plaintext), so the diff is safe to glance at.

**No-change case.** If the diff is empty, Ctrl+S shows an `overlayDiff` with a single "no changes" line and the footer shows `[Esc] close` (Enter is a no-op). This replaces the current silent-noop-on-Ctrl+S-with-clean-config behavior with explicit feedback.

## Architecture & data flow

New files:

- `internal/llm/provider/templates.go` — the `ProviderTemplate` table and `Lookup(id)`.
- `internal/app/tui/settings/discover.go` — `probeProvider` + `isLocalhost` + the `probeResultMsg` type.
- `internal/app/tui/settings/configdiff.go` — the diff helper (Phase 2).

Modified files:

- `internal/app/tui/settings/field.go` — add `kindAction`, `kindPicker`, their closures.
- `internal/app/tui/settings/fieldlist.go` — handle the two new kinds in `Update`/`openRow`/`View`; yank/paste/moveUp/moveDown (Phase 2).
- `internal/app/tui/settings/model.go` — `overlayPicker`, `overlayDiff` overlay kinds; dispatch `probeResultMsg`/`picker.PickedMsg`/`actionResultMsg`.
- `internal/app/tui/settings/state.go` — `discovered` cache.
- `internal/app/tui/settings/frames_collections.go` — `providersFrame` wizard entry, Name field, test-connection row, `kindPicker` provider/model rows in `presetsFrame`.
- `internal/app/tui/settings/frames_agent.go` — Provider/Model `kindPicker` rows.
- `internal/app/tui/settings/messages.go` — new message types.

**Data flow:** the settings `Model` owns overlay routing. A `kindPicker` row's Enter produces a `pushPickerRequest`; the `Model` opens `overlayPicker`. `picker.PickedMsg` resolves the field id and calls `onPick`. A `kindAction` row's Enter returns a `tea.Cmd` the `Model` forwards to Bubble Tea; the resulting `probeResultMsg`/`actionResultMsg` is dispatched to the active pane, which finds the row by `field.id` and updates its `actLabel`. The discovery cache lives on `state` and feeds picker `options()`. Save still goes through the existing `saveCmd` → `config.SaveProjectConfig` path (Phase 2 inserts the diff overlay between Ctrl+S and `saveCmd`).

## Error handling

- Probe failures surface inline as the action row's `actLabel` (`✗ <truncated error>`), never as a modal. The 5s timeout and connection errors are caught and rendered the same way.
- `kindPicker.onPick` errors render through the existing inline `errMsg` path under the focused row.
- The privacy gate renders a disabled row with an explanatory label rather than failing at runtime, so a user sees *why* a remote probe is blocked without a round-trip.
- The free-text escape hatch validates through `onPick`, so an invalid custom model id is rejected the same way an enum value would be.

## Testing

- `internal/llm/provider/templates_test.go` — table lookup, collision-free naming helper.
- `internal/app/tui/settings/discover_test.go` — `probeProvider` against an `httptest` server returning a known `/v1/models` body; timeout; non-200; `isLocalhost` for each address form.
- `internal/app/tui/settings/fieldlist_test.go` — `kindAction` Enter fires the Cmd; `kindPicker` Enter emits the push request; result messages update the right row by id; yank/paste/move (Phase 2).
- `internal/app/tui/settings/model_test.go` — overlay picker open/pick/cancel; probe result dispatch; privacy-gated remote row is a no-op; save-diff overlay shows added/removed/changed lines with masked secrets (Phase 2).
- `internal/app/tui/settings/frames_collections_test.go` — wizard template pick creates a pre-filled entry; Name rename moves the key; test-connection row labels cycle idle→pending→ok/fail.
- `internal/app/tui/settings/frames_agent_test.go` — Provider picker offers configured providers; empty-state item pushes the Providers drill; Model picker falls back to the template catalog then to the "test connection to discover" item.
- Existing tests (`TestSettingsNavigationThroughMainModel`, `TestSettingsTypingThroughMainModel`, `TestSettingsBoolFieldToggleThroughMainModel`, `TestSettingsNavigationWithDefaultConfig`) are updated for the new row kinds on the Agent/Providers panes.

## Design constraints honored

- **Local-first**: remote discovery is gated behind `privacy.remote_providers_allowed`. The default config still has no providers and no remote assumptions.
- **Provider-flexible**: all flows go through the generic `openai_compatible` factory; the template catalog is a UX convenience, not a new provider type.
- **Tool-safe**: the settings overlay mutates only a working copy; nothing reaches the filesystem until Ctrl+S (Phase 2 adds an explicit diff gate before that save).
- The TUI renders only; routing, policy, and prompt logic stay out of the view layer.
