# Studio planning, drafts and handoff — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-planning-handoff-ux-spec.md](../studio-planning-handoff-ux-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Clear persistence and reviewed handoff across the existing planner and drafts. **Non-goals:** Unapproved factory starts or fabricated offline durability. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Clarify project-scoped journeys

**Goal:** Clarify project-scoped journeys.

**Files:** `app/lib/src/ui/factory/planning_screen.dart`, `app/lib/src/ui/factory/handoff_sheet.dart`, `app/lib/src/model/planning_model.dart`, `app/test/ui/factory/planning_test.dart`.

**Steps:** Use existing project-scoped places to connect planner, draft and handoff. Show connection, mode, workspace and pending requests only from confirmed state. Keep workspace-sharing warning and user choice, and retain the source route and edits when returning.

**Verify:** Test navigation and pending requests in narrow and wide layouts, unknown/disconnected turns and preservation of edits; no implicit resends.

**Command:** From `app/`, run `flutter test test/ui/factory/planning_test.dart test/model/planning_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Expose truthful draft persistence

**Goal:** Expose truthful draft persistence.

**Files:** `app/lib/src/model/drafts_model.dart`, `app/lib/src/ui/factory/draft_conflict.dart`, `app/test/model/drafts_model_test.dart`.

**Steps:** Map the actual save, pending, error, conflict, deleted and converted states to clear labels. Explain which saved version another device can see; do not claim local persistence across reload unless implemented. Preserve pending edits, versioned writes and conflict resolution.

**Verify:** Test debounce and flush, failed/offline save, two editors including repeated conflict, deletion and conversion; assert no lost text or blind retry.

**Command:** From `app/`, run `flutter test test/model/drafts_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Review captured evidence and actual approval

**Goal:** Review captured evidence and actual approval.

**Files:** `app/lib/src/model/planning_model.dart`, `app/lib/src/model/handoff_model.dart`, `app/lib/src/ui/factory/handoff_sheet.dart`, `web/bridge/factory_http.go`, `app/test/ui/factory/handoff_sheet_test.dart`.

**Steps:** Trace snapshot capture, optional selected files and the bridge create/conversion routes before implementing. Present only supported captured evidence, rejected paths and frozen versions; default selected files to none. The current sheet calls HandoffModel.approve(), posts FactoryCreate and says Approve and start: verify server transition and make the consequence accurate. Preserve a separate approval for draft-only paths and bind submitted content to the reviewed snapshot; do not silently start from capture. If the current seam cannot guarantee the binding or safe uncertain retry, implement and test the required model/bridge contract before claiming it.

**Verify:** Test exact request IDs, hashes/versions and selected operations, changed/stale evidence, failed or uncertain create and actual returned item; include bridge tests for any changed contract and distinguish fixture from live coverage.

**Command:** From `app/`, run `flutter test test/ui/factory/handoff_sheet_test.dart test/model/handoff_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
