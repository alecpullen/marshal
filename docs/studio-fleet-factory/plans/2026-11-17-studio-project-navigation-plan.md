# Studio project settings and navigation — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-project-navigation-spec.md](../studio-project-navigation-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Project orientation and lossless expected-CI-check editing. **Non-goals:** New factory-wide settings store, forge discovery or authorization changes. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Orient within the existing project

**Goal:** Orient within the existing project.

**Files:** `app/lib/src/ui/project/project_page.dart`, `app/lib/src/model/place.dart`, `app/lib/src/model/nav_model.dart`, `app/test/ui/project/project_page_test.dart`.

**Steps:** Clarify the existing project, factory, item and independent-agent paths and section navigation using the shared shell and kit. Preserve encoded identity, back navigation, grouped settings and unsaved-edit guards; call out a missing or unavailable project.

**Verify:** Test narrow and wide navigation, special-character IDs, keyboard/focus and unsaved edits across section changes.

**Command:** From `app/`, run `flutter test test/ui/project/project_page_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Represent expected checks without loss

**Goal:** Represent expected checks without loss.

**Files:** `app/lib/src/model/project_model.dart`, `app/lib/src/model/handoff_model.dart`, `app/test/model/project_model_test.dart`, `app/test/model/handoff_model_test.dart`.

**Steps:** Add a typed draft representation of kind, name and app ID sourced from the generated FactoryCheckKey; ProjectDraft currently does not expose expected checks, and HandoffModel currently reduces them to names and recreates check runs with app ID zero. Preserve exact keys end-to-end, including defaults, overrides and settings serialization, without editing generated wire code. Define raw parsing so commit_status uses the remainder and check_run uses the last colon for its numeric app ID; retain invalid text and block save. Preserve absent/inherited vs explicit values and the existing ETag and conflict handling. HandoffModel currently derives `noRequiredCIAcknowledged` from an empty list; replace that derivation with an explicit user acknowledgement that is retained in the working draft and included in the request only when chosen.

**Verify:** Assert exact read/write and handoff payloads for colons, case, same-name different app IDs, malformed raw text, inheritance, reset, explicit no-required-CI acknowledgement and stale settings; prove unrelated settings survive.

**Command:** From `app/`, run `flutter test test/model/project_model_test.dart test/model/handoff_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Add the structured editor and optional evidence

**Goal:** Add the structured editor and optional evidence.

**Files:** `app/lib/src/ui/project/defaults_tab.dart`, `app/lib/src/model/factory_store.dart`, `app/test/ui/project/project_page_test.dart`.

**Steps:** Use Rows, Sheet and kit fields for structured and advanced raw modes over the same draft. Make suggestions opt-in and only from authorized already-recorded CI evidence in the same project and repository, with revision/time provenance. If that evidence is not exposed, omit suggestions and show manual entry; do not invent a forge lookup or auto-select.

**Verify:** Exercise mode switching with invalid input, unavailable/partial evidence, distinct app IDs and no mutation from viewing suggestions; run project and handoff tests.

**Command:** From `app/`, run `flutter test test/ui/project/project_page_test.dart test/model/handoff_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
