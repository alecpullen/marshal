# F6 plan 1: work items — implementation record

Date: 2026-11-18. Branch `studio/f6-1-work-items`, off `studio/f5-2-pulls-app`.
Plan: [2026-11-17-f6-1-work-items-plan.md](plans/2026-11-17-f6-1-work-items-plan.md).
Design: [f6-factory.md](f6-factory.md) decisions 1–7, 13, 14.

This records what the plan produced, the decisions it left open that were
settled here, the deviations from its letter, and what is not done.

## What exists

- **The contract.** `web/bridge/contract/factory.go` declares the work item and
  everything `GET /api/factory/items/{id}` returns, `FactoryCommand` with its
  payloads, and the two snapshot types an approval carries. `web/bridge`
  aliases every name to it, so the journal's bytes are unchanged — the
  declarations keep the field order and the two marshallers. `test/contract/
  factory.json` records a work item in each of draft, needs_input with a
  decision, ready and closed with a completion; both generated clients are
  regenerated and current, and both fixture round trips compare them.
- **The store and the model.** `model/factory_store.dart` is the only reader of
  the item list and the only poller (5 s while an item works, 60 s otherwise),
  and it owns the command helpers the screens call. `model/work_item_model.dart`
  maps every state to a stage and a status, filters, the activity timeline with
  its roles in words, the four asks, and `WorkItemView` — the projection the
  screens draw.
- **The thread.** `ui/factory/work_item_screen.dart` with `plan_tab`,
  `agents_tab`, `checks_tab`, `pulls_tab`, `item_asks`, `recovery_form` and
  `revision_sheet`. Its stage strip is the kit's `StageStrip`, its activity the
  kit's `ActivityRow`, both added to the design system (§5, "Added in F6") and
  to the gallery, whose Linux goldens are regenerated.
- **Asks and commands.** A draft's approval, a decision with a free answer, a
  recovery with its model, context choice and extra rounds inline, and a
  completion with complete, merge-and-complete and reopen; stop is in the
  header's overflow menu and confirms. Every action posts one command through
  the store, shows `Sending…` until the item changes, and a 409 leaves the
  action available with "This changed while you were deciding" beside it.
- **Drill-in and revisions.** Opening a worker swaps the centre for F3's thread
  on that worker's session with "Back to activity"; "Propose a change" opens a
  sheet showing the approved plan against the proposed one and sends
  `revision_propose` with the base revision and hash the item holds.

## Where it is wired

- `PlaceKind.thread` resolves a work item before an agent, and the shell builds
  the work-item place from `FactoryStore`.
- The sidebar lists work items under their project and omits every agent that
  is a worker of any item, historical ones included.
- `InboxModel` includes `asksOf` for every item, so a draft or a decision is
  never waiting with nothing showing it.
- `main.dart` constructs `FactoryStore`, hands it to the inbox and to the
  `Studio`, and starts its poller.

## Decisions this record makes

- **The screens take a projection.** `ui/` may not import `client/` (F1's
  boundary, enforced by `test/boundary_test.dart`), so `WorkItemView` reads the
  wire once in `model/` and the screen and its tabs take that. The commands
  live on `FactoryStore` (`approve`, `answerDecision`, `recover`, `complete`,
  `reopen`, `stopItem`, `mergeAndComplete`, `proposeRevision`) for the same
  reason.
