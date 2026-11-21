# Agent-first Marshal Studio implementation plan

> **For agentic workers:** Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement one checked task at a time.

**Goal** — Start ordinary work by talking to a project agent. Give the operator a separate Marshal conversation for cross-project work, while keeping Overview, Needs you and Pull requests one navigation action away.

**Architecture** — Flutter owns navigation, drafts and presentation. A host daemon owns coordinated tasks, assignment identity and recovery. An agent-side coordinator uses the configured model and narrowly scoped Studio tools. Ordinary project workers continue to run through the existing fleet and ACP paths.

**Stack and baseline** — Flutter/Dart, Go, ACP, MCP, bridge JSON and SSE. Authored against `db8c32b5` on `docs/studio-conversation-navigation`. Read the [approved design](../agent-first-studio.md) and [Studio design system](../design-system.md). Reconcile parallel changes to the optional agent prompt on integration. Existing callers that deliberately create a blank session retain that capability.

## Delivery order

| Shippable increment | Tasks | What an operator can do |
|---|---|---|
| Project conversations | 1–3 | Select a project, type a task, steer the agent and inspect its diff. Open the existing fleet and review screens directly. |
| Marshal reads the fleet | 4–5 | Resume a host's Marshal thread and ask which projects need a decision. Follow a link to the original ask. |
| Marshal coordinates work | 6–8 | Hand off from a project, follow assignments, steer workers and receive a result in the original conversation. |

Ship each increment when its complete journey passes. Introduce the Marshal sidebar entry with task 5, when it has a working destination. Each numbered task is a reviewable commit. Start with the named failing case, run the task's check, implement, rerun it and commit the green change.

## Global constraints

- Preserve Overview, the Inbox route and its filters, answers and Ready items, the pull-request check groups, project boards, thread links and keyboard access. Use **Needs you** for visible Inbox text throughout Studio.
- First release coordinates accessible projects on one selected host. Each task has one coordinator thread. Project defaults, trust, tool approval and source visibility apply independently at every target.
- Keep `web/` standard-library-only. Extend `web/bridge/contract` and regenerate Dart via `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update` on each wire change. Retain old-client compatibility.
- Use Studio kit components and tokens. Register new screen states in `app/test/support/screens.dart`. Cover keyboard, narrow layout, pending, empty, stale, unavailable, unauthorized and retry where applicable.
- Use isolated stores, fake providers and disposable projects for verification. A configured local model is a supported route. Keep routing, permissions and orchestration outside Flutter widgets.

## Review focus

| Failure case | Expected result | Task |
|---|---|---|
| Same project name on two hosts or stale remembered thread | Restore the correct host/project or open its empty composer | 2 |
| Two submissions or a lost spawn response | One agent receives the first prompt once; the draft survives uncertainty | 3 |
| A host is unreachable or a project grant is revoked | Report incomplete coverage and omit restricted content and links | 4–5 |
| Daemon restarts between worker creation and receipt persistence | Reconcile to the existing pending intake or worker | 6 |
| User steers a linked thread while another assignment fails or the task stops | Preserve the correction and independent work; report each outcome | 7–8 |

## Contracts between tasks

These are proposed additions. Keep existing `Place.inbox()`, `ThreadRef`, `NewWorkModel.start()`, spawn results and answer routes as the underlying interfaces.

| Owner | Contract | Consumer |
|---|---|---|
| 2–3 | Project entry carries host, canonical project ID and a local draft ID. First send carries one client-generated request ID reused for retries of the same content. | Shell, project composer, spawn handler |
| 4 | A task on one daemon has a durable task ID, coordinator session ID, optional source thread, title, status and revision. A thread reference includes project ID, kind and ID. Host comes from the serving daemon and is included in Studio routes. | Coordinator history, handoff, Studio |
| 4–5 | Read-only attention entries contain a source reference, kind, display text, age and destination. Report freshness and access per source. | Marshal summary and source links |
| 6–8 | Each assignment carries task ID, assignment ID, target project, instruction, relationship (created worker or existing thread), pending intake or worker reference, state, and latest observed result revision. Task and assignment IDs are stable across reconnects. | Intake, status projection, stop and result delivery |

