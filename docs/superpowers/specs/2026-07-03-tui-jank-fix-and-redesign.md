# TUI Jank Fix & UI/UX Streamline — Design Spec

**Date:** 2026-07-03  
**Status:** Draft (pending implementation plan)  
**Related docs:** `docs/06-tui-design.md`, `docs/02-system-architecture.md`

## 1. Problem statement

The Marshal TUI is janky and renders incorrectly. Users report:

- Layout/rendering defects: misaligned panels, overflow, flicker, clipping.
- Interaction/state issues: unclear focus, tab switching problems, scrolling/overlays that misbehave.

A parallel deep-dive analysis of `internal/app/tui`, `internal/app/session`, and the vision doc found the root causes are a mix of unsafe layout math, state mutation during rendering, missing synchronization with background agent work, and a large gap between the implemented two-column layout and the full UI vision.

## 2. Goals

1. Eliminate visible rendering defects at 80×24 and larger terminal sizes.
2. Make updates feel smooth and flicker-free while the agent is working.
3. Make focus, tabs, overlays, and keyboard navigation predictable.
4. Add tests that catch layout, focus, and interaction regressions.
5. Lay a clean foundation for the richer multi-panel UI described in `docs/06-tui-design.md`.

## 3. Non-goals

- Mouse support beyond basic scrolling.
- Custom theming or color palettes.
- A full accessibility audit.
- Re-implementing the underlying Bubble Tea framework.

## 4. Current state

The TUI is implemented in `internal/app/tui/model.go` as a single Bubble Tea model with a monolithic `Update` and `View`. Recent commits added:

- AltScreen mode.
- A two-column layout (chat left, sidebar tabs right).
- Tab switching, focus toggling, and keybind help.
- Settings and memory-browser overlays.
- Wiring to the agent runner.

### 4.1 Key issues found

**Layout / rendering**
- Geometry is computed in two places (`Update` for `WindowSizeMsg`, then again in `View`) and the two calculations disagree.
- The chat viewport is mutated inside `View()`, which violates Bubble Tea’s render contract and causes scroll drift / blank gaps.
- Border and padding frame sizes are not subtracted from container budgets, so content can overflow.
- Input width is hard-coded to 80 and never resized.
- Help text and status bar items are unbounded and can exceed terminal width.
- The no-diff approval banner draws its own box borders and is then wrapped in a second border, producing double borders.
- Overlays (settings, memory) use fixed frame widths and ignore terminal size.

**State / interaction**
- Global navigation keys (`Tab`, `Shift+Tab`, `Ctrl+P`, etc.) are evaluated before the pending-approval guard, so they leak into the approval modal.
- `Esc` during approval quits the app instead of denying the request.
- The `inputFocused` flag can drift from the real `textinput` focus state.
- Settings can be toggled with `Ctrl+O`; memory cannot be toggled with `Ctrl+K`.
- Many documented shortcuts are missing or mis-assigned (`Ctrl+P` opens Plan instead of a command palette, `Ctrl+R` is rollback instead of repo map, etc.).
- Provider errors are only rendered in the legacy fallback view, not the AltScreen dashboard.

**Performance**
- `refreshViewport()` rebuilds the entire transcript on every call.
- `agentTickMsg` re-renders the full layout every 150 ms but never refreshes the chat viewport, so streaming progress is invisible until the turn finishes.
- The agent goroutine mutates `session.State` while `View()` reads it without synchronization.

**Design gaps vs. `docs/06-tui-design.md`**
- Only Chat + three sidebar tabs exist; Plan, Diff, Tool Log, Context, Agents, Memory, and Config are not full panels.
- No mode indicator (Ask/Plan/Edit/Auto/Swarm).
- No command palette, model switcher, diff view, repo map, or agents panel.
- Status bar lacks role, model, provider, and context-token usage.

## 5. Design

### 5.1 High-level architecture

```
┌─────────────────────────────────────────────┐
│  tea.Program                                │
│    └── tui.Model (coordinator)              │
│          ├── layout.Geometry (sizes)        │
│          ├── focus.Stack (modal state)      │
│          ├── chat.Viewport                  │
│          ├── sidebar.Tabs (Plan/Context/Log)│
│          ├── overlays.Settings              │
│          ├── overlays.Memory                │
│          └── (future) panel.Manager         │
└─────────────────────────────────────────────┘
```

Principles:

- `View()` is read-only. No model field may be mutated inside `View()`.
- Layout geometry is computed once per `WindowSizeMsg` and stored on the model.
- Modal/overlays sit above the main layout and intercept keys first.
- Sub-models receive their own sizes and are responsible for their own borders/padding.
- Shared state writes from the agent either flow through `tea.Cmd` messages or are guarded by the existing mutex.

### 5.2 Phase 1 — Stabilize the current UI

