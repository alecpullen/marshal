# Studio Fleet attention and recovery — Flutter implementation plan

Date: 2026-11-18. Status: Flutter port for review; the product direction was approved on 2026-11-17, but the Flutter implementation details are newly adapted. This is a follow-up to Flutter parity, not a claim that any listed enhancement is already shipped. Reconcile with current `main` before starting.

Specification: [studio-fleet-attention-ux-spec.md](../studio-fleet-attention-ux-spec.md). Backlog: [Studio UI improvements](../studio-ui-improvements-backlog.md).

## Goal and assumptions

**Goal:** Clear cross-project attention, source freshness and recovery. **Non-goals:** New pollers, implied ownership or automatic actions. **Base:** `origin/main` at `1c2175ec` (2026-11-18); reconcile later changes before execution. Tasks run in order, each verified and committed separately. Existing Go wire contracts and Dart generated types are the shared contracts; no new cross-task exported interface is prescribed by this port. New state stays within the named existing models.

## Constraints and verification for every task

Work against the current Flutter tree and re-check concurrent changes before implementation. `docs/studio-flutter/design-system.md` is normative under `app/lib/src/ui/`: screens import `kit/kit.dart`, use its components and `tokens.dart`, and provide the applicable loading, empty, stale, error and unavailable states. Do not introduce Material/Cupertino imports in screens, one-off visual styles, new palette values, panel fills or shadows. If the kit lacks a capability, first specify it in design-system §10, then implement it in the kit, gallery, component tests and light/dark goldens together. Keep UI independent of `client/`; put state and operations in existing models and use generated wire types without hand-editing them. Preserve existing source identities, routes, permissions, request IDs and versions.

Run the named focused tests from `app/`, then `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`. Include the design-rule, boundary, state, tooltip, overflow, contrast and gallery tests in that suite. Update gallery goldens only for intentional kit changes using `flutter test --update-goldens test/gallery_test.dart` on the pinned macOS environment. Test narrow and wide layouts, keyboard/focus and semantics with deterministic fixtures; distinguish these from real bridge and physical-phone evidence. Do not edit the retiring Svelte Studio for this work.

## Task 1: Audit and refine existing attention sources

**Goal:** Audit and refine existing attention sources.

**Files:** `app/lib/src/model/inbox_model.dart`, `app/lib/src/model/overview_model.dart`, `app/lib/src/model/factory_store.dart`, `app/test/model/inbox_model_test.dart`.

**Steps:** InboxModel already combines Factory approval-draft, decision, recovery and completion asks with other sources. Trace existing stores, polling and ownership; preserve separate identities and actions, and add only missing source-reported ready information. Do not create a parallel poller, infer owner or collapse a worker request into a factory decision.

**Verify:** Test mixed sources and projects, separate requests for one worker, unknown ownership and ready versus complete; assert identity and counts.

**Command:** From `app/`, run `flutter test test/model/inbox_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 2: Represent source freshness and recovery

**Goal:** Represent source freshness and recovery.

**Files:** `app/lib/src/model/fleet_store.dart`, `app/lib/src/model/factory_store.dart`, `app/lib/src/model/inbox_model.dart`, `app/test/model/overview_model_test.dart`.

**Steps:** Carry the existing stores’ per-source read success, stale snapshot and error information into view models. If a source does not expose it, extend its existing store without overwriting last trustworthy data. Keep healthy sources visible and distinguish unavailable from empty; refresh must never retry a mutation.

**Verify:** Test partial failures, stale and absent snapshots, reconnect, stable ordering and no duplicate polls or mutations.

**Command:** From `app/`, run `flutter test test/model/inbox_model_test.dart test/model/overview_model_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Task 3: Compose Overview and Inbox

**Goal:** Compose Overview and Inbox.

**Files:** `app/lib/src/ui/overview/overview.dart`, `app/lib/src/ui/inbox/inbox.dart`, `app/test/ui/overview_test.dart`, `app/test/ui/inbox_test.dart`.

**Steps:** Use existing AskCard, PrCard and StateMessage to show project, source, reason, confirmed state and direct action; factory entries navigate to their existing item. Prioritize actionable, then reported ready, then running, with source order or stable timestamp/identity ties. Keep filters and filtered-empty states comprehensible on phone.

**Verify:** Widget-test route identity, per-source stale/error states, focus, phone width and visible counts; run against real bridge separately where available.

**Command:** From `app/`, run `flutter test test/ui/overview_test.dart test/ui/inbox_test.dart`. Expected: all named tests pass, including the task’s new regressions.

## Final verification and integration

From `app/` run `flutter analyze`, `flutter test`, and `dart format --output=none --set-exit-if-changed lib test integration_test`; all must pass. If bridge contract or Go behavior changes, from the repository root run `go test ./web/bridge/...` and the contract generator currentness tests, and regenerate the Dart contract only through its generator. Record the Flutter revision, test results and any live bridge or physical-device coverage separately. Reconcile the other backlog slices and current cutover status before merging.
