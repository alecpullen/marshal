# Studio work-item and worker experience — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-work-item-experience-spec.md](../studio-work-item-experience-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Clear decisions, activity and worker navigation in the existing Flutter work-item screen. **Non-goals:** Factory policy, backend transitions, new subagent protocol. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Preserve and clarify priority

**Goal:** Preserve and clarify priority.

**Files:** `app/lib/src/ui/factory/work_item_screen.dart`, `app/lib/src/ui/factory/item_asks.dart`, `app/lib/src/ui/factory/plan_tab.dart`, `app/test/ui/factory/work_item_test.dart`.

**Steps:** Keep existing ItemAsks before activity and make the existing Plan tab and revision entry discoverable. Retain decision, recovery, completion and PR actions and their exact IDs. Preserve revision input and in-flight assessment through polling and drill-in; move ownership into the item model if the screen is disposed.

**Verify:** Widget-test pending asks, revision input during refresh and worker drill/back; assert no automatic decision or mutation.

**Command:** From `app/`, run `flutter test test/ui/factory/work_item_test.dart test/ui/factory/item_asks_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Improve worker and activity context

**Goal:** Improve worker and activity context.

**Files:** `app/lib/src/ui/factory/agents_tab.dart`, `app/lib/src/model/work_item_model.dart`, `app/lib/src/ui/factory/work_item_screen.dart`, `app/test/ui/factory/work_item_drill_in_test.dart`.

**Steps:** Expose available worker role, name and confirmed status; distinguish unavailable, parked and unknown when backed by data. Keep identity inspectable and reuse existing worker conversation. Retain timeline order and scroll position; use relative times with exact-time tooltip and unknown for invalid values.

**Verify:** Test unknown and parked workers, selected worker and back navigation, timestamp fallbacks and incoming activity without losing reading position.

**Command:** From `app/`, run `flutter test test/ui/factory/work_item_drill_in_test.dart test/model/work_item_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Protect existing review and completion behavior

**Goal:** Protect existing review and completion behavior.

**Files:** `app/lib/src/ui/factory/pulls_tab.dart`, `app/lib/src/ui/factory/revision_sheet.dart`, `app/test/ui/factory/item_asks_test.dart`.

**Steps:** Keep the separate user completion decision, existing merge/recovery options, evidence freshness and all linked PRs. Integrate shared subagent UI only if its separate plan has landed; do not add a parallel protocol.

**Verify:** Exercise multiple PRs, stopped/ready/complete states and failed completion; verify payload IDs and no inferred completion.

**Command:** From `app/`, run `flutter test test/ui/factory/item_asks_test.dart test/ui/factory/f6_1_e2e_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