## Task 1 — Keep the fleet screens first-class

- [x] Update `app/lib/src/model/nav_model.dart`, `ui/shell/sidebar.dart`, `ui/shell/keys.dart` and `ui/inbox/inbox.dart` (UI paths here and below are under `app/lib/src/`). Rename all user-facing Inbox references, including search, shortcuts, tooltips and accessibility labels. Preserve `Place.inbox()`, existing URLs and shortcut destinations.
- [x] Keep Overview, Needs you and Pull requests visible in desktop navigation and reachable from the narrow navigation menu. Preserve the existing overview content, inbox filters and inline answer lifecycle, and PR review actions. Keep settings, library, automations and boards reachable under secondary navigation.
- [x] Update the navigation rules in `docs/studio-flutter/design-system.md`. Extend `test/model/nav_model_test.dart`, `test/ui/shell/keys_test.dart` and `test/ui/inbox_test.dart` under `app/`. Assert that an old Inbox link opens Needs you, an inline answer removes the ask only after confirmation, and Ready links to the same pull request as the dedicated screen.
- [x] From `app/`, run `flutter test test/model/nav_model_test.dart test/ui/shell/keys_test.dart test/ui/inbox_test.dart test/ui/pulls test/route_parity_test.dart`. All destinations and legacy routes pass.

## Task 2 — Open a conversation when selecting a project

- [x] Distinguish selecting a sidebar project from explicitly opening its board. In `model/place.dart`, `model/route_translation.dart`, `model/selection.dart` and `ui/shell/shell.dart`, keep old board and thread deep links intact. Use a separate project conversation entry for the sidebar and New. Direct URLs win over restored preferences. Keep an explicit Overview visit on Overview.
- [x] Persist last selected project and last usable thread per host/project through the existing preference-store seam. Resume that thread on project selection. A missing or closed thread opens a focused draft. With no context, visibly use a valid last project, offer a selector for multiple candidates, or offer Connect repository for an empty host.
- [x] Add `ui/new_work/project_composer.dart` backed by `NewWorkModel`. Place the project identity above the input. Put overrides, issues, recipes, recent prompts, drafts and the explicit planner/work-item paths under Options. Preserve the existing planner and work-item handoff behavior. Route sidebar New, narrow New and the keyboard shortcut through the same entry, including from global screens.
- [x] Add `app/test/ui/new_work/project_entry_test.dart`. Check two same-named projects on different hosts, closed thread, first visit, Back, board link, project switching with a draft and zero creates before Send. From `app/`, run `flutter test test/ui/new_work test/ui/shell/address_test.dart test/model/route_translation_test.dart`.

## Task 3 — Deliver the first message exactly once

- [x] In `model/new_work_model.dart` and `ui/shell/shell.dart`, make the first nonblank Send start the agent with the prompt and project defaults. Bind the returned thread directly without sending the prompt a second time. Keep the input while pending and on failure. Disable repeat submission while the request is in flight. Route subsequent turns through the existing thread composer.
- [x] Add an optional request ID to `web/bridge/contract/spawn.go`. In `web/bridge/http.go`, `fleet.go` and `workspace.go`, claim it durably within the caller/project scope before side effects. Resolve the same request to the same agent across retry/restart. Reject reuse with a different payload. Preserve the existing path for callers omitting the ID. Distinguish a definite rejection (draft can be edited) from an unknown outcome (resolve original ID first).
- [x] In `ui/thread/thread_composer.dart` and `thread_screen.dart`, keep recipient, steering, Stop, the original ask card and the change inspector within reach. A request to plan enters discussion before implementation; honor the existing explicit Plan first and work-item routes. Test natural-language planning with a scripted agent turn and verify no tool mutation precedes a user's decision.
- [x] Extend `app/test/ui/new_work/start_opens_the_thread_test.dart` and add `web/bridge/spawn_request_test.go`. Cover double Send, lost acknowledgment, draft restoration, same ID/different content, and legacy spawn. Run `scripts/test.sh quick ./web/bridge/... -run 'Spawn|DartContract'` and, from `app/`, `flutter test test/ui/new_work test/ui/thread test/ui/shell/thread_stop_test.dart`. The project-only journey can ship here.

