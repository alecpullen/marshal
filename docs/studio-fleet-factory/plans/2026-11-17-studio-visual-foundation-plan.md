# Studio visual foundation — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-visual-foundation-spec.md](../studio-visual-foundation-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Shared Flutter visual primitives and responsive navigation. **Non-goals:** New product features, replacement theme, changes to the Svelte Studio. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Establish the Flutter baseline

**Goal:** Establish the Flutter baseline.

**Files:** `app/lib/src/ui/shell/shell.dart`, `app/lib/src/ui/kit/kit.dart`, `app/lib/src/ui/tokens.dart`, `app/test/support/screens.dart`.

**Steps:** Run the app using the current F8 serving instructions and capture narrow and desktop Flutter screens in both themes with revision and fixture provenance. Exercise shell navigation, Overview, a work item and a thread; record reproducible defects and distinguish them from the historical Svelte screenshots.

**Verify:** Inspect the captures and verify focus, visible content and semantics at narrow and wide widths. Record whether a real bridge or physical device was used.

**Command:** From `app/`, run `flutter test test/overflow_test.dart test/states_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Repair shared primitives where evidence warrants

**Goal:** Repair shared primitives where evidence warrants.

**Files:** `docs/studio-flutter/design-system.md`, `app/lib/src/ui/kit/`, `app/lib/src/ui/gallery/gallery.dart`, `app/test/gallery_test.dart`.

**Steps:** For each reproduced shared defect, identify the existing kit primitive and fix it there. If a new capability or token is necessary, specify it in §10 first and update tokens and documentation together; add its gallery example and component tests. Keep current color and geometry when evidence does not justify a change.

**Verify:** Run the relevant kit tests and gallery test in light and dark; update pinned-platform goldens only when intentional.

**Command:** From `app/`, run `flutter test test/gallery_test.dart test/contrast_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Verify representative screens and responsive navigation

**Goal:** Verify representative screens and responsive navigation.

**Files:** `app/lib/src/ui/shell/shell.dart`, `app/lib/src/ui/overview/overview.dart`, `app/lib/src/ui/factory/work_item_screen.dart`, `app/lib/src/ui/thread/thread_header.dart`, `app/test/overflow_test.dart`.

**Steps:** Apply only the screen-level composition changes needed to use the repaired kit. Check shell navigation, long identities, focus return and short phone viewports without changing routes or authority.

**Verify:** Add focused widget regressions for reproduced failures; run overflow, tooltip, states and design-rules tests and compare before/after Flutter captures.

**Command:** From `app/`, run `flutter test test/overflow_test.dart test/tooltips_test.dart test/design_rules_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
