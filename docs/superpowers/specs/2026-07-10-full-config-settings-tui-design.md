# Full Config Settings TUI Design

## Goal

Replace the current single-screen settings form (a flat `huh.Form` covering a
subset of `config.Config`) with a two-pane settings TUI that lets users view
and edit **every** section of `.marshal/config.toml` from inside Marshal, with
no need to hand-edit TOML.

The existing settings overlay (`/settings` command, `Ctrl+O`-equivalent routing)
is the entry point. This design supersedes the v1 settings design in
`2026-07-02-settings-tui-design.md`: the public package API, save target, and
parent-TUI wiring are preserved; only the internal model and the breadth of the
save path change.

## Scope

### In scope

- A two-pane overlay: a left sidebar listing all config sections and a right
  pane rendering the focused section's fields.
- Editing the **full** `Config` surface:
  - Scalar sections: `agent` (profile select + agent knobs), `privacy`,
    `snapshots`, `web`, `commands` (test/format/vet + project name/languages),
    `indexing`.
  - Shell + sandbox: `shell` (timeouts, output limits, booleans, guardrail
    argv0, allow/confirm/deny command lists) and `sandbox` (backend select,
    resource limits, container runtime/image, allow/deny env lists).
  - Collection/map sections with in-TUI list editors: `providers` (named
    entries with masked API key + APIKeyEnv), `presets` (model presets),
    `mcp` (servers, policies, disclosure threshold), `hooks` (entries),
    `permissions` (rules), `swarm` (budget scalars + tool_iters map),
    `diagnostics` (lang→command map).
- Atomic save of all edited sections to `.marshal/config.toml` via an extended
  `SaveProjectConfig`, preserving unrelated sections already in the file.
- Reload-on-save via the existing `SavedMsg` path (parent swaps
  `state.Config` and calls `configReloader`).
- Masked display of secret fields (`providers.APIKey`, `web.SearchKey`) with a
  preference hint for `APIKeyEnv`.
- Soft validation warnings for risky option combinations (rendered inline, do
  not block save).
- Dirty indicator and two-level Esc (close sub-form first, then overlay).

### Out of scope

- Editing `agent_profiles` (the role→preset map) and per-role
  `agents.<role>.context` budgets as dedicated editors. These advanced sections
  are shown read-only with a hint to edit config.toml directly. (Follow-up.)
- Saving to the user config (`~/.config/marshal/config.toml`). Only the
  project-local file is written. (Future: choose-target per save.)
- mtime conflict detection on save.
- Undo within the settings session (partial edits). Full cancel discards all.
- A separate CLI entry point (`marshal --settings`).

## Architecture

Replace `internal/app/tui/settings/` **in place**. The public API
(`New`, `Model`, `Update`, `View`, `SetSize`, `Init`, `SavedMsg`,
`CancelledMsg`, `FocusedFieldTitle`, `Footer`, `BoolValue`) is unchanged so the
parent TUI wiring in `internal/app/tui/model.go` and `view.go` needs zero edits.
`SaveProjectConfig` is extended in `internal/app/config/save.go`.

### Top-level model (`settings.Model`)

- `state *state` — heap-allocated (so pointer bindings survive `Model` value
  copies, same trick as the current model). Holds the working copy of
  `config.Config`, a snapshot for dirty detection, and per-section field
  buffers.
- `sidebar` — ordered list of section keys + display titles; `cursor int`.
- `pane` — the currently focused section's renderer (`sectionPane` value).
- `width`, `height`, `workingDir`, `projectCfgPath`, `footer`.
- `dirty bool` — set when `state.cfg` diverges from the snapshot taken at
  `New()`.
- `helpOpen bool` — `?` toggles a keybinding cheatsheet for the active pane.

### Layout

Rendered with lipgloss into the full terminal area (same placement as today):

```
┌─ Settings ──────────────────────────── Ctrl+S save · Esc cancel ─┐
│ Agent          │ Default profile    [local_balanced        ▾]   │
│ Providers    ▸ │ Provider           <name>                     │
│ Model Presets  │ Model              <model id>                  │
│ Privacy        │ Max tool iters     40                          │
│ Shell          │ Max retries        3                           │
│ Sandbox        │ Plan first         [✓]                        │
│ Indexing       │ Remote providers   [ ]                        │
│ Web            │                                                       │
│ Swarm          │                                                       │
│ MCP            │                                                       │
│ Snapshots      │                                                       │
│ Hooks          │                                                       │
│ Permissions    │                                                       │
│ Diagnostics    │                                                       │
│ Commands       │                                                       │
└── Agent ───────────────────────────────────────────────────────┘
```