- **A contract type may carry its marshaller.** `FactoryApproval.MarshalJSON`
  and `FactoryWorker.MarshalJSON` keep a journal written before subagent
  settings byte-identical, and a Go method cannot be declared outside its
  type's package. They moved with the types; `contract/doc.go`'s "no methods"
  now has this one exception, documented in `contract/factory.go`. Two fields
  that only the bridge reads (`FactoryOutcome.Continuation` and
  `FactoryCommand`'s two freeze fields) are exported with `json:"-"` because
  the bridge sets them from another package; neither is on the wire.
- **Reaching the contract reached a little further than the plan listed.**
  `FactoryCreate` and `FactoryApproval` carry an `EditSnapshot`, and
  `FactoryReport` carries `ReviewFinding`, so those three types (with
  `SelectedFile`) moved into the contract too, and `planner_types.go` and
  `reciperun.go` alias them. No field or JSON key changed: every tag of every
  moved type keeps its pre-move spelling, and `contract/factory_tags_test.go`
  now pins them, so a rename fails there before the fixture round trip or the
  currency tests can regenerate themselves to it. That matters most for
  `FactoryConfig.NoRequiredCIAcknowledged`, whose tag is
  `noRequiredCiAcknowledged`: the journal stores the item re-marshalled and
  `replayRecord` byte-compares it, so renaming the tag would make `webbridge`
  refuse to start on an upgraded store, and both generated clients post the
  old key.
- **The generators skip unexported types.** The contract now holds two
  package-internal legacy forms; `dartgen` and `tsgen` no longer emit a
  declaration for a type the package does not export, which is what keeps the
  generated clients free of journal-only helpers.
- **`factory.ts` re-exports what it can adopt, and keeps what it narrows.** The
  read-only wire shapes are now `= FactoryWire.X`; the request shapes
  (`FactoryCommand`, `FactoryCheckKey`), the handoff shapes (`FactoryConfig`,
  `FactoryCreate`, `FactoryCreateConfig`, `FactoryDefaults`, `FactoryApproval`,
  `FactoryItem`) and `FactoryWorker`/`FactoryChild`/`FactoryMergeStep` and the
  merge-batch pair stay local because the Svelte client builds or narrows them
  (a command names only the fields it sets; a config a user left alone is
  absent, not empty). The reason is written at the top of the file.
- **A work item's four asks use the kit's existing `AskCard` kinds.** The
  card's title and body say what is being decided, and four new kind names
  would add a word and an icon that say nothing the title does not (§10).
- **The end-to-end is a model-level flow test.** `integration_test` needs a
  Linux device toolchain this machine does not have, and a widget test cannot
  host the flow: a command posted from a tapped callback runs in the test's
  fake-async zone, where the fixture bridge's real HTTP never completes. So
  `test/ui/factory/f6_1_e2e_test.dart` drives the store through draft →
  approved → decision answered → repaired → ready → complete, asserting the
  view the thread draws at each step, and the thread's drawing, its asks and
  its actions are covered by the widget tests beside it.

## Not done

- **Steering's recipient and queued message** (plan task 5, items 2): a work
  item's composer does not yet name its recipient, and an undeliverable message
  is not shown queued with Edit and Cancel. The drill-in uses F3's composer as
  it is.
- **The remaining `revision_*` kinds**: only `revision_propose` is driven.
  `revision_supersede`, `revision_reject`, the assessment kinds,
  `revision_authorize`, `revision_delivery_choice` and
  `revision_follow_up_confirm` are not surfaced.
- **The Changes tab is a slot**: `WorkItemScreen` accepts F4's tab for the
  item's branch and says the change is not being read when the host passes
  none. The shell does not yet build one for a work item.

## Environment notes

- `flutter test integration_test -d linux` and `flutter build linux` cannot run
  here (no CMake/GTK3/display). The flows are driven by widget and model tests
  instead, as above.
- `flutter test --platform chrome` cannot run here (no browser).
- The gallery goldens are Linux-pinned and were regenerated on Linux.
- `web/ui`'s `npm run check` has the pre-existing five errors the task brief
  names, and no new ones.

## Verification

- `CGO_ENABLED=1 go test ./web/... -count=1` — passes, including the bridge's
  journal-replay tests, the contract fixture round trip and both generated
  clients' currency tests.
- `cd web/ui && npm run check && npm test` — 1355 tests pass; `npm run check`
  reports the five pre-existing errors and nothing new.
- `cd app && flutter analyze` — no issues.
- `cd app && flutter test` — 1994 tests pass, including the enforcement tests
  (design rules, tooltips, states, overflow, gallery goldens, boundary).
- `dart format --output=none --set-exit-if-changed lib test` — clean.