## Task 4 — Give Marshal a host-local session and a scoped read surface

- [x] Add `web/bridge/coordinator.go`, `coordinator_store.go` and `contract/coordinator.go`. Store task/session mappings and source references in daemon state using the existing atomic store conventions. Provide list, get and create operations at `/api/coordinator/tasks` and its task resource. First-send creation uses a request ID; reopening the destination reads existing tasks. Old stores load with no tasks.
- [x] Compose each coordinator as a real ACP conversation on the selected daemon with its configured provider, a daemon-managed working directory and an explicit coordination-only tool set. Add its runtime role at agent composition in `internal/app/`. Keep shell, repository editing, access management and general fleet mutation tools outside this role. Persist conversation identity independently of browser lifetime.
- [x] Add the coordinator attention read in `web/bridge/marshal_studio_tools.go`. Reuse the fleet Inbox and PR projections, including pending intake and ready reviews. Bind a daemon-issued task/session identity to its allowed projects, check access on each read and deep-link resolution, and return partial freshness. Preserve the existing MCP client ownership checks in `mcptools.go`. Unavailable or unauthorized sources cannot produce an all-clear.
- [x] Add `web/bridge/coordinator_test.go` cases for two tasks with different histories, host-local scope, a revoked read grant and mixed freshness. Assert that an attention-only turn starts zero workers and has no access to mutation tools. Run `scripts/test.sh quick ./web/bridge/... ./internal/app/... -run 'Coordinator|MarshalStudio|DartContract'`.

## Task 5 — Open Marshal and answer attention questions

- [x] Extend `model/place.dart`, `model/nav_model.dart`, `client/bridge_client.dart`, `app/lib/main.dart` and `ui/shell/shell.dart` with the **Marshal** destination. Use task-qualified links that survive reload and Back. Restore the selected host's last task, offer its recent tasks, and let New open a distinct draft. Reuse the normal thread transcript and composer with **Marshal** as recipient.
- [x] Project the task 4 attention result into links to original asks and reviews. Opening an item reaches its owning host, project and thread or PR. Answer it with the existing control. Refresh the attention result on the next query. Surface the age of cached answers from an unavailable host.
- [x] Add `app/test/ui/shell/coordinator_test.dart` and screen fixtures. With two tasks, verify independent drafts/history, correct linked asks across two projects, and an unreachable host. Run `flutter test test/ui/shell/coordinator_test.dart test/ui/inbox_test.dart test/states_test.dart` from `app/`. The read-only Marshal journey can ship here.

## Task 6 — Dispatch once under each project's permissions

- [x] Add `web/bridge/coordinator_assignments.go`. Persist the assignment intent and stable dispatch identity before dispatch. Extend the intake/worker paths in `intake.go`, `fleet.go` and `workspace.go` so the same identity follows pending intake, its confirmation and the live worker. Reuse the durable effect identity pattern in `factory_workers.go` without creating a factory work item for ordinary delegation.
- [x] Check the task's effective project/operation scope at dispatch time. Resolve distinct targets by canonical ID, not display name. Clear authorized assignments start immediately; existing trust and approval checks yield their usual asks. Store pending intake as pending and show running only on acknowledgment. Return a failure per target while allowing eligible siblings to progress.
- [x] Expose task-scoped assignment operations to coordinator sessions only. Link existing threads for observation by default; accept steering one only with explicit user direction to that thread. Keep project agent identities scoped to their projects. Never pass intake an operator identity merely because a model requested work.
- [x] Add `web/bridge/coordinator_assignments_test.go`. Concurrent identical dispatches, expiration of pending intake and restart on either side of worker creation must reconcile to one outcome. A grant revoked before dispatch must deny that assignment. Run `scripts/test.sh quick ./web/bridge -run 'Coordinator|Intake|FactoryWorker'`.

## Task 7 — Hand work from a project to Marshal and report back

