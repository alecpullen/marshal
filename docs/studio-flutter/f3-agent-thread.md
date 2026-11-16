# F3 — the agent thread

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0–F2. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §2, [design-system.md](design-system.md),
`findings/transcript-selection.md`, `docs/web-studio/design.md` §5.2 and
§8.5, `docs/web-studio/specs/2026-10-03-w1-foundation-design.md` §5,
`internal/viewmodel/`, and in the Svelte client: `lib/stack.ts`,
`lib/transcript/`, `lib/session/AgentConversation.svelte`,
`lib/interaction.ts`, `lib/ModeSwitcher.svelte`, `views/NewAgent.svelte`,
`lib/newagent/`, with their tests.

**Before starting.** F2 has landed. Re-read the files above at the current
`main`.

## What F0 found

From [findings/transcript-selection.md](findings/transcript-selection.md):

- Flutter's `SelectionArea` over a lazily built list cannot select rows that
  are not built, restarts a selection after a jump, and threw inside the
  framework on select-all after scrolling away. It is not used for the
  transcript.
- Decision 5 is therefore settled: selection is **model-backed**. Anchors
  are a node id and an offset in `model/`; the list draws highlight spans
  and copies from the plain-text projection. The spike proved this for
  plain-text rows on macOS with rows arriving.
- This adds scope the rest of this document does not list: word and line
  click, shift-extend, the context menu, accessibility, and selection
  through rich text and tool rows. Plan a pull request for it between "The
  thread screen" and "Composer and asks".
- Selection was not exercised in a browser. Do that first: the web is half
  the product.
- Geist has no glyphs for ⌘ ⌥ ⏎ ✓ ✗ ▸ ▾ ⋯. Bundle a symbol fallback font in
  this phase, by the design system's §10, before any transcript text needs
  one. The web build must still fetch nothing from another host.

## Context

A session's transcript is not assembled by the client. The agent builds a
tree — turn, task, step, tool row — in `internal/viewmodel` and sends it
over ACP. The bridge serves the snapshot at
`GET /api/sessions/{id}/stack` and relays patches on the session's event
stream. A patch carries a revision, the revision it applies to, the root
ids, whole nodes to insert or replace, and removed ids. The client keeps a
map of nodes and refetches the snapshot after a reconnect or a revision
gap. Fold eligibility, receipt numbers, inferred headlines and the binding
of steps to tasks are computed in Go and arrive as fields.

The session routes this phase uses: `stack`, `nodes/{nodeId}`, `prompt`,
`steer`, `cancel`, `mode`, `load`, `access`, `roster`, and
`POST /api/permissions/{toolCallId}` and `POST /api/questions/{questionId}`
for asks. Starting an agent is `POST /api/agents`, with
`GET /api/prompts/recent`, `GET /api/recipes` and
`GET /api/repos/{id}/issues` behind the dialog's three lists.

The stack types are hand-written TypeScript in `lib/stack.ts` today.

## Goal

- Opening an agent thread shows its transcript, live.
- The user can prompt, steer, stop, change permission mode, and answer the
  agent's asks in place.
- Text in the conversation can be selected and copied while it streams.
- The new-work dialog starts an agent.
- The TUI attached to the same session shows the same transcript.

## Non-goals

- The inspector. Its column is not drawn in this phase; selecting a tool
  row shows its output inline, as density allows.
- Work items, plan runs, and the "Plan first" and "Work item" choices in
  the dialog. They are drawn disabled, in the planned state.
- Voice, attachments. Their buttons are drawn disabled with tooltips.

## Decisions

1. **Contract first.** The stack node, the stack patch, the session
   summary, the prompt, steer and mode requests, the roster, and the
   permission and question answers move into `web/bridge/contract` as Go
   types, the bridge uses them, and TypeScript and Dart are regenerated.
   `lib/stack.ts` then re-exports the generated types and keeps only its
   functions.
2. **The tree is rendered, never regrouped.** `model/stack_store.dart`
   holds the node map and root order for one session and applies patches
   by the revision rule. It exposes nodes; it derives nothing the agent
   already sent. A patch whose base revision is not the store's current
   one triggers a snapshot fetch and is otherwise discarded.
3. **Client state is separate and small.** Density, per-node fold
   overrides, the browse cursor and scroll position live in
   `model/thread_view_state.dart`, per thread, and survive switching
   threads within a run of the app.
4. **Rows.** The kit gains exactly these, by the design system's §10
   procedure: `TurnPrompt` (what the user said), `StepHeader`, `ToolRow`,
   `Narration`, `OutputBlock`, `LiveRow`, and `Composer`. Each maps to one
   node kind or one part of the screen. Markdown in narration is rendered
   with a package chosen and pinned in this phase.
