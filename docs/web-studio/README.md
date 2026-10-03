# Web Studio — docs index

The Web Studio reworks `web/` into a self-hosted studio for agents,
projects, workspaces and automations. It shares the TUI's view model, so
both clients show the same transcript.

- **Design:** [`design.md`](design.md), with mockups in [`mockups/`](mockups/).
- **Phase specs:** [`specs/`](specs/).
- **Implementation plans:** [`plans/`](plans/).

The plans follow the `marshal-writing-plans` skill:
- each task has a Goal, Files, Steps and Verify;
- each plan ends with a full-suite verification and a self-review.

## Execution order

Run the plans strictly in this order. Each plan assumes every plan above
it is complete, and names earlier symbols as those plans define them.
Commit each task as `<plan-slug>: task N — <title>`.

| # | Phase | Spec | Plan | Layer |
|---|---|---|---|---|
| 1 | W1 · Foundation | [spec](specs/2026-10-03-w1-foundation-design.md) | [W1](plans/2026-10-03-w1-foundation-plan.md) | Go + UI |
| 2 | W2 · Session & ship | [spec](specs/2026-10-03-w2-session-and-ship-design.md) | [W2.1 Session backend](plans/2026-10-03-w2-1-session-backend-plan.md) | Go |
| 3 | | | [W2.2 Session dock](plans/2026-10-03-w2-2-session-dock-plan.md) | UI |
| 4 | | | [W2.3 Review & ship, New agent](plans/2026-10-03-w2-3-review-and-new-agent-plan.md) | UI |
| 5 | W3 · Runs & control | [spec](specs/2026-10-03-w3-runs-and-control-design.md) | [W3.1 Engine](plans/2026-10-03-w3-1-engine-plan.md) | Go |
| 6 | | | [W3.2 Bridge: control agent, runs, library, models, usage, watches](plans/2026-10-03-w3-2-bridge-control-plan.md) | Go |
| 7 | | | [W3.3 Runs page, Live wall](plans/2026-10-03-w3-3-runs-and-live-plan.md) | UI |
| 8 | | | [W3.4 Library, Models, Usage, Watches](plans/2026-10-03-w3-4-control-pages-plan.md) | UI |
| 9 | W4 · Workspaces | [spec](specs/2026-10-03-w4-workspaces-design.md) | [W4.1 Engine: workspace files, trust, policy](plans/2026-10-03-w4-1-engine-workspace-plan.md) | Go |
| 10 | | | [W4.2 Bridge: templates, builds, spawn, pools](plans/2026-10-03-w4-2-bridge-templates-builds-plan.md) | Go |
| 11 | | | [W4.3 Bridge: secrets, credentials, egress proxy](plans/2026-10-03-w4-3-bridge-secrets-egress-plan.md) | Go |
| 12 | | | [W4.4 UI: workspaces, builds, New agent](plans/2026-10-03-w4-4-ui-workspaces-plan.md) | UI |
| 13 | | | [W4.5 UI: network inspector, projects, secrets](plans/2026-10-03-w4-5-ui-network-projects-plan.md) | UI |
| 14 | W5 · Automations & ops | [spec](specs/2026-10-03-w5-automations-and-ops-design.md) | [W5.1 Engine: hold, memory scopes, preview ports](plans/2026-10-03-w5-1-engine-plan.md) | Go |
| 15 | | | [W5.2 Bridge: terminal, preview](plans/2026-10-03-w5-2-bridge-terminal-preview-plan.md) | Go |
| 16 | | | [W5.3 Bridge: recipes, schedules, notifications, status links](plans/2026-10-03-w5-3-bridge-recipes-schedules-plan.md) | Go |
| 17 | | | [W5.4 Bridge: review bot, CI fixer](plans/2026-10-03-w5-4-bridge-automations-plan.md) | Go |
| 18 | | | [W5.5 UI: terminal, preview, recipes, schedules, notifications, memory, status](plans/2026-10-03-w5-5-ui-ops-plan.md) | UI |
| 19 | | | [W5.6 UI: automations](plans/2026-10-03-w5-6-ui-automations-plan.md) | UI |

"Later" (multiple users: accounts, sign-in, roles, team memory) is
deliberately not planned. Design §5.4 lists the groundwork W1–W5 lay for
it: owner fields, origins, single-operation actions, and per-browser
preference keys.

## Cross-plan facts

These numbers and names are set by earlier plans and used by later ones.

**`fleet.json` workspace version:**

| Version | Plan | Adds |
|---|---|---|
| 6 | Base | — |
| 7 | W2.1 | review comments |
| 8 | W3.2 | watch rules |
| 9 | W4.2 | agent workspace, container overrides, project settings |
| 10 | W4.3 | credentials |
| 11 | W5.3 | recipe on agent, schedules, notifications, status links |

**New ACP capabilities, by plan:**

| Plan | Capabilities |
|---|---|
| W1 | `stackView` |
| W2.1 | `stackNode`, `lastRequest`, `subagentStacks`, `filesView`, `commitDraft`, `stepDiffs` |
| W3.1 | `runDetail`, `watchAccess`, and `configAccess` (agent-level) |
| W4.1 | `workspaceFiles` (agent-level) |
| W5.1 | `holdControl`, `memoryScopes` |

**Known failing tests** on the base commit `2ddc09e`, all environmental:
- `internal/acp` `TestValidateWorkingPathsRejectsEtc`;
- `internal/app/tui` `TestSDDPlanPickerFlagsUnreadableLedger`;
- `internal/app/tui/connect` `TestOAuthLoginShowsAuthorizationURL`;
- `internal/llm/provider/limits` `TestFetchReturnsData`;
- `internal/sddplans` `TestDiscoverSurfacesUnreadableLedger`.

Every plan's Verify step treats these as known.

**The standard-library boundary:**
- All Go under `web/` stays standard library only, and imports nothing
  from `marshal/…` (`TestWebIsStdlibOnly`).
- New bridge code therefore lives in package `bridge`, never in a
  subpackage.
- TOML is parsed by the engine (W4.1), not the bridge.
