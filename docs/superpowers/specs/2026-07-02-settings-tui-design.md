# Settings TUI Design

## Goal

Add a runtime settings TUI to Marshal so users can view and edit essential configuration without leaving the application. The settings screen is reachable from the main TUI, persists changes to the project config file, and applies them immediately where possible.

This milestone is intentionally placed before Milestone O (First swarm prototype) so that users can switch profiles, models, and privacy settings on the fly as more specialist roles are introduced.

## Scope

### In scope

- Open a settings overlay from the main TUI with `Ctrl+O`.
- Edit the following essentials:
  - Default agent profile (`[profile] default`).
  - Legacy agent provider and model (`[agent] provider`, `[agent] model`).
  - Active model preset name, provider, and model (the preset currently mapped to the active profile's role).
  - Local-only flag on the active preset.
  - Remote providers allowed flag (`[privacy] remote_providers_allowed`).
- Persist saved changes to `.marshal/config.toml` in the current working directory.
- Apply changes immediately where possible:
  - Profile/model/preset/local-only updates affect the next agent turn.
  - If the provider config itself changed, the provider is rebuilt on save.
- Provide clear save/cancel/error feedback inside the settings overlay.

### Out of scope

- Editing tool allow/confirm/deny rules.
- Editing commands (`[commands]`).
- Editing indexing toggles (`[indexing]`).
- Editing role-specific context budgets (`[agents.<role>.context]`).
- Editing full model preset/agent profile tables beyond the active selection.
- Global config scope (`~/.config/marshal/config.toml`) for v1.
- A separate CLI entry point such as `marshal --settings`.
- Nested or table-based form widgets.

## Architecture

Add a new sub-package `internal/app/tui/settings` that owns the settings form and its Bubble Tea model. The main `internal/app/tui` package delegates to it when the settings overlay is open.

```text
internal/app/tui
  model.go          -- adds settingsOpen flag, routes messages when open
  option.go         -- adds WithProviderBuilder option for runtime rebuild
internal/app/tui/settings
  model.go          -- settings Bubble Tea model, field navigation, save/cancel
  field.go          -- simple field abstractions (string, bool, select)
  view.go           -- rendering for the settings overlay
  messages.go       -- settingsSavedMsg, settingsCancelledMsg
```

The config package gains a focused writer:

```text
internal/app/config
  config.go         -- existing load/merge logic
  save.go           -- new SaveProjectConfig(path, cfg) helper
```

This separation keeps the main TUI model from growing into a settings form manager and makes the settings model independently testable.

### Provider rebuild hook

The main TUI is constructed with an optional `ProviderBuilder` callback:

```go
type ProviderBuilder func(cfg config.Config) (provider.Provider, error)
```

When settings are saved, the main model calls this callback with the reloaded config. On success it replaces the runner's provider; on failure it records a provider error and leaves the settings overlay open so the user can correct the configuration. If no builder is provided, settings are still saved but the active provider remains unchanged until Marshal is restarted.

## Components

### `settings.Model`

- Holds a copy of the current `config.Config`.
- Holds a slice of fields and a focused index.
- Tracks a footer/status string for errors or confirmation.
- Exposes `Update` and `View` matching the Bubble Tea model interface.
- Returns `settingsSavedMsg` or `settingsCancelledMsg` when closed.

### Field types

Each field knows how to render itself, how to handle input, and how to write its value back into the copied config.

- `stringField` — for provider, model, and preset name.
- `boolField` — for `local_only` and `remote_providers_allowed`.
- `selectField` — for choosing the default profile from known `agent_profiles`.

All fields are simple: text inputs reuse `bubbles/textinput`, booleans toggle with `Space`/`Enter`, and selects cycle through options with `Enter`/`Left`/`Right`.

### `config.SaveProjectConfig`

Writes a minimal project config file containing only the essentials the TUI can edit:

```toml
[profile]
default = "local_balanced"

[agent]
provider = "ollama"
model = "qwen2.5-coder:14b"

[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
local_only = true

[privacy]
remote_providers_allowed = false
```

The preset section that is edited is the one currently mapped to the default profile's `implementer` role. If the default profile does not define an `implementer` role, the TUI falls back to the legacy `[agent]` provider/model fields and does not show or edit a preset.

The function preserves the existing file only if it already exists; otherwise it creates `.marshal/config.toml`. It does not read or merge global config — the caller reloads via `config.Load` after writing.

## Data flow

1. User presses `Ctrl+O` in the main TUI.
2. Main model creates `settings.New(m.state.Config)` and sets `settingsOpen = true`.
3. User edits fields; navigation is local to the settings model.
4. User presses `Ctrl+S`:
   - Settings model calls `config.SaveProjectConfig`.
   - On success it calls `config.Load` to get a fully merged config and returns `settingsSavedMsg{cfg: cfg}`.
   - On failure it sets the footer error and stays open.
5. Main model receives `settingsSavedMsg`:
   - Updates `m.state.Config`.
   - If a `ProviderBuilder` was configured, calls it with the new config and replaces the runner's provider on success. On failure it records a provider error and keeps the overlay open.
   - Sets `settingsOpen = false`.
6. User presses `Esc`:
   - Settings model returns `settingsCancelledMsg`.
   - Main model discards the copy and closes the overlay.

## Error handling

- **Write failure:** shown in the settings footer; config copy and main state are unchanged.
- **Reload failure after write:** keep the previous config, show an error in the footer, and stay open.
- **Invalid provider/preset combinations:** not blocked by the TUI. Marshal already falls back to a disabled runner with a provider error if a route cannot be resolved.
- **Missing `.marshal` directory:** `SaveProjectConfig` creates it if needed.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| `Ctrl+O` | Open settings overlay (or close without saving if already open) |
| `Tab` / `Shift+Tab` | Next / previous field |
| `Space` / `Enter` | Toggle boolean or cycle select field |
| `Ctrl+S` | Save changes and close |
| `Esc` | Cancel and close |

## Testing

- **Unit tests for `settings.Model`:** open, edit each field type, save, cancel, error state.
- **Unit tests for `config.SaveProjectConfig`:** roundtrip write/load, only essential fields are written, existing unrelated fields in `.marshal/config.toml` are preserved or overwritten only where the TUI touched them.
- **TUI integration test:** main model routes `Ctrl+O` to settings mode, propagates `settingsSavedMsg` into `session.State.Config`, and closes the overlay.
- **Manual test:** open Marshal in a project, change profile/model/local-only, save, and verify the status bar reflects the new route on the next turn.

## Acceptance criteria

- `Ctrl+O` opens the settings overlay from the main TUI.
- All in-scope settings can be edited and saved.
- Saved changes appear in `.marshal/config.toml`.
- `go test ./...` passes.
- The MVP checklist gains a new entry between Milestone N and Milestone O, e.g. `Milestone N.5: Settings TUI`.

## Relationship to other milestones

- Builds on Milestone B (TUI shell), Milestone C (provider abstraction), and Milestone L (role-based routing) because it edits profile/model/provider settings that those milestones introduced.
- Precedes Milestone O (swarm prototype) because swarm roles will expose more profile/preset switches that users will want to tune without restarting Marshal.