Phase 1 keeps the existing two-column + tabs concept but fixes the bugs that make it janky.

#### 5.2.1 Single source of truth for layout

Add a `resize(width, height int)` helper on `Model` that is called from `Update(tea.WindowSizeMsg)` (and from `New` with sensible defaults). It computes:

- `m.width`, `m.height`
- `m.leftWidth = int(float64(width) * 0.70) - leftBorder`
- `m.rightWidth = width - m.leftWidth - divider - rightBorder`
- `m.contentHeight = height - statusHeight - inputHeight - helpHeight`
- `m.chatHeight = m.contentHeight - chatBorder`
- `m.input.Width = max(m.leftWidth - inputPadding, 1)`
- `m.viewport.Width = max(m.leftWidth - chatBorder, 1)`
- `m.viewport.Height = max(m.chatHeight, 1)`

All values are clamped to `>= 1`. If the terminal is below a usable minimum (e.g., width < 40 or height < 10), the model falls back to a minimal placeholder view.

`View()` reads these stored values and never assigns to `m.viewport` or `m.input`.

> The exact border/padding constants (`leftBorder`, `rightBorder`, `chatBorder`, etc.) will be derived from the lipgloss styles used for each panel. The implementation plan will enumerate the concrete formulas and minimum sizes.

#### 5.2.2 Fix viewport refresh and performance

- Add dirty counters `lastMessageCount`, `lastAuditCount`, `lastContextPackVersion` to `Model`.
- `refreshViewport()` compares counters and rebuilds only when something changed.
- `agentTickMsg` calls `refreshViewport()` before re-queueing the tick, so streaming content becomes visible.
- Stop scheduling `tickCmd()` when the model is not busy.
- Consider caching the active sidebar body string and rebuilding it only when the active tab or underlying data changes.

#### 5.2.3 Fix focus and key routing

- Introduce a lightweight focus rule: overlays and approval prompts capture keys first.
- Move `Tab`/`Shift+Tab`, `Ctrl+P`/`Ctrl+X`/`Ctrl+T`, and `Ctrl+R` handling so it runs only when no modal/approval is active.
- `Esc` during approval sends `UserApprovalDecision{Approved: false}` and clears the prompt.
- Keep `inputFocused` in sync with `m.input.Focus()`/`Blur()`; prefer `m.input.Focused()` where possible.
- Make `Ctrl+K` toggle the memory browser; `Ctrl+O` toggles settings.
- Render `ProviderError()` in the AltScreen layout (e.g., a one-line error banner above the status bar).

#### 5.2.4 Fix overlays

- Rewrite the settings frame renderer so every content row is padded/truncated to the inner width and has both left and right borders.
- Add `width`/`height` to `settings.Model` and `memory.Model`; handle `tea.WindowSizeMsg` inside them.
- Propagate terminal size from the main model to the active overlay.
- In settings, recompute the active preset’s provider/model/local-only values when the Default profile changes.
- Add a viewport/scroll offset to the memory browser so long lists do not overflow.

#### 5.2.5 Fix status bar and help text

- Constrain help text to `leftWidth - 2`; truncate or switch to a shorter hint on narrow terminals.
- Render `IDLE` with the same style/width as `WORKING` so toggling busy does not change the bar’s geometry.
- Cap the status bar at `m.width`; truncate `cwd`/`project` if necessary.

#### 5.2.6 Fix approval UI

- Build the no-diff approval banner as plain text and let `lipgloss` draw the single rounded border.
- Truncate command/reason/risk to fit.
- Show human-readable risk text when available.
- Make the “always allow” label context-aware (e.g., `[a] Always allow "go test"`).

### 5.3 Phase 2 — Streamlined multi-panel UI

Phase 2 moves from the two-column dashboard to the richer layout in `docs/06-tui-design.md`. It should only start after Phase 1 is merged and stable.

#### 5.3.1 Panel manager

Introduce `internal/app/tui/panel` (or similar) with:

- A `Panel` interface: `Init()`, `Update(tea.Msg)`, `View(width, height int) string`, `Focus()`/`Blur()`, `IsFocused() bool`.
- A `Manager` that owns a grid of panels and routes keys/focus between them.
- The main `tui.Model` becomes a thin wrapper around the manager plus global overlays.

Initial panels:

| Panel | Responsibility |
|---|---|
| Chat | Conversation transcript + input. |
| Plan | Live plan steps from `contextpack.SectionPlan`. |
| Diff | Proposed / applied patches. |
| Tool Log | Audit log of tool calls. |
| Context | Current context pack with detail and item removal. |
| Agents | Swarm agent state. |
| Memory | Durable project facts. |
| Config | Settings form. |

#### 5.3.2 Layout grid

Default grid (configurable later):

```
┌────────────── Chat ──────────────┬────────── Context ──────────┐
├────────────── Diff ──────────────┼────── Plan + Tool Log ──────┤
└───────────── Input / Status Bar ───────────────────────────────┘
```