5. **Selection.** Follow `findings/transcript-selection.md`. Whatever it
   recommends, the acceptance is fixed: select across several rows, with
   rows arriving, and copy plain text that reads in order.
6. **Following.** The view follows the live end while the user is at the
   end. Scrolling up stops following and shows one "Jump to live" control
   carrying the live headline. Returning to the end resumes.
7. **Keys.** The browse keys are the TUI's: `Esc` enters and leaves browse
   mode; `j` `k` `J` `K` `g` `G` move; `Enter` cycles density; `z` folds;
   `y` copies; `i` is reserved for the inspector in F4. In approval cards,
   `y` `n` `a`. `⌘N` opens the dialog. All are listed in one map that
   tooltips read from.
8. **Composer.** One field. While the agent is running, sending steers and
   a Stop button is present; otherwise sending prompts. The permission mode
   and model are chips. A draft is kept per thread and survives a
   reconnect.
9. **Asks in place.** The same `AskCard` as the Inbox, pinned at the step
   that raised it. Answering in either place resolves both.
10. **Thread header.** Project, kind, branch, environment and host on one
    line; status dot, title and headline on the next; share and the
    overflow menu on the right. Share is drawn disabled until F7.
11. **New-work dialog.** Prompt first. Chips for project, base branch,
    environment, model, permission mode, workspace and host, each defaulted
    from the project and each opening a `Menu`. Issues, recent prompts and
    recipes fill the prompt when chosen. It posts the spawn request and
    opens the new thread.
12. **A session the user can only read** — the access route says so — shows
    the transcript with the composer replaced by one line saying why.

## Work

Suggested pull requests, in order.

1. **Contract.** The Go types, the bridge switched to them, both generated
   files, fixtures, and the Svelte client re-pointed.
2. **Stores.** `stack_store.dart` and `thread_view_state.dart`, with tests
   that replay recorded snapshot-and-patch sequences, including a gap and a
   reconnect. Record the sequences from a real session and check them in
   as fixtures.
3. **Rows.** The seven kit additions, the design-system change, gallery and
   goldens.
4. **The thread screen.** Header, virtualised transcript, following,
   density, folding, browse keys, selection.
5. **Composer and asks.** Prompt, steer, stop, mode; approvals and
   questions in place; the read-only state.
6. **New-work dialog.** With its three lists.
7. **End to end.** Start an agent from the dialog, steer it, answer one
   approval, let it finish; assert the transcript the app shows equals the
   plain-text projection the agent reports for the same session.

## Tests

- `web/bridge/contract`: fixtures for each new type; both currency tests.
- `app/test/model/`: `stack_store_test.dart` (snapshot, in-order patch,
  out-of-order patch, gap, removal, reconnect), `thread_view_state_test.dart`.
- `app/test/ui/thread/`: `transcript_test.dart`, `following_test.dart`,
  `browse_keys_test.dart`, `selection_test.dart`, `composer_test.dart`,
  `asks_in_place_test.dart`, `new_work_dialog_test.dart`.
- A performance check: a transcript of five thousand nodes scrolls without
  a dropped-frame budget breach on the CI runner; the budget is set in this
  phase and recorded.

## Done when

- A full session — start, steer, answer, finish — is driven from the app
  while the TUI is attached to the same session, and both show the same
  steps, headlines and receipts.
- Killing the connection mid-turn and restoring it leaves the transcript
  complete, with no duplicated or missing node.
- Selecting from the middle of one step to the middle of a later one, while
  output streams, copies readable text.
- The browser Studio at `/next/` does the same.

## Open questions

- Whether live tool output streams into a row from the existing tool-call
  updates or waits for the next stack patch. The Svelte client uses the
  updates; keep that unless the stack stream proves sufficient.
- The dropped-frame budget for the performance check.

## Implementation record (2026-11-17)

Both open questions are settled: live tool output is drawn from the tool-call
rows the stack already carries (a running call is a `WireTool.running`), and
the budget is **no frame over 32 ms** while scrolling a 5,000-node transcript
(`app/test/ui/thread/performance_test.dart`).

**What exists.** Tasks 1–8 of the plan, one commit each on
`studio/f3-agent-thread`:

1. `web/bridge/contract/stack.go` and `session.go`, the bridge switched to
   `contract.PromptRequest`/`SteerRequest`/`ModeRequest`, `stack.ts`
   re-exporting the generated types, and `test/contract/{stack,session}.json`
   in both round-trip tests.
2. `model/stack_store.dart` (revision rule, refetch on a gap or a reconnect,
   `plainText`), `model/thread_view_state.dart`, the new session routes on
   `client/bridge_client.dart`, and `test/fixtures/stack/`.
3. The seven kit components, the design system's §3/§5 entries, Noto Sans
   Symbols 2 as a fallback family, the gallery and its Linux goldens.
