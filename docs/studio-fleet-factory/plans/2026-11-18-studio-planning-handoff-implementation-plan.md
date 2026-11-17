# Studio planning, drafts and handoff — implementation plan

Date: 2026-11-18. Status: executable. Source: [PR 115](https://github.com/alecpullen/marshal/pull/115)
(the Flutter port plan [`2026-11-17-studio-planning-handoff-ux-plan.md`](2026-11-17-studio-planning-handoff-ux-plan.md))
and the approved product direction in
[`studio-planning-handoff-ux-spec.md`](../studio-planning-handoff-ux-spec.md).

Base: `main` at `bdd20ddc`. This plan turns the PR's three workstream tasks
into committed, individually verified work. It does not restate the product
direction; read the spec first.

## Goal

Make the planner, the working drafts and the factory handoff understandable
and truthful about their three different persistence and approval contracts:

1. a planning thread's live session and its captured snapshot,
2. a working draft's autosaved content,
3. a factory item's created draft and its later approval.

## Non-goals

- New backend conversions, new endpoints, or a new cross-device draft store.
- Silently copying one artifact into another, or claiming unsaved planner text
  survives a reload or reaches another device.
- Any change to the retiring Svelte Studio (`web/ui`, removed in F8).
- Reworking unrelated Studio screens.

## Verified facts about the current tree

These were read from `main` at the base commit. Do not re-derive them; do
re-check if a task's code no longer matches.

- `HandoffModel.approve()` posts `FactoryCreate` through the injected `create`
  route. In `main.dart` that route is `FactoryStore.create` →
  `BridgeClient.createFactoryItem` → `POST /api/factory/items`.
- The bridge's create path stores **a draft**:
  `web/bridge/factory_store.go:836` builds the item with `State: "draft"`.
  Work is started later by the `approve` command
  (`factory_http.go` `cmd.Kind == "approve"`, `FactoryStore.approve`). So the
  sheet's action label "Approve and start" overstates what the request does:
  it creates an unstarted factory draft item.
- The bridge already has the reviewed-evidence handoff route:
  `POST /api/planners/{id}/snapshots/{snapshotId}/handoff`
  (`web/bridge/planner_service.go` `handleHandoff`). It overwrites
  `draft.Plan` / `draft.Spec` with the snapshot's frozen artifacts, verifies
  every captured file against its recorded hash, copies the snapshot's
  `EditSnapshot` onto the draft, and returns `{"item": {...}}`. A replay of the
  same `requestId` returns the original item with 200 instead of 201.
- `FactoryCreate.editSnapshot` (`EditSnapshot{baseCommit, files, sha256}`)
  already exists in the generated Dart contract and in the Go wire type. The
  Go comment states it is "optional and absent on every draft a Studio surface
  creates" — this work is what gives it a Studio producer.
- The Dart client has `createPlannerSnapshot`, `plannerSnapshot` and
  `createFactoryItem`, but **no** snapshot-handoff method. Task 3 adds it.
- `PlannerSnapshotReply` carries `id`, `sha256`, `baseCommit`, `artifacts`,
  `files`, `createdAt`, `rejected`. `PlannerSnapshot` (model) drops
  `baseCommit`; Task 3 keeps it.
- `PlannerView` carries `requestedMode`, `confirmedMode`, `modeConfirmed`,
  `workspaceID`, `state`, `live`, `sessionID`, `occupancy`,
  `resumeAvailable`, `turns`, `requests`, `reason`, `endedAt`. The planning
  screen currently ignores all of it except `requestedMode`.
- `PlanningScreen` is built in `app/lib/src/ui/shell/shell.dart`
  (`_PlanningPlace`), which owns the handoff sheet call and returns the created
  item id through `onOpenItem`. `HandoffModel` is built in `app/lib/main.dart`
  (`handoffFor`).
- The kit (`app/lib/src/ui/kit/kit.dart`) provides `Box`, `Box.flush`, `Btn`,
  `Tip`, `Label`, `Field`, `Menu`, `MenuChoice`, `Sheet`, `StatusMessage`/
  `StateMessage`, `AskCard`, `Composer`, `OutputBlock`, `TurnPrompt`,
  `Hairline`, `PageBody`, `Glyph`/`Glyphs`, `Space`, and the `Ember` text
  roles. Screens may not import Material or Cupertino and may not set colours,
  sizes, weights, borders or radii of their own.

## Constraints for every task

- `docs/studio-flutter/design-system.md` is normative. Build only from the
  kit; take every colour, text role, spacing, radius and size from
  `tokens.dart`. If a needed piece is absent, extend the design system (§10:
  document, kit, gallery, component test and goldens together) instead of
  inventing a one-off in the screen.
- `ui/` never imports `client/`; state and operations stay in `model/`, which
  uses the generated wire types without hand-editing them. Do not edit
  `app/lib/src/client/contract.g.dart` by hand; regenerate through
  `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`
  if a Go wire type changes.
- Truthfulness is the point of the work: every label must match what the code
  actually does. Never claim durability, cross-device visibility, evidence or
  a started job the current seam does not provide.
- Preserve request identities, hashes, versions, conflict handling and
  no-blind-retry behaviour. No implicit resend, no automatic approval, no
  duplicate creation.
- No state may be lost by navigating, switching views or closing a sheet that
  the current code preserves.

## Task 1 — Clarify project-scoped journeys

**Goal:** the planner screen states its own connection, mode and workspace
truthfully, keeps pending requests next to the conversation, and survives the
round trip to the handoff sheet and back.

**Files:** `app/lib/src/ui/factory/planning_screen.dart`,
`app/lib/src/ui/factory/handoff_sheet.dart`,
`app/lib/src/model/planning_model.dart`,
`app/test/ui/factory/planning_test.dart`.

**Steps**

1. In `PlanningModel`, expose the session facts the screen needs, derived only
   from `PlannerView` (confirmed state, never a guess):
   - a connection state: live / ended / unknown, from `view.live`,
     `view.state` and `endedAt`;
   - the mode as words with *requested vs confirmed* kept apart:
     `modeConfirmed`, `confirmedMode`, `requestedMode`, using the existing
     `_modeWords` vocabulary ("Permissive", "Restricted");
   - where it runs (`workspaceID`) and whether the turn outcome is unknown
     (the model already appends a "Turn outcome unknown" line for
     `turn.uncertain`).
   Add these as getters; no new wire calls.
2. In `PlanningScreen`, replace the caller-supplied `subtitle` heuristic with a
   kit-built context strip placed next to the conversation and composer
   (spec: "put connection/mode/workspace state and actionable questions near
   the conversation and composer"). Use kit primitives only — `Label`, `Pill`,
   `Status`/`Dot`, `Hairline` — and words, not colour alone (design system
   §2). Show:
   - the connection state, with the requested-but-unconfirmed mode stated as
     such ("Restricted requested · not confirmed");
   - the workspace (and "no workspace" when the view states none);
   - the number of requests waiting on the person, when there are any.
   Keep `subtitle` optional and do not break `shell.dart`'s call.
3. Connect the journey without inventing one: the handoff action stays where
   it is, but the plan panel must say what handing off does and that the
   conversation stays reachable — the sheet returns to the same place. The
   composer's text (a `TextEditingController` in the screen's state) must
   survive opening and closing the handoff sheet; assert it.
4. Do not turn the handoff into a second approval here. Task 3 owns the
   action's wording and evidence.

**Verify** (from `app/`):

```bash
flutter test test/ui/factory/planning_test.dart test/model/planning_model_test.dart
```

Add regressions for: narrow (phone) and wide layout; pending requests shown
next to the composer; an unknown/disconnected turn not drawn as running; the
requested-but-unconfirmed mode; and the composer's text preserved across a
handoff round trip. Use `EmberApp` with a constrained surface for the narrow
case and deterministic fixtures, not a live bridge.

## Task 2 — Expose truthful draft persistence

**Goal:** a person can tell, without colour, whether the text on screen is on
the bridge (and therefore visible to another device), still only in this
editor, saving, failed, in conflict, read-only or gone.

**Files:** `app/lib/src/model/drafts_model.dart`,
`app/lib/src/ui/factory/draft_conflict.dart`,
`app/test/model/drafts_model_test.dart`.

**Steps**

1. Add a persistence state to `DraftsModel`, computed from what the model
   already knows (`_pending`, `_saving`, `_failure`, `_conflict`, `_draft`,
   the draft's `state`):
   - `localEdits` — content is queued for autosave, not yet acknowledged;
   - `saving` — a write is in flight;
   - `saved` — the bridge holds the version on screen (another device can see
     it);
   - `failed` — the last write did not reach the bridge (with `failure`);
   - `conflict` — the 409/404 outcomes the model already distinguishes;
   - `readOnly` — the draft's `state` is no longer `editing` (a converted
     draft).
   Expose it plus the words a screen needs: a short label and a one-sentence
   consequence. State plainly that nothing is claimed about surviving a
   reload: this bridge is the only durable copy.
2. In `DraftConflictBar` (or a sibling widget in the same file, exported), add
   the persistence line the draft editor shows while editing (not only under a
   conflict), built from kit primitives. Use words plus an icon/glyph, never
   colour alone. Under a conflict keep the existing both-versions panel and
   the existing two choices unchanged.
3. Do not change the autosave window, the versioned-write contract, the
   conflict resolution, or the delete/create behaviour. `saveNow()` must still
   flush pending content; `discard()` must still drop only local work.

**Verify** (from `app/`):

```bash
flutter test test/model/drafts_model_test.dart
```

Add regressions for: debounce coalescing and flush-on-close; a failed/offline
save leaving the text intact and reporting `failed`; two editors conflicting
twice in a row; deletion elsewhere; a converted draft being read only; and no
blind retry (the same request is not re-posted automatically).

## Task 3 — Review captured evidence and actual approval

**Goal:** the handoff sheet shows exactly the evidence a capture froze, binds
the submitted draft to that capture, and names the action and its consequence
accurately.

**Files:** `app/lib/src/model/planning_model.dart`,
`app/lib/src/model/handoff_model.dart`,
`app/lib/src/ui/factory/handoff_sheet.dart`,
`app/lib/src/client/bridge_client.dart`,
`app/lib/main.dart`,
`app/lib/src/ui/shell/shell.dart`,
`app/test/ui/factory/handoff_sheet_test.dart`,
`app/test/model/handoff_model_test.dart`.

(PR 115's task named `web/bridge/factory_http.go`. The route and its binding
semantics already exist and are correct; no Go change is expected. If an
implementation reason forces one, run `go test ./web/bridge/...` and the
contract currentness test, and regenerate the Dart contract only through its
generator.)

**Steps**

1. Keep the reviewed capture in the model. Extend `PlannerSnapshot` with
   `baseCommit` and keep `id`, `sha256`, artifacts (plan/spec) and `files` as
   the frozen evidence; the model already holds `latest`.
2. Give `HandoffModel` the reviewed evidence, optionally:
   - `reviewedSnapshot` (`PlannerSnapshot?`), `plannerID` (`String?`) and a
     handoff route
     `Future<FactoryItem> Function(String plannerID, String snapshotID, FactoryCreate request)?`;
   - when both are present, `approve()` posts through the snapshot handoff
     route, so the bridge's frozen bytes and verified file hashes are
     authoritative; otherwise it keeps the existing `create` route (the
     new-work dialog has no snapshot).
   - bind the request: build `editSnapshot` from the reviewed snapshot
     (`EditSnapshot(baseCommit: …, files: …, sha256: …)`) and use the
     snapshot's frozen plan/spec, never the sheet's editable copies, when a
     snapshot is bound.
3. Rename the action and word the consequence truthfully for the route that
   will run:
   - creating a factory item is **not** starting work. Label it
     "Create the factory draft" (or "Hand off to the factory") and say the
     work starts when the item is approved in the factory;
   - with a bound snapshot, say the draft is built from the frozen capture and
     that changing evidence requires a fresh capture.
   Update `consequence`, `blockers` wording and the sheet's action label
   together, and keep the disabled-with-tooltip behaviour.
4. Show the evidence in the sheet, from the snapshot only:
   - snapshot id (short) and digest, base commit;
   - every captured file with its operation, and its diff available through
     the kit's `OutputBlock`;
   - every rejected path with its reason;
   - "No files selected" when the capture is documents-only — **default
     selected files to none** and do not offer file selection the backend does
     not drive from this sheet.
   When no snapshot is bound, say so: the plan text shown is what will be sent.
5. A stale/changed capture must not be silently resubmitted: when the reviewed
   digest no longer matches a fresh capture the sheet asked for, surface the
   conflict and require a fresh review. Do not add a blind retry for an
   uncertain create — the request id makes a deliberate retry a replay, and the
   item the bridge returns is the item the screen links to.
6. Wire it up: `BridgeClient` gains
   `handoffPlannerSnapshot(plannerID, snapshotID, FactoryCreate)` posting to the
   snapshot handoff route and reading `{"item": …}`; `main.dart`'s
   `handoffFor` and `shell.dart`'s `_PlanningPlace` pass the planner id and the
   latest snapshot through.

**Verify** (from `app/`):

```bash
flutter test test/ui/factory/handoff_sheet_test.dart test/model/handoff_model_test.dart
```

Add regressions asserting exact payloads: the frozen plan/spec bytes and
`editSnapshot` (base commit, files, digest) on the snapshot route; the exact
request id; the returned item id is the one reported; documents-only captures
select nothing; rejected paths are shown; a failed create leaves the sheet
open with its failure and does not auto-retry; and the action's label and
consequence match the route that will run.

## Parallelisation and worktrees

Task 2 touches `drafts_model.dart`, `draft_conflict.dart` and
`drafts_model_test.dart`, which Task 1 and Task 3 do not touch, so it runs in
parallel in its own git worktree. Task 1 and Task 3 both own
`planning_model.dart` and `handoff_sheet.dart`, so they run in order in this
worktree. Branches merge back with no overlapping files.

## Final verification and integration

From `app/`:

```bash
flutter analyze
flutter test
dart format --output=none --set-exit-if-changed lib test integration_test
```

All must pass, including the design-rule, boundary, state, tooltip, overflow,
contrast and gallery tests. Gallery goldens are updated only for intentional
kit changes, on the pinned macOS environment
(`flutter test --update-goldens test/gallery_test.dart`). If a Go wire type or
bridge behaviour changed, from the repository root run
`go test ./web/bridge/...` and the contract currentness test.

Record the Flutter revision, the test results, and which evidence is fixture
based (widget/model tests) versus live (a real bridge, a physical phone). The
fixture tests are the evidence for this change; nothing here claims live
planner, cross-device or physical-device coverage.