The manager computes panel sizes from terminal dimensions and delegates rendering.

#### 5.3.3 Mode and command palette

- Add `mode` to `Model` (Ask/Plan/Edit/Auto/Swarm) and render it in the status bar.
- Implement a command palette overlay bound to `Ctrl+P`.
- Implement `Ctrl+M` (model/profile switcher), `Ctrl+D` (diff panel focus), `Ctrl+R` (repo map), `Ctrl+A` (agents panel), `Ctrl+Y`/`Ctrl+N` for approvals, `Ctrl+E` for editing, `Ctrl+S` for saving a session summary.
- Update help text dynamically to match the current mode and focus.

#### 5.3.4 Status bar

Show:

```
MARSHAL | <mode> | <role> | <model> @ <provider> | local | ctx <usage>/<max> | <busy/idle>
```

Truncate fields gracefully on narrow terminals.

### 5.4 Agent → TUI state synchronization

Two options were considered:

1. **Message-based:** `agent.Runner` emits `tea.Msg`s (e.g., `AgentMessageMsg`, `ToolLogMsg`, `PlanUpdatedMsg`) that the model handles in `Update`. This removes direct mutation from the render path but requires changes inside the agent loop.
2. **Mutex + dirty counters:** Keep the existing mutex-protected `session.State`, add dirty counters to the TUI model, and rebuild the viewport only when counters change.

**Decision for Phase 1:** Use option 2. `session.State` already copies slices under lock in `Messages()`, `AuditLog()`, and `ContextPack()`, so reads are safe. Dirty counters eliminate wasteful rebuilds and make streaming content visible during `agentTickMsg`. Phase 2 can re-evaluate message-based updates when the panel manager is introduced.

## 6. Testing strategy

### 6.1 Unit tests

- **Layout bounds:** Set `width=80, height=24`, call `View()`, assert total height == 24 and no line exceeds 80 cells.
- **Resize:** Send `tea.WindowSizeMsg{Width:120, Height:40}` and assert `m.input.Width`, `m.viewport.Width/Height`, and column widths scale correctly.
- **Focus/tabs:** Esc unfocuses, Enter focuses, Tab cycles only when no modal, number keys switch tabs only when unfocused.
- **Modal key capture:** While approval is pending, `Tab`, `Ctrl+P`, `Ctrl+X`, `Ctrl+T`, `Ctrl+R` do not leak through.
- **Esc denies approval:** `Esc` sends `Approved: false` and does not quit.
- **Overlay toggle:** `Ctrl+O` and `Ctrl+K` open and close their overlays.
- **Overlay sizing:** Settings/memory views fit within the terminal at 80×24.
- **Provider error visibility:** Error text appears in the AltScreen view.
- **Busy refresh:** A fake runner that appends messages during `Run` causes those messages to appear before `agentFinishedMsg`.
- **Concurrency:** Run `go test -race ./internal/app/tui/...` with a goroutine mutating state while the model renders.

### 6.2 Snapshot / golden tests

Add golden-file tests for:

- Normal dashboard at 80×24.
- Approval banner with and without diff.
- Settings and memory overlays.

These catch unintended layout drift.

## 7. Rollout plan

1. **Phase 1 PR(s)** — address stabilization. Suggested PR split:
   - PR 1: layout geometry + `View()` purity + resize tests.
   - PR 2: focus/key routing + approval UI fixes + interaction tests.
   - PR 3: overlay responsiveness + settings/memory fixes + overlay tests.
   - PR 4: performance / refresh + busy-streaming tests.
2. **Merge Phase 1**, dog-food, and gather feedback.
3. **Phase 2 design refinement** — finalize panel grid and keymap based on Phase 1 usage.
4. **Phase 2 PR(s)** — panel manager, individual panels, mode/palette, status bar.
5. **Final review** — update `docs/06-tui-design.md` if any shortcuts or layouts changed.

## 8. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Moving geometry out of `View()` breaks existing tests. | Update tests to send `WindowSizeMsg`; assert bounded output instead of exact fallback strings. |
| `Esc` behavior change surprises users. | Update on-screen help; keep `Ctrl+C` as quit. |
| Phase 2 scope grows. | Keep Phase 2 behind a feature branch/flag; deliver Phase 1 first. |
| Agent-state races persist. | Add `go test -race` to CI; prefer message-based updates in Phase 2. |

## 9. Open questions

- Should Phase 1 switch the agent loop to message-based updates, or keep mutex + dirty counters?
- Which panels should be visible by default in Phase 2, and should the grid be user-configurable immediately?
- Should the command palette replace some global shortcuts, or supplement them?

---

*Mockup: see the visual companion session at `.superpowers/brainstorm/44096-1783084036/content/layout-proposal.html`.*