- Left sidebar: fixed ~16–18 chars; right pane = `width − sidebar − borders`.
- Footer line: save/error status; `* modified` indicator when `dirty`.
- The right pane header shows the active section title and any soft warnings
  for that section.

### Focus model

- `↑`/`↓` or `k`/`j` move the sidebar cursor (when focus is on the left).
- `Tab` / `l` / `→` enter the right pane's active form.
- `h` / `Shift-Tab` / `←` return to the sidebar.
- `g` / `G` jump to first/last section.
- Inside a scalar form, field navigation uses huh's own `↑`/`↓`.

## Config sections and field mapping

The sidebar maps 1:1 to `Config` struct sections. Each section pane uses `huh`
widgets for scalars; collection sections use an entry-list + sub-form.

| # | Section key | Config fields | Editor |
|---|---|---|---|
| 1 | `agent` | `Profile.Default` (select), `Agent.{Provider,Model,MaxToolIterations,MaxRetries,MaxTurnContextTokens,PlanFirst,SubtaskIterations}` | scalars form |
| 2 | `providers` | `Providers` map: per entry `{Type,BaseURL,APIKey(masked),APIKeyEnv,ToolCalling}` | list + sub-form |
| 3 | `presets` | `Models.Presets` map: per entry `{Provider,Model,ContextWindow,MaxOutputTokens,Temperature,TopP,ToolCalling,ReasoningEffort,LocalOnly}` | list + sub-form |
| 4 | `privacy` | `Privacy.{RemoteProvidersAllowed,RedactSecrets,IncludeGitignoredFiles}` | scalars form |
| 5 | `shell` | `Tools.Shell.{DefaultTimeoutSeconds,MaxOutputBytes,MaxBackgroundJobs,BackgroundRetention,AllowNetwork,AllowSudo,AllowDestructive,AutoApprove,GuardrailDynamicArgv0,Allow.Commands,Confirm.Commands,Deny.Patterns}` | scalars + list-of-strings |
| 6 | `sandbox` | `Tools.Shell.Sandbox.{Backend(select),MemoryLimitMB,CPUSeconds,MaxProcesses,FileSizeLimitMB,ContainerRuntime,ContainerImage,AllowFallback,EnvAllowlist,EnvDenylist}` | scalars + list-of-strings |
| 7 | `indexing` | `Indexing.{UseTreesitter,UseEmbeddings,SummariseFiles,Ignore[]}` | scalars + list-of-strings |
| 8 | `web` | `Web.{Enabled,FetchTimeout,SearchProvider,SearchURL,SearchKey(masked)}` | scalars form |
| 9 | `swarm` | `Swarm.Budget.{MaxFixRounds,MaxTotalTokens,ToolIters(map)}` | scalars + map editor |
| 10 | `mcp` | `MCP.{Servers(map: Command,Args[],Env map),Policies(map),DisclosureThresholdTools}` | list + sub-form |
| 11 | `snapshots` | `Snapshots.{Enabled,RetentionDays,MaxFileBytes}` | scalars form |
| 12 | `hooks` | `Hooks.{FailClosed,Entries[](Event,Matcher,Command,TimeoutMS)}` | list + sub-form |
| 13 | `permissions` | `Permissions.Rules[](Permission,Pattern,Action)` | list + sub-form |
| 14 | `diagnostics` | `Diagnostics.Commands(map: lang→cmd)` | map editor |
| 15 | `commands` | `Commands.{Test,Format,Vet}` + `Project.{Name,Languages[]}` | scalars + list-of-strings |

### Collection / list editors (providers, presets, mcp, hooks, permissions)

The right pane shows existing entries as a selectable list with a `▸` marker.

| Key | Action |
|---|---|
| `↑`/`↓` | Move entry cursor |
| `a` | Add entry (prompt for name/key, open sub-form) |
| `e` / Enter | Edit selected (open sub-form) |
| `d` | Delete selected (confirm) |
| `h`/`Shift-Tab` | Return to sidebar |

The sub-form is a `huh.Form` rendered in the same right pane (it replaces the
list while active). Submitting commits the entry into the working copy and
returns to the list; Esc discards and returns to the list. Sub-forms edit a
local copy and commit on submit, so cancel never mutates the working copy.

### List-of-strings editor (shell allow/confirm/deny, sandbox env lists, indexing ignore, project languages)

An inline editable list widget.

| Key | Action |
|---|---|
| `↑`/`↓` | Move row cursor |
| `a` | Append row (prompt for value) |
| Enter | Edit row inline |
| `d` | Delete row |

### Map editor (swarm tool_iters, diagnostics commands)

Key/value list.