- [x] Add a structured handoff tool to the built-in Studio session integration in `web/bridge/marshal_studio_tools.go` and its tool registration. Bind source to the invoking session. Carry the requested outcome, explicit targets, source reference and necessary findings. Use the existing user-direction mechanism for permission-sensitive actions. An ambiguous “the CLI” yields a clarifying question before dispatch to that target. A direct Marshal request uses the same assignment path without a source.
- [x] In `web/bridge/coordinator.go`, project assignment updates through existing fleet events with a snapshot on reconnect. Use current worker results, including user steering through a worker thread. Persist the source delivery with a stable event ID so retries cannot post duplicate summaries. Deliver an informational thread update without initiating another agent turn. Keep it available when the source has no attached viewer; recheck visibility before delivery.
- [x] Extend the thread model and renderer in `model/thread_view_state.dart` and `ui/thread/transcript.dart` for the handoff link, project/status/thread rows and source result. Preserve each composer's draft. Show a scoped retry for a failed assignment, the original ask for a blocked worker, and the last observed time for stale state.
- [x] Extend the task 4–6 suites with the API/CLI walkthrough. Assert the source is linked once, one CLI worker starts, its correction is reflected in the eventual summary, and reload produces one delivered result. Run `scripts/test.sh quick ./web/bridge -run 'Coordinator|MarshalStudio'` and `flutter test test/ui/shell/coordinator_test.dart test/ui/thread` from `app/` for the Flutter command.

## Task 8 — Stop at the requested scope and verify the complete journey

- [x] Persist a task stop before cancelling children. Block new dispatches, withdraw owned pending intake, and reconcile racing worker acknowledgments. Cancel only workers created by the task. Explicitly steered or observed independent threads retain their own Stop. Keep ordinary Stop-turn and **Stop coordinated task** distinct; show pending until the daemon confirms.
- [x] Implement the transition in `web/bridge/coordinator_assignments.go` and wire the scoped action into the coordinator thread. Test stopping during dispatch, a repeated stop after reconnect, and a task containing both owned and linked threads. Assert the linked thread remains usable and the final summary names failed or cancelled work accurately.
- [x] Exercise the complete project → Marshal → worker → source journey with a fake provider, two disposable projects and a real test bridge. Add the joined test to `web/bridge/marshal_studio_integration_test.go` and the corresponding UI flow to `app/test/ui/shell/coordinator_test.dart`. Verify queue answers, direct PR review and Overview navigation throughout.
- [x] From repo root run `scripts/test.sh full`, `go vet ./...`, and `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge`. From `app/`, run `flutter analyze`, `flutter test` and `flutter build web --base-href / --no-web-resources-cdn --output ../share/marshal/studio`. Run gallery golden verification on macOS per AGENTS.md. Check the built Studio at 390, 800 and 1440 px and at 2× text. Acceptance is a completed conversation and a delivered result, with the three preserved screens still usable by keyboard.

## Implementation notes (tasks 1–8)

Implemented on `feat/agent-first-studio`, stacked on the optional-agent-prompt change (PR 198). One reviewable commit per task, plus the integration commits that stacked them.

Deliberate limits of this increment, left visible rather than faked:

- **The coordinator ACP conversation is composed but not spawned.** The runtime role, coordination-only tool set, durable task/session identity and daemon-managed working directory exist; starting the child needs an `acp`/`launch` flag outside the task's file scope. Tasks, attention reads, dispatch, handoff, delivery and stop all work; the coordinator thread shows its standard error state with retry until that child is started.
- **Stopping is the operator's action.** There is no model-facing stop tool. A refused ACP cancel leaves the task `stopping` and answers pending, so a repeat retries; no test forces that failure.
- **First-prompt delivery is at-most-once.** A crash between the durable claim and the prompt loses that first turn rather than risking a duplicate instruction; the draft is kept, so sending again recovers.
- **Gallery goldens are pinned to Linux** in this tree (`app/test/gallery_test.dart`), contrary to the stale "macOS" line in `AGENTS.md`; they were regenerated on Linux.
- **The built Studio was checked for a CDN URL and CanvasKit presence as CI does, but not exercised in a browser.** Narrow/wide layout and 2× text are covered by `overflow_test` and the coordinator screen tests.