4. `ui/thread/thread_screen.dart`, `thread_header.dart`, `transcript.dart`,
   one key map (`threadKeys`), following and "Jump to live", and the thread
   place wired through `Studio`.
5. `model/selection.dart` and `ui/thread/selection_layer.dart`: model-backed
   anchors, pointer hit-testing to an anchor, per-row highlight boxes, edge
   auto-scroll, ⌘C/⌘A, word/line/shift-click and a Copy context menu.
6. `model/composer_model.dart`, `ui/thread/thread_composer.dart`, and asks
   pinned in the transcript as the Inbox's `AskCard`.
7. `model/new_work_model.dart` and `ui/new_work/new_work_dialog.dart`, opened
   from a thread with ⌘N; Start posts `SpawnRequest` and opens the agent.
8. The performance test, the end-to-end flow (`test/ui/thread/e2e_test.dart`
   and `integration_test/f3_test.dart`), and this record.

**Fixture provenance.** Tasks 1 and 2 ask for fixtures recorded from a real
session. This environment has no model provider, no credentials and no
network to an agent, so a live turn was impossible. The fixtures were instead
produced by the **production projection and patch code**: a temporary Go test
built `viewmodel.Node` trees, called `viewmodel.Project`, and ran
`acp.stackProjector.diff` on them, writing the snapshot and the patches it
returned. The JSON in `test/contract/stack.json` and
`app/test/fixtures/stack/*.json` is therefore genuine projector output, but
the transcript behind it is constructed, not recorded from a model's turn.
The recorder test was deleted after it wrote the files.

**Choices and deviations.**

- `SessionAccess` is `contract.AccessState`, the type the access routes
  already write; a second Go type for one wire shape would have been worse
  than reusing the name.
- `WireMessage.IsFinal` carries the JSON key `final`, because Dart reserves
  the bare word.
- `StackStore.plainText` for a receipt says what the row says (counts, then
  usage), so a copy of the transcript matches what is drawn.
- `Ask` gained an optional `step`, read from a pending request's `stepId`, so
  the transcript can pin the card where it was raised.
- The new-work dialog's three lists are the bridge's own routes. Recipes come
  from `GET /api/recipes` (`web/bridge/recipes_http.go`) through
  `BridgeClient.recipes` and the `contract.Recipe` declaration, whose type
  names and JSON keys are the ones `studio/f7-1-automations-library` also
  declares, so the two branches merge rather than diverge. Issues come from
  `GET /api/repos/{id}/issues` through `BridgeClient.issues` and
  `contract.RepoIssue`, which transcribes the keys the route actually sends
  (`web/bridge/forge.go`'s `Issue` carries no struct tags, so they are the Go
  field names) — `TestListRepoIssuesAnswerIsContractRepoIssue` in
  `web/bridge` holds the two together.
- **Issues need a repo id the app cannot yet name.** An issue belongs to a
  registered remote repo (`GET /api/repos` reports ids such as
  `owner/name`), while a project is a local checkout: no bridge route maps a
  project root to a repo id, and `GET /api/projects` carries none. So
  `NewWorkModel.forProject` takes an optional `repoId` and wires the route
  when it is given one; with none it asks nothing, and the dialog says "No
  registered repo for this project" rather than "No issues for this project"
  (`NewWorkModel.issuesFromRepo`). Disclosed, not papered over: the app gets
  the mapping with the project settings surface that names a project's repo
  (F6's project defaults, F7's repositories), and the client method and
  contract type are already in place for it.
- The two lists' `const []` fallbacks are for a genuine failure only: a route
  that answers non-2xx leaves that one list empty and the dialog's other
  lists loading (`a route that refuses leaves an empty list, not a failed
  dialog` in `app/test/model/new_work_model_test.dart`).
- The new-work dialog is a `Sheet`, not a `ScreenCase`: it is not registered
  in `test/support/screens.dart`, because the §7 states it has are its
  lists' empty states and a failure line, not a screen's.
- Selection highlight boxes are computed from each row's laid-out
  paragraphs; a row drawn from several paragraphs (a tool row) maps the
  selection onto them in order, which is exact for one paragraph and an
  approximation for several.
- The context menu is the kit's `Box` and `Btn` at the pointer: `Menu`
  anchors to a control, and a pointer anchor would be a §10 change to it.

**Environment-blocked, not failures.** `flutter test --platform chrome`
(no working browser: the Chrome host stalls on CanvasKit), so the browser
half of selection is unverified here. `flutter build linux` and
`flutter test integration_test -d linux` (no CMake/GTK3 headers, no display),
so `integration_test/f3_test.dart` is written but has not run. `npm run
check` has a pre-existing five errors, unchanged by this phase.