| Key | Action |
|---|---|
| `↑`/`↓` | Move entry cursor |
| `a` | Add key/value |
| `e`/Enter | Edit value (key read-only after creation) |
| `d` | Delete entry |

### Masked secret fields

- `providers.APIKey` and `web.SearchKey` render as `••••<last4>` when
  non-empty. The masked display is read-only.
- Pressing `e` enters edit mode (the field clears so the user can type a new
  value). Saving writes the real value, not the mask.
- A note under each secret field: "Prefer the env-var field to avoid storing
  secrets in config."
- `providers.APIKeyEnv` is the preferred editable field (plain text env var
  name) and is shown alongside the masked key.

### Validation / soft warnings

A non-blocking amber warnings line rendered in the right pane header for the
active section. Warnings never prevent save.

- Remote providers allowed but no provider configured (warn).
- `AllowSudo && AutoApprove` (warn: sudo runs without confirmation).
- `AllowDestructive && AutoApprove` (warn).
- Sandbox backend `container` but `ContainerImage` empty and no runtime
  detected (warn: will fall back or error at runtime).
- Provider `APIKey` non-empty (info: secret stored in plaintext).

## Save path and reload

### Working copy

`state.cfg` is the single mutable `config.Config`. Each section's widgets bind
to fields on `state.cfg` by pointer. `dirty` is set whenever a field callback
mutates `state.cfg` away from the snapshot taken at `New()`.

### Atomic save (Ctrl+S)

1. `Model.saveCmd()` calls the **extended** `SaveProjectConfig(path,
   state.cfg)` — extended to persist every editable section (it currently
   persists only profile/agent/privacy/shell/sandbox/preset). Newly covered:
   providers, commands/project, web, snapshots, MCP, hooks, permissions,
   diagnostics, indexing, swarm.
2. Each section is written **only if it differs from `Default()`**, to avoid
   bloating the file with default-valued sections.
3. On success: reload via `config.Load(...)` and emit `SavedMsg{Cfg: loaded}`.
   The parent swaps `state.Config` and calls `configReloader` (existing path).
   `dirty` resets to false.
4. On failure: set `footer` to the error; keep the overlay open so the user can
   fix and retry. `dirty` stays true.

### Preserve unrelated sections

`SaveProjectConfig` already loads the existing file into `configFile` and only
overwrites the sections it manages, leaving unknown sections intact. The
extension follows the same pattern: read the existing `configFile`, overlay the
editable sections, write back. Sections Marshal does not understand are
preserved verbatim.

### No-save-on-cancel

Esc emits `CancelledMsg`; the parent closes the overlay without touching the
file. The working copy is discarded.

### Reload conflict

If the file changed on disk between `New()` and save (e.g. edited in another
terminal), the save still overwrites with the working copy — this matches
current behavior. mtime conflict detection is a known limitation (out of scope).

## Key bindings and navigation

### Global (handled before routing into forms)

| Key | Action |
|---|---|
| `Ctrl+C` | Quit — handled by parent, never reaches settings |
| `Esc` | Cancel deepest open thing first (sub-form → list → overlay) |
| `Ctrl+S` | Save all → `saveCmd()` |
| `?` | Toggle help overlay (cheatsheet for the active pane) |

### Sidebar (focus on left)

| Key | Action |
|---|---|
| `↑`/`↓` or `k`/`j` | Move section cursor (clamped) |
| `Tab`/`l`/`→` | Enter right pane's active form |
| `g`/`G` | Jump to first/last section |

### Right pane — scalar form sections

| Key | Action |
|---|---|
| `↑`/`↓` | Previous/next field |
| `Space` | Toggle boolean |
| `h`/`Shift-Tab`/`←` | Return to sidebar |
| `Ctrl+S` | Save (global) |

### Right pane — collection sections

| Key | Action |
|---|---|
| `↑`/`↓` | Move entry cursor |
| `a` | Add entry (sub-form) |
| `e`/Enter | Edit selected (sub-form) |
| `d` | Delete selected (confirm) |
| `h`/`Shift-Tab` | Return to sidebar |

### Right pane — list-of-strings sections

| Key | Action |
|---|---|
| `↑`/`↓` | Move row cursor |
| `a` | Append row |
| Enter | Edit row inline |
| `d` | Delete row |
| `h`/`Shift-Tab` | Return to sidebar |

### Right pane — map sections

| Key | Action |
|---|---|
| `↑`/`↓` | Move entry cursor |
| `a` | Add key/value |
| `e`/Enter | Edit value |
| `d` | Delete entry |
| `h`/`Shift-Tab` | Return to sidebar |

### Sub-form (within right pane)

| Key | Action |
|---|---|
| `↑`/`↓` | Navigate fields |
| Enter | Submit → commit to working copy, return to list |
| Esc | Cancel → discard, return to list (does NOT close overlay) |

