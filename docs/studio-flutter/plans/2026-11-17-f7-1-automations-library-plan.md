# F7 plan 1: the list pattern, Automations and Library — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495`. Runs after
[the F2 plan](2026-11-17-f2-shell-plan.md); independent of F3–F6. Followed
by [F7 plan 2](2026-11-17-f7-2-settings-plan.md).

Approved design:
[f7-automations-library-settings.md](../f7-automations-library-settings.md),
decisions 1–5 and 9. Rules: [design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. Re-read the handlers named below at the current `main`
before Task 1.

## Goal

- One list-and-detail pattern used by every section in F7.
- Automations: bots, schedules, watches, recipes.
- Library: skills, plugins, MCP, memory.

## Non-goals

- Settings (plan 2). New capabilities.

## Assumptions

- Routes: `/api/schedules` (list, create, `{id}` put and delete,
  `{id}/run`); `/api/watches` (list, create, `{owner}/{id}` delete) and
  `/api/reroutes/{id}/undo`; `/api/recipes` (list, `{name}` get, put,
  delete, `copy`, `run`); `/api/automations/review/run`,
  `…/review/drafts` (list, `{id}` get and put, `post`, `send-to-author`,
  `discard`), `…/ci/history` and `…/ci/history/{id}`;
  `/api/library/memory` (list, `suggestions`, `{id}` delete, `promote`,
  `confidence`); `/api/library/skills` with `preview`, `confirm`,
  `discard` and delete; `/api/clients` (list, create, delete,
  `{clientId}/factory-grants/{projectId}`).
- The Svelte views: `views/Schedules.svelte`, `views/Watches.svelte`,
  `views/automations/ReviewBot.svelte`, `CIFixer.svelte`,
  `views/Library.svelte`, `lib/library/`, `lib/ClientsPanel.svelte`.
- The kit has `Rows`, `Sheet`, `Field`, `Tabs`, `Spark`.

## Decisions this plan makes beyond the spec

- A detail opens in a `Sheet` at 760 and wider and as a pushed page below.
- A section's model extends one base class, so save, stale-save and
  unsaved-edit behaviour is written once.
- The review bot's held drafts become asks of kind `review_draft`.

## Execution

- Branch `f7-1-automations-library`. Commit each task as
  `f7-1-automations-library: task N — <title>`, explicit paths only.

## Contracts

Contract files, each added by the task that needs it: `schedules.go`,
`watches.go`, `recipes.go`, `automations.go`, `library.go`, `clients.go`,
declaring with today's JSON what `lib/api.ts` hand-writes for these routes.

```dart
// model/section_model.dart
enum SaveState { clean, dirty, saving, failed, stale }
abstract class SectionModel<T> extends ChangeNotifier {
  List<T> get items; bool get loading; String? get error;
  T? editing; SaveState saveState; String? saveError;
  Future<void> load(); void edit(T item); void change(T next);
  Future<void> save(); void cancel(); Future<void> reloadAfterStale();
  bool get hasUnsavedEdits;
}

// ui/sections/section_page.dart
class SectionPage<T> extends StatelessWidget { /* title, subtitle, tabs, Rows, detail builder */ }
```

`AskKind` gains `review_draft`.

## Task 1 — the pattern

**Files:** `app/lib/src/model/section_model.dart`,
`app/lib/src/ui/sections/section_page.dart`, `detail_host.dart`,
`unsaved_guard.dart`, tests, a gallery specimen.

1. `SectionPage` draws `PageBody`, optional `Tabs`, `Rows`, and opens the
   detail per Decisions with Save and Cancel.
2. Save shows saving, then the bridge's refusal in place; a stale refusal
   keeps the edit and offers Reload. Leaving with unsaved edits asks first,
   through one guard the shell consults.
3. Tests: `save shows the refusal in place`,
   `a stale save keeps the edit and offers reload`,
   `leaving with edits asks first`, `detail is a sheet at 760 and a page
   below`.

**Verify:** `cd app && flutter test`.

## Task 2 — schedules and recipes

**Files:** `web/bridge/contract/schedules.go`, `recipes.go`, fixtures,
generated files, `app/lib/src/model/schedules_model.dart`,
`recipes_model.dart`, `app/lib/src/ui/automations/automations_place.dart`,
`schedules_tab.dart`, `recipes_tab.dart`, `placeholder.dart`, tests,
`screens.dart`.

1. The Automations place with tabs Bots, Schedules, Watches, Recipes; Bots
   and Watches show the planned state until Tasks 3 and 4.
2. Schedules: list, create, edit, run now, delete (confirms). Recipes:
   built-in and the user's, with copy, edit, run; a built-in cannot be
   edited, only copied.
3. Recipes are offered in the new-work dialog from this model when F3 has
   landed.
4. Tests: `create posts a schedule`, `run now posts`, `delete confirms`,
   `a built-in recipe is copy only`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 3 — watches

**Files:** `web/bridge/contract/watches.go`, fixtures, generated files,
`app/lib/src/model/watches_model.dart`,
`app/lib/src/ui/automations/watches_tab.dart`, Overview, tests.

1. Rows: source, condition, a day's `Spark`, action on trip, state. A
   watch an agent started names the agent. A tripped watch's detail shows
   what it did, and Undo when the action was a reroute.
2. Tripped watches appear in Overview's Automations card.
3. Tests: `a tripped watch says what it did`, `undo posts the reroute
   undo`, `an agent's watch names the agent`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 4 — bots

**Files:** `web/bridge/contract/automations.go`, fixtures, generated files,
`app/lib/src/model/bots_model.dart`,
`app/lib/src/ui/automations/bots_tab.dart`, `review_draft_sheet.dart`,
`inbox_model.dart`, tests.

1. Per project: the review bot's settings and drafts, the CI fixer's
   settings and history, and F5's automatic-fixing switch when F5 plan 1
   has landed (otherwise a planned row).
2. The CI fixer's detail states as text that it never deletes, skips or
   disables a test.
3. A review draft opens with its findings by severity and Post, Edit, Send
   to the author's agent, Discard. A held draft is an ask of kind
   `review_draft`.
4. Tests: `post posts the draft`, `send to author posts`,
   `discard confirms`, `a held draft is an ask`,
   `the fixer's rule is shown and not editable`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 5 — Library

**Files:** `web/bridge/contract/library.go`, `clients.go`, fixtures,
generated files, `app/lib/src/model/library_model.dart`,
`clients_model.dart`, `app/lib/src/ui/library/library_place.dart`, four tab
files, `placeholder.dart`, tests, `screens.dart`.

1. Tabs Skills, Plugins, MCP, Memory.
2. Skills: list by scope; adding one goes through preview, then confirm or
   discard. Memory: scope, provenance, confidence; a suggested promotion is
   an action the user approves. MCP: servers and clients; a client's detail
   has its token (shown once on creation), its confirmation rule and its
   factory grants per project.
3. Tests: `a skill is previewed before it is confirmed`,
   `promotion needs approval`, `a client token is shown once`,
   `a grant is saved per project`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 6 — record and full verification

**Files:** the spec.

1. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && dart format
--output=none --set-exit-if-changed lib test`; `CGO_ENABLED=1 go test
./web/... -count=1`; `cd web/ui && npm run check && npm test`.

## Done when

- Every Automations and Library list uses `SectionPage`.
- Each section handles loading, empty, stale, error and unavailable.

## What later plans rely on

- `SectionModel` and `SectionPage`, used by every Settings section.
