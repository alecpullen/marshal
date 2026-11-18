# Studio shared agent conversation — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-agent-conversation-ux-spec.md](../studio-agent-conversation-ux-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Readable and controllable shared Flutter conversations. **Non-goals:** New ACP runtime or a second subagent implementation. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Make context and transcript clear

**Goal:** Make context and transcript clear.

**Files:** `app/lib/src/ui/thread/thread_screen.dart`, `app/lib/src/ui/thread/thread_header.dart`, `app/lib/src/model/thread_view_state.dart`, `app/lib/src/model/stack_store.dart`, `app/test/ui/thread/transcript_test.dart`.

**Steps:** Refine shared full and embedded thread context and existing transcript grouping with kit primitives. Show actual connection and authority only when known; retain density, selection, copy, event order and TUI-style browsing.

**Verify:** Test dense and long messages, full/embedded routes, unknown state, keyboard navigation and selection.

**Command:** From `app/`, run `flutter test test/ui/thread/transcript_test.dart test/ui/thread/browse_keys_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Preserve reading and composition

**Goal:** Preserve reading and composition.

**Files:** `app/lib/src/ui/thread/thread_composer.dart`, `app/lib/src/model/composer_model.dart`, `app/test/ui/thread/composer_test.dart`, `app/test/ui/thread/following_test.dart`.

**Steps:** Keep the composer usable on narrow and short viewports. Distinguish prompt, steering, cancellation request and confirmed outcomes from actual session state. Keep unsent text on failure and never automatically resend an uncertain request; preserve user scroll position and jump-to-latest.

**Verify:** Test failed send, disconnect/reconnect, cancellation confirmation, short viewport, focus and incoming transcript while reading older messages.

**Command:** From `app/`, run `flutter test test/ui/thread/composer_test.dart test/ui/thread/following_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Expose pending interactions and existing child context

**Goal:** Expose pending interactions and existing child context.

**Files:** `app/lib/src/ui/thread/thread_screen.dart`, `app/test/ui/thread/asks_in_place_test.dart`, `app/test/ui/thread/e2e_test.dart`.

**Steps:** Make existing questions, approvals and recovery reachable in both contexts, retaining their IDs and action authority. Reuse subagent presentation/steering if the separate subagent work has landed; otherwise keep existing drill-in and identify the integration point without inventing capability.

**Verify:** Test concurrent requests, child drill/back, disabled/unsupported actions and exact payloads; report external ACP coverage separately.

**Command:** From `app/`, run `flutter test test/ui/thread/asks_in_place_test.dart test/ui/thread/e2e_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