### Esc semantics (two levels)

The first Esc closes any open sub-form or inline edit and returns to that
section's list. A second Esc (when at a section's top-level list or scalar form)
emits `CancelledMsg` and closes the overlay.

### Keymap construction

A single `huh.KeyMap` is reused for all scalar sub-forms (same as today), with
`Ctrl+S` as submit. The sidebar and list navigation use raw `tea.KeyPressMsg`
switches in `Update`, not a huh keymap — those panes are not huh forms.

## File layout

```
internal/app/tui/settings/
  messages.go            — SavedMsg, CancelledMsg (unchanged)
  model.go               — top-level Model: New, Update, View, SetSize, Init,
                          saveCmd; sidebar list, cursor, dirty tracking,
                          two-pane layout, Esc-level handling, ? help overlay
  sections.go            — section registry: ordered list of section
                          keys/titles; per-section pane factory
  section_agent.go       — scalar form for agent + profile select
  section_privacy.go     — scalar form for privacy
  section_shell.go       — scalar form + list-of-strings for shell rules
  section_sandbox.go     — scalar form + env allow/deny lists
  section_snapshots.go   — scalar form
  section_web.go         — scalar form (masked search key)
  section_indexing.go    — booleans + ignore list
  section_commands.go    — test/format/vet + project name/languages
  section_collection.go  — generic list+sub-form renderer used by:
  section_providers.go   —   providers (masked APIKey)
  section_presets.go     —   model presets
  section_mcp.go         —   MCP servers + policies + threshold
  section_hooks.go       —   hooks entries
  section_permissions.go —   permission rules
  section_map.go         — generic map editor (swarm tool_iters, diagnostics)
  validation.go          — soft-warning rules; returns []string for active section
  masked.go              — maskKey/unmaskKey helpers for secret display
  liststrings.go         — inline list-of-strings widget
  model_test.go          — existing tests updated + new coverage
```

## Testing

### Unit tests

- `model_test.go` (existing): update the 8 tests that assume the flat
  single-form (e.g. `FocusedFieldTitle` returns the active section's field, not
  always "Default profile"). Keep `BoolValue` working for agent-section fields.
- New per-section tests: each `section_<x>_test.go` builds the pane, sends key
  sequences, asserts `state.cfg` mutation. Isolated — no TUI program needed.
- Collection editor tests: add/edit/delete entries via key sequences; assert
  working-copy map mutations and sub-form commit/discard.
- Masked-key tests: assert masked display format, that editing sets the real
  value, and that `SaveProjectConfig` writes the real (not masked) value.

### Save path tests

`internal/app/config/save_test.go` extended: round-trip every editable section
through `SaveProjectConfig` → `Load` → assert field equality. This is the
critical regression guard — ensures the extended save never drops a section or
writes malformed TOML. Also assert default-valued sections are omitted from the
written file.

### Esc-level tests

Esc in a sub-form returns to list (no `CancelledMsg`); Esc at section top
emits `CancelledMsg`.

## Error handling

- **Save failure:** set `footer` to `Save failed: <err>`, keep overlay open,
  `dirty` stays true. User fixes and retries.
- **Reload failure:** set `footer` to `Reload failed: <err>` but keep the
  working copy (same as today's path). The parent's `configReloader` error path
  already handles provider error display.
- **Invalid field input:** huh validators return errors inline (e.g. non-numeric
  in an int field). The form blocks submit until valid. Collection sub-forms
  validate the entry key (no empty name, no duplicate key within that section).
- **Validation warnings:** rendered as a non-blocking amber line in the right
  pane header. Never prevent save.
- **Sub-form cancel:** discards the in-progress entry edit; the working copy is
  untouched (sub-forms edit a local copy and commit on submit).

## Known limitations

- No mtime conflict detection on save.
- No undo within the settings session (once `dirty`, only a full cancel
  discards).
- `agent_profiles` (the role→preset map) and per-role `agents` context budgets
  are advanced config not surfaced as dedicated editors in this iteration —
  shown read-only with a hint to edit config.toml directly. Follow-up.
- The `/` section filter was considered but dropped from this iteration to keep
  the model simple; can be added later without affecting the rest.

## Acceptance criteria

- `/settings` opens the two-pane settings overlay from the main TUI.
- All 15 sections are navigable and editable.
- Saved changes appear in `.marshal/config.toml`; unrelated sections are
  preserved.
- `go test ./...` passes (existing settings tests updated, new section + save
  tests added).
- The parent TUI wiring (`model.go`, `view.go`) is unchanged beyond what
  already exists.
- `gofmt -w .` and `go vet ./...` pass.