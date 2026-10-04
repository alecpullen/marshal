# Web Studio — docs index

The Web Studio reworks `web/` into a self-hosted studio for agents,
projects, workspaces and automations. It shares the TUI's view model, so
both clients show the same transcript.

- **Design:** [`design.md`](design.md), with mockups in [`mockups/`](mockups/).
- **Phase specs:** [`specs/`](specs/).
- **Implementation plans:** [`plans/`](plans/).

The plans follow the `marshal-writing-plans` skill and are split into a
backend track and a UI track (below):
- each task has a Goal, Files, Steps and Verify;
- each plan ends with a full-suite verification and a self-review.

## Execution order

The plans run on two tracks. The **backend track** (engine and bridge, all
Go) depends only on earlier backend plans. The **UI track** (`web/ui`,
Svelte and TypeScript) depends on the previous UI plan, plus a **gate**:
the backend plans of its own phase. The two tracks talk only through the
JSON contract (ACP and the bridge's HTTP and SSE routes), so once a
phase's backend is done, its UI can run while the next phase's backend
runs.

Every plan names its prerequisites under **Runs after** and refers to
earlier code by the names those plans define. Commit each task as
`<plan-slug>: task N — <title>`.

### Backend track

| # | Plan | Phase spec |
|---|---|---|
| B1 | [W1.1 Foundation backend](plans/2026-10-03-w1-1-backend-plan.md) | [W1](specs/2026-10-03-w1-foundation-design.md) |
| B2 | [W2.1 Session backend](plans/2026-10-03-w2-1-session-backend-plan.md) | [W2](specs/2026-10-03-w2-session-and-ship-design.md) |
| B3 | [W3.1 Engine](plans/2026-10-03-w3-1-engine-plan.md) | [W3](specs/2026-10-03-w3-runs-and-control-design.md) |
| B4 | [W3.2 Bridge: control agent, runs, library, models, usage, watches, roster](plans/2026-10-03-w3-2-bridge-control-plan.md) | W3 |
| B5 | [W4.1 Engine: workspace files, trust, policy](plans/2026-10-03-w4-1-engine-workspace-plan.md) | [W4](specs/2026-10-03-w4-workspaces-design.md) |
| B6 | [W4.2 Bridge: templates, builds, spawn, pools](plans/2026-10-03-w4-2-bridge-templates-builds-plan.md) | W4 |
| B7 | [W4.3 Bridge: secrets, credentials, egress proxy](plans/2026-10-03-w4-3-bridge-secrets-egress-plan.md) | W4 |
| B8 | [W5.1 Engine: hold, memory scopes, preview ports](plans/2026-10-03-w5-1-engine-plan.md) | [W5](specs/2026-10-03-w5-automations-and-ops-design.md) |
| B9 | [W5.2 Bridge: terminal, preview](plans/2026-10-03-w5-2-bridge-terminal-preview-plan.md) | W5 |
| B10 | [W5.3 Bridge: recipes, schedules, notifications, status links, memory routes](plans/2026-10-03-w5-3-bridge-recipes-schedules-plan.md) | W5 |
| B11 | [W5.4 Bridge: review bot, CI fixer](plans/2026-10-03-w5-4-bridge-automations-plan.md) | W5 |

### UI track

| # | Plan | Gate: backend plans that must be done first |
|---|---|---|
| U1 | [W1.2 Foundation UI](plans/2026-10-03-w1-2-ui-plan.md) | B1 |
| U2 | [W2.2 Session dock](plans/2026-10-03-w2-2-session-dock-plan.md) | B2 |
| U3 | [W2.3 Review & ship, New agent](plans/2026-10-03-w2-3-review-and-new-agent-plan.md) | B2 |
| U4 | [W3.3 Runs page, Live wall](plans/2026-10-03-w3-3-runs-and-live-plan.md) | B4 |
| U5 | [W3.4 Library, Models, Usage, Watches](plans/2026-10-03-w3-4-control-pages-plan.md) | B4 |
| U6 | [W4.4 UI: workspaces, builds, New agent](plans/2026-10-03-w4-4-ui-workspaces-plan.md) | B7 |
| U7 | [W4.5 UI: network inspector, projects, secrets](plans/2026-10-03-w4-5-ui-network-projects-plan.md) | B7 |
| U8 | [W5.5 UI: terminal, preview, recipes, schedules, notifications, memory, status](plans/2026-10-03-w5-5-ui-ops-plan.md) | B10 |
| U9 | [W5.6 UI: automations](plans/2026-10-03-w5-6-ui-automations-plan.md) | B11 |

Each UI plan also needs the UI plan above it.

### One person, one track at a time

Running everything serially works too. Any order that respects the gates
is valid; the simplest is phase by phase:
B1, U1, B2, U2, U3, B3, B4, U4, U5, B5, B6, B7, U6, U7, B8, B9, B10, U8,
B11, U9.

### Rules that keep the tracks separate

- Backend plans never edit `web/ui`.
- UI plans never edit bridge or engine Go code. They only rebuild
  `web/bridge/static` and run the bridge's asset and boundary tests.
- A route a UI plan needs belongs to that phase's bridge plan. For
  example, the Runs page's roster route is W3.2 Task 9.

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
| W1.1 | `stackView` |
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
