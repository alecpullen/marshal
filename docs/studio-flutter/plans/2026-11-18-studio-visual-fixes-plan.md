# Studio visual fixes

A plan for the defects one screenshot of a live agent thread shows (Studio
on a tailnet host, 2026-11-18): an agent waiting on a question, after a
snapshot warning, with the Changes tab open. Each defect below was traced to
its code before it was written down. The plan has no separate spec: the
**Findings** section is the spec.

- **Authored against**: `origin/main` at `958e2ec9`.
- **Executed as**: one commit per task, `studio-visual-fixes: task N — <title>`.
- **Verification runs on Linux**, where the factory, CI and the gallery
  goldens run (`app/test/gallery_test.dart` pins goldens to Linux).
- **Design system binds every UI task**: `docs/studio-flutter/design-system.md`.
  Tasks 6 and 8 extend the kit, so each changes that document, the kit, the
  gallery and the goldens together (§10).

## Findings

Screenshot positions are given as the defect appears; anchors are on
`958e2ec9`.

1. **The Changes tab shows `TypeError: null: type 'minified:xp' is not a
   subtype of type 'List<dynamic>'`.** An agent that has changed no file
   answers `GET /api/agents/{id}/diff` with `"files": null`:
   `parseNumstat` (`internal/acp/worktree.go:237`) starts from a nil slice,
   and the bridge relays the bytes unchanged (`web/bridge/http.go:736`).
   The generated `_list` (`web/bridge/contract/dartgen/dartgen.go:509`,
   emitted at `app/lib/src/client/contract.g.dart:19`) casts `null` to
   `List`. Go writes every nil slice and nil map as `null`, so any required
   array or object can fail the same way.
2. **Failures are shown as the exception's text.** `ChangesModel.refresh`
   sets `failureMessage = '$error'` (`app/lib/src/model/changes_model.dart:345`);
   26 other sites under `app/lib/src/model/` do the same. design-system §7
   asks for what failed in plain words. `describeFailure`
   (`app/lib/src/model/fleet_store.dart:781`) already words a bridge
   refusal, but returns anything else verbatim.
3. **An inspector tab's state message touches the panel's edge.** The
   Changes tab's ready content is inset `Space.s16` by its `ListView`
   (`changes_tab.dart:81`), but its loading and failed branches
   (`changes_tab.dart:69-75`) and its fixture states (`_stateOnly`, `:41`)
   are drawn bare. Files, Preview, Inspect and Terminal do the same.
4. **A harness notice reads as the agent speaking.** The snapshot warning is
   a `RoleSystem` message (`internal/agent/snapshot_warning.go:521`); the
   transcript draws every non-user message as `Narration`
   (`app/lib/src/ui/thread/transcript.dart:425-430`), in the agent's reading
   style, all six lines open, its paths breaking mid-word.
5. **The thread is called `03423874cb70374a`.** `threadFor` falls back to the
   agent id when the agent has no name (`app/lib/src/model/thread_ref.dart:155`).
   The bridge keeps the spawn prompt (`Agent.Prompt`,
   `web/bridge/workspace.go:62`) but `AgentStatus` does not carry anything
   made from it (`web/bridge/fleet.go:2150`). The id is both the sidebar row
   and the header title.
6. **The ask's wait is measured from the agent's last update.**
   `_askForPending` uses `lastActiveOf(agent)` (`inbox_model.dart:790-799`),
   which is `AgentStatus.updatedAt`; the bridge moves that on every status
   delta and access report (`web/bridge/fleetevents.go:190, 229`), so a
   parked question can keep reading `waiting now`. `PendingRequest` has no
   time of its own (`web/bridge/contract/agents.go:98`).
7. **The question's options lose their descriptions.** The `question` tool
   accepts `{label, description}` options
   (`internal/tools/native/question.go:23`); `_questionsOf` keeps only the
   label (`inbox_model.dart:850-861`), so the options are bare words.
8. **The header names the project by its full root path.** The identity
   line interpolates `thread.project`, the root
   (`app/lib/src/ui/thread/thread_header.dart:53-55`), while the sidebar
   calls the same project `marshal` (`projectName`,
   `app/lib/src/model/nav_model.dart:260`).
9. **The header repeats the question.** The headline beside the title is
   `describePending` — the ask's own title — while the card that asks it sits
   in the conversation below.
10. **Two sidebar rows are filled for one place.** `_ProjectBuilder.build`
    marks a project active when one of its threads is open
    (`nav_model.dart:238-241`), and the sidebar fills an active project row
    as selected (`app/lib/src/ui/shell/sidebar.dart:185`), so the project and
    its thread are both drawn as where the person is.
11. **The sidebar's search has a stray `Search` label above it.** The planned
    search is a `Field`, whose label is required and drawn above it
    (`sidebar.dart:103-107`, `app/lib/src/ui/kit/field.dart:93`).
12. **The sidebar's edges are ragged.** The host switcher and search are
    inset 12 (`sidebar.dart:76-81`); a `NavRow`'s icon sits at 10 (gutter 8
    plus `NavRow`'s left padding `Space.s2`, `nav_row.dart:70-71`) and its
    count ends at 236; the `Projects` label sits at 16 and its plus ends at
    236 (`sidebar.dart:128-134`) while the search's plus ends at 240.
13. **`Pull requests 0` and an Inbox count of 0 are drawn.** `buildNavModel`
    passes the counts through as ints (`nav_model.dart:139, 145`); the test
    beside it says the empty row "says nothing" but asserts `0`
    (`app/test/model/nav_model_test.dart:257-261`).
14. **`Earlier · 49` is in `faint`.** `sidebar.dart:215`; design-system §2:
    `faint` never carries information.
15. **The composer's controls float in its middle.** `Composer` lays its
    chips and buttons in a `Wrap` inside a `Column` whose cross axis is
    centred (`app/lib/src/ui/kit/composer.dart:132-151`); a `Wrap` is as wide
    as its run, so the whole row is centred.
16. **The composer is wider than the conversation.** The transcript stops at
    `Layout.reading` (`transcript.dart:262-268`); `_composer` pads the
    composer by `Layout.pageSide` and nothing else
    (`app/lib/src/ui/thread/thread_screen.dart:564-576`), so it spans the
    whole column.

Left as designed, not changed here: the first option of a question is drawn
primary (`inbox_model.dart:1056`, an F2 decision); a pinned ask is indented
`Sizes.row`, which aligns it with tool rows (`tool_row.dart:64-65`), not with
narration. The snapshot store fault behind the warning
(`unparseable ref line`) is a store problem, not a Studio one.

## Non-goals

- Making the question's first option plain, or adding a recommendation flag
  to the `question` tool.
- Search itself; the field stays planned.
- Reaching the earlier threads behind `Earlier · N`.
- Changing what the snapshot warning says, or how the TUI draws it.
- Any change to the TUI or `internal/viewmodel`.

## Assumptions

- `fleet.Diff` (`web/bridge/fleet.go`) relays `session/diff`'s JSON
  unchanged for every agent, so fixing the ACP answer fixes the route.
- `relativeAge` (`app/lib/src/model/age.dart:10`) stays the one way a wait
  is worded; with a correct `since` it reads `waiting 8m` as designed.
- The transcript's selection copies a node's text from the store
  (`selection_layer.dart:352`), and maps a drawn paragraph's offsets in paint
  order, so a row whose paragraphs are drawn in its text's order copies and
  highlights correctly.

## Contracts

Declarations only.

```go
// web/bridge/contract/agents.go — added fields

type AgentStatus struct {
	// …existing fields…

	// Title is what the agent was asked, in one line: the first non-blank
	// line of its spawn prompt, runs of whitespace collapsed to one space,
	// at most 80 runes, ending in "…" when cut. Empty when it was spawned
	// with no prompt.
	Title string `json:"title,omitempty"`
}

type PendingRequest struct {
	// …existing fields…

	// At is when the bridge first saw the request parked, in Unix
	// milliseconds. Zero (absent) when it did not see it arrive.
	At int64 `json:"at,omitempty"`
}
```

```go
// web/bridge/fleet.go

// agentTitle is AgentStatus.Title for a spawn prompt.
func agentTitle(prompt string) string
```

```dart
// app/lib/src/client/contract.g.dart (generated)
// AgentStatus:    final String? title;
// PendingRequest: final int? at;

// app/lib/src/model/inbox_model.dart
class AskOption {
  const AskOption(this.label, {String? value, this.description});
  final String label;
  final String value;

  /// What choosing it means, as the agent wrote it; null when it wrote none.
  final String? description;
}

// app/lib/src/ui/kit/notice.dart
/// Something the harness said in a conversation, not the agent.
class Notice extends StatefulWidget {
  const Notice({super.key, required this.text});
  final String text;
}

// app/lib/src/ui/kit/field.dart
/// The sidebar's search: no label above it; a magnifying glass leads it.
const Field.search({
  Key? key,
  TextEditingController? controller,
  String? hint,
  bool enabled = true,
  ValueChanged<String>? onChanged,
  ValueChanged<String>? onSubmitted,
});

// app/lib/src/ui/inspector/inspector.dart
/// A tab's state message, inset as the tab's content is.
class TabMessage extends StatelessWidget {
  const TabMessage(this.child, {super.key});
  final Widget child;
}
```

## Task 1: An untouched tree's diff lists no files

**Goal.** `session/diff` answers `"files": []`, never `null`.

**Files.** `internal/acp/worktree.go`, `internal/acp/worktree_test.go`

**Steps.**
1. In `parseNumstat` (`worktree.go:237`), start `files` as an empty,
   non-nil slice of `DiffFile`, so a diff with no lines returns `[]`.
   Add one line to its doc comment: never nil, because the wire writes a
   nil slice as `null` and clients decode `files` as a list.
2. In `worktree_test.go`, add `TestDiffOfAnUntouchedTreeListsNoFiles`:
   isolate a session as `TestDiffReturnsFileStatsWithoutPath` (`:157`) does,
   change nothing, call `Diff` with no path, marshal the result, and assert
   the JSON contains `"files":[]`.
3. Extend `TestParseNumstat` (`:123`) with an empty-input case asserting a
   non-nil, zero-length result.

**Verify.** `CGO_ENABLED=1 go test ./internal/acp -run 'TestDiff|TestParseNumstat'`
passes; with step 1 reverted, `TestDiffOfAnUntouchedTreeListsNoFiles` fails.

## Task 2: The Dart contract reads a null array or object as empty

**Goal.** A required list or map sent as `null` decodes empty, for every
generated type.

**Files.** `web/bridge/contract/dartgen/dartgen.go`,
`web/bridge/contract/dartgen/testdata/rules.dart`,
`app/lib/src/client/contract.g.dart` (regenerated),
`app/test/client/contract_fixture_test.dart`

**Steps.**
1. In `dartgen.go`, change the emitted `_list` helper (`:508-511`) so a
   `null` value yields an empty list, and `_map` the same for an empty map.
   Their doc comments say why: Go writes a nil slice or map as `null`, so a
   required array or object can arrive as `null` and means empty. Correct
   the comment at `:651` that says a required field is "never null".
2. Leave `_listOrNull` and `_mapOrNull` as they are: an optional field that
   is absent stays null.
3. Regenerate the rules fixture:
   `go test ./web/bridge/contract/dartgen -run TestGeneratorRules -update`.
4. Regenerate the app's module:
   `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
5. In `contract_fixture_test.dart`, add
   `'a required list sent as null decodes empty'`: `DiffResult.fromJson`
   of `{'files': null}` has no files and no diff, and `StepDiffs.fromJson`
   of `{'steps': null}` has no steps.

**Verify.** `go test ./web/bridge/contract/dartgen` passes, and
`cd app && flutter test test/client` passes. With step 1 reverted (and
regenerated), the new Dart test fails.

## Task 3: The bridge names an agent and says when its request was parked

**Goal.** `GET /api/agents` carries each agent's `title`, and a pending
request carries `at`, for agents and for sessions.

**Files.** `web/bridge/contract/agents.go`, `web/bridge/fleet.go`,
`web/bridge/fleetevents.go`, `web/bridge/registry.go`,
`web/bridge/fleet_test.go`, `web/bridge/fleetevents_test.go`,
`web/bridge/sessions_test.go`, `app/lib/src/client/contract.g.dart`
(regenerated)

**Steps.**
1. Add `AgentStatus.Title` and `PendingRequest.At` as Contracts declares.
2. Add `agentTitle` beside `agentDisplayName` (`fleet.go:2065`), standard
   library only (`web/` imports nothing else): take the first line that is
   not blank, collapse whitespace runs with `strings.Fields`, cut to 80
   runes and append `…` when cut. In `Snapshot` (`fleet.go:2150`), set
   `Title: agentTitle(a.Prompt)`.
3. Give `observedPending` (`fleetevents.go:126`) a field `at time.Time`. In
   `observePending`, keep the earlier `at` when the agent already holds a
   pending request with the same `id`; otherwise stamp `time.Now().UTC()`.
   In `Snapshot` (`fleet.go:2190`), set `At` from `live.pending.at` in Unix
   milliseconds when it is not zero.
4. In `Registry`, add `permAt` and `quesAt`, `map[string]time.Time`,
   initialised wherever `permParams` and `quesParams` are (`registry.go:185,
   188, 1363, 1372`), written where their params are written (`:1032,
   :1110`) and deleted wherever their params are deleted (`:965, :983,
   :1042, :1120, :1284, :1297`). `PendingRequest` (`:925`) sets `At` from
   them.
5. Regenerate: `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
6. Tests:
   - `fleet_test.go` `TestAgentTitle`: a table — empty, blank lines before
     the text, internal whitespace runs, exactly 80 runes (not cut), 81 runes
     (cut to 80 with `…`), multibyte runes counted as runes.
   - `fleet_test.go` `TestSnapshotTitlesAnAgentByItsPrompt`: an agent
     spawned with a two-line prompt reports its first line as `Title`.
   - `fleetevents_test.go` `TestSnapshotPendingCarriesWhenItWasParked`: as
     `TestSnapshotCarriesPendingQuestionPayload` (`:144`), and `At` is set
     and not after the time read after `observePending`.
   - `fleetevents_test.go` `TestPendingParkTimeOutlivesStatusUpdates`:
     observe a question, apply a status delta and an access report, observe
     the same question again; `At` is unchanged while `UpdatedAt` moved.
   - `sessions_test.go` `TestPendingRequestCarriesWhenItWasParked`: a
     registry question and approval each report a non-zero `At`, and a
     resolved one is gone with its time.

**Verify.** `go test ./web/bridge/... ./web/bridge/contract/dartgen` passes,
and `go test ./web/bridge -run TestWebIsStdlibOnly` still passes.

## Task 4: Threads are named, asks are timed, options are described

**Goal.** An unnamed agent's thread is called by what it was asked; an ask
waits from when it was parked; a question lists what its options mean;
an empty count is not drawn.

**Files.** `app/lib/src/model/thread_ref.dart`,
`app/lib/src/model/inbox_model.dart`, `app/lib/src/model/nav_model.dart`,
`app/test/model/thread_ref_test.dart`, `app/test/model/inbox_model_test.dart`,
`app/test/model/nav_model_test.dart`

**Steps.**
1. In `threadFor` (`thread_ref.dart:149`), the title is the agent's name;
   else its trimmed `title`; else `Agent ` and the first eight characters
   of its id, as `_sessionTitle` (`:199`) does for a session.
2. In `inbox_model.dart`, add a private `_parkedAt(PendingRequest pending,
   DateTime fallback)`: the request's `at` as a UTC time when it is present
   and positive, else `fallback`. `_askForPendingOn` (`:804`) uses
   `_parkedAt(pending, since)` for the ask's `since`, so both the agent path
   (`:790`) and the session path (`:659`) take it.
3. Give `AskOption` its `description` (Contracts). In `_questionsOf`
   (`:835`), read `option['description']` from a map option, trimmed, null
   when absent or blank. Update the doc comment's payload shape to
   `{label, value, description}`.
4. `_questionBody` (`:917`): for one question, return one line per option
   that has a description, as `<label> — <description>`, joined with
   newlines, or the empty string when none has one. For two or more
   questions it keeps `N more questions`.
5. In `buildNavModel` (`nav_model.dart:139, 145`), pass a count of zero as
   null, so an empty Inbox or Pull requests row draws no count.
6. Tests:
   - `thread_ref_test.dart`: `'an unnamed agent is called by its title'`,
     `'an agent with neither is called by its short id'`, and
     `'a name wins over a title'`.
   - `inbox_model_test.dart`: `'a parked request waits from when it was
     parked'` (an agent whose `updatedAt` is newer than its pending `at`:
     `since` is `at`), `'a request with no park time waits from the agent'`,
     and `'a question lists what its options mean'` (one question, two
     options, one described: the body is that option's one line).
   - `nav_model_test.dart`: in `'inbox count follows the store'` (`:241`),
     the first expectation becomes `isNull`, and the final list of rows with
     a count becomes `['Inbox']` (no pull request is ready in that fixture).

**Verify.** `cd app && flutter test test/model` passes.

## Task 5: Failures are in plain words, inset as the tab's content is

**Goal.** No screen shows an exception's text, and an inspector tab's state
message sits on the tab's own inset.

**Files.** `app/lib/src/model/fleet_store.dart`; the model files that
interpolate an error (listed in step 2); `app/lib/src/ui/inspector/inspector.dart`,
`changes_tab.dart`, `files_tab.dart`, `preview_tab.dart`, `inspect_tab.dart`,
`terminal_tab.dart`; `app/test/model/fleet_store_test.dart`,
`app/test/design_rules_test.dart`, `app/test/ui/inspector/changes_test.dart`

**Steps.**
1. `describeFailure` (`fleet_store.dart:781`): a `TypeError` or a
   `FormatException` becomes `The bridge sent an answer this Studio cannot
   read. The bridge and the Studio may be different versions.` A
   `BridgeFailure` keeps its current wording; anything else keeps its
   `toString()`. Update its doc comment.
2. Replace each `'$error'` and `error.toString()` that becomes text a screen
   shows with `describeFailure(error)`: `share_model.dart:58, 78`;
   `new_work_model.dart:481, 620, 649`; `preview_model.dart:69`;
   `inspect_model.dart:102`; `connect_repo_model.dart:81, 97`;
   `composer_model.dart:195` (`_words` returns `describeFailure(error)`);
   `run_model.dart:445, 510`; `files_model.dart:144`;
   `changes_model.dart:345, 363, 523`; `drafts_model.dart:272, 376, 434,
   454`; `planning_model.dart:365, 385, 398, 427, 453`;
   `handoff_model.dart:654`; `environment_model.dart:347`;
   `general_model.dart:193`; `inbox_model.dart:462`;
   `pulls_model.dart:223, 244`; `revision_model.dart:70`. Import
   `fleet_store.dart` with `show describeFailure` where a file lacks it.
3. Add `TabMessage` to `inspector.dart` (Contracts): its child inside
   `Padding(EdgeInsets.all(Space.s16))`, the inset the Changes tab's ready
   `ListView` uses.
4. Wrap in `TabMessage`: in each of the five tabs, what `_stateOnly`
   returns (`changes_tab.dart:41`, `files_tab.dart:81`,
   `preview_tab.dart:49`, `inspect_tab.dart:41`, `terminal_tab.dart:62`);
   and `changes_tab.dart` `_body`'s loading and failed branches (`:69-75`);
   `files_tab.dart` `_tree`'s loading and error returns (`:129-136`);
   `preview_tab.dart` `_preview`'s error and loading returns (`:124-129`);
   `inspect_tab.dart` `_body`'s empty, loading and error returns (`:70-85`);
   `terminal_tab.dart` `_body`'s error return (`:102-109`). A message already
   inside a padded list (`preview_tab.dart:85`, `changes_tab.dart:87`) is
   not wrapped.
5. Tests:
   - `fleet_store_test.dart`: `'a decode failure is worded, not dumped'`
     (a `TypeError` from casting `null` to `List` gives the sentence of
     step 1) and `'an unknown error keeps its own words'`.
   - `design_rules_test.dart`: a rule `'models word their failures'` that
     reads each file under `lib/src/model/` except `fleet_store.dart` and
     fails on `'$error'`, `"$error"` or `error.toString()`.
   - `changes_test.dart`: `'a failed read says so in words, inset'` — a
     fake client whose `diff` throws a `TypeError`: the tab shows step 1's
     sentence, not `TypeError`, and its icon's left edge is `Space.s16`
     from the tab's left edge.

**Verify.** `cd app && flutter test test/model test/design_rules_test.dart test/ui/inspector test/states_test.dart test/overflow_test.dart`
passes.

## Task 6: A harness notice is drawn as a notice

**Goal.** A system message in a conversation is one quiet line with its
detail folded, never the agent's prose.

**Files.** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/notice.dart` (new), `app/lib/src/ui/kit/kit.dart`,
`app/lib/src/ui/gallery/gallery.dart`, `app/lib/src/ui/thread/transcript.dart`,
`app/test/kit/notice_test.dart` (new), `app/test/ui/thread/transcript_test.dart`,
`app/test/goldens/` (regenerated)

**Steps.**
1. design-system.md §5: add a `Notice` row to the kit table — "Something
   the harness said in a conversation: a snapshot warning, a rollback, a
   compaction. Its first line in `small` `muted`; the rest folded behind
   *Details*. No icon, fill or border. Never the agent's words, which are
   `Narration`". Add an "Added for the visual fixes" paragraph after the F6
   ones saying why: a warning drawn as narration reads as the agent speaking.
2. `notice.dart`: `Notice` (Contracts). The text is split on newlines and
   blank lines dropped. The first line is drawn in `small` tinted `muted`,
   wrapping, never truncated. When more lines remain, a text control
   `Details` (closed) or `Hide details` (open) follows, a `Press` drawn in
   `small` `muted` that turns `fg` on hover. Open, the remaining lines are
   drawn beneath the first, each in `small` `muted`, `Space.s2` apart, and
   the control moves below them, so paragraphs are drawn in the text's
   order. It starts closed. Spacing: `Space.s4` above the control, and the
   whole notice has `Space.s12` below it, as `Narration` has.
3. Export it from `kit.dart`; add a `Notice` section to the gallery after
   `Narration` with two specimens: one line, and the snapshot warning's
   four-line shape (any paths; nothing from a real machine).
4. `transcript.dart:425-430`: a `message` whose role is `system` is a
   `Notice` of its content; the user's stays a `TurnPrompt` and anything
   else stays `Narration`. At outline density it is hidden, as narration is.
5. Regenerate the goldens on Linux:
   `cd app && flutter test --update-goldens test/gallery_test.dart`.
6. Tests:
   - `notice_test.dart`: `'one line has no Details'`, `'the rest is hidden
     until Details opens it'`, `'Hide details folds it again'`, and
     `'it is drawn in the notice style, not the reading one'` (the first
     line's style is the `small` role tinted `muted`).
   - `transcript_test.dart`: `'a system message is a notice, not
     narration'` — a fixture with a user prompt, an assistant message and a
     system message: one `TurnPrompt`, one `Narration`, one `Notice`.

**Verify.** `cd app && flutter test test/kit/notice_test.dart test/ui/thread test/gallery_test.dart test/tooltips_test.dart test/design_rules_test.dart`
passes on Linux.

## Task 7: The composer sits under the conversation, its actions at the end

**Goal.** The composer is as wide as the reading column, with its chips at
the start of its row and Stop and Send or Steer at the end.

**Files.** `app/lib/src/ui/kit/composer.dart`,
`app/lib/src/ui/thread/thread_screen.dart`, `app/test/kit/composer_test.dart`,
`app/test/ui/thread/composer_test.dart`, `app/test/goldens/` (regenerated)

**Steps.**
1. In `Composer.build`, make the editing branch's `Column`
   (`composer.dart:132`) stretch its cross axis. Replace the single `Wrap`
   (`:151`) with a `Wrap`
   whose alignment is `WrapAlignment.spaceBetween`, `runSpacing`
   `Space.s6`, holding two groups, each a `Wrap` with `spacing`
   `Space.s8` and centred cross alignment: the start group is the mode
   chip, the model chip, Attach and Dictate; the end group is Stop (while
   running) and Send or Steer. On a narrow composer the end group wraps
   below the start group and stays at the end.
2. In `_composer` (`thread_screen.dart:564`), centre the composer at the top
   of its space and cap it at `Layout.reading`, as the transcript's rows are
   (`transcript.dart:262-268`), keeping the `Layout.pageSide` padding
   outside the cap.
3. Regenerate the goldens on Linux (the Composer specimens move).
4. Tests:
   - `kit/composer_test.dart`: `'the chips lead and the actions trail'` —
     at 700 wide, the mode chip's left edge is within `Space.s12` of the
     composer's left edge and the Send button's right edge within `Space.s8`
     of its right edge.
   - `ui/thread/composer_test.dart`: `'the composer is as wide as the
     reading column'` — at 1440 wide, the `Composer`'s width is
     `Layout.reading` and its centre is the transcript's centre.

**Verify.** `cd app && flutter test test/kit/composer_test.dart test/ui/thread test/overflow_test.dart test/gallery_test.dart`
passes on Linux; the phone-sized test (`composer_test.dart:200`) still
keeps Send reachable.

## Task 8: One selection and one edge in the sidebar

**Goal.** Only the open place is filled; the planned search has no stray
label; every sidebar row's content sits 12 from each edge; earlier work is
readable.

**Files.** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/field.dart`, `app/lib/src/ui/kit/nav_row.dart`,
`app/lib/src/ui/shell/sidebar.dart`, `app/lib/src/model/nav_model.dart`,
`app/lib/src/ui/gallery/gallery.dart`, `app/test/model/nav_model_test.dart`,
`app/test/ui/shell_test.dart`, `app/test/kit/field_test.dart`,
`app/test/goldens/` (regenerated)

**Steps.**
1. design-system.md: in §5's variants paragraph, add `Field.search` — "the
   one field without a label above it: the sidebar's search, which a
   leading magnifying glass names". In the F2 `NavRow` paragraph, say the
   selected row is the open place alone; a project whose thread is open is
   not filled.
2. `field.dart`: add `Field.search` (Contracts). `label` becomes nullable,
   set by the other two constructors as before; the search variant draws no
   label, leads its input with `Glyphs.search` at `Sizes.iconRow` in
   `muted`, `Space.s8` before the text, and its semantics label is
   `Search`. Gallery: add its specimen to the Field section.
3. `nav_model.dart:238`: a project is active only when its own page is
   open. Update `NavProject.active`'s comment (`:81`).
4. `nav_row.dart:70-71`: the row's left padding is `Space.s4` (unchanged
   `Space.s16` when indented) and its right padding `Space.s4`.
5. `sidebar.dart`:
   - the planned search (`:103-107`) is `Field.search` with the same hint,
     `enabled: false` and the same `Tip`;
   - the `Projects` label row's padding (`:128-134`) is `Space.s4` at both
     sides, keeping `Space.s6` above and `Space.s4` below;
   - `Earlier · N` (`:215`) is tinted `muted`.
6. Regenerate the goldens on Linux (Field and NavRow specimens move).
7. Tests:
   - `nav_model_test.dart`: `'an open thread lights its project row'`
     (`:224`) becomes `'an open thread lights only its own row'`, expecting
     `[false, false]`; add `'the project page lights its project row'`.
   - `field_test.dart`: `'the search field has no label above it'`.
   - `shell_test.dart`: `'one row is filled for the open thread'` — with a
     thread open, exactly one row in the sidebar has the `selected` fill;
     `'the sidebar rows share one edge'` — the host switcher's, the search
     field's, the Overview icon's and the `Projects` label's left edges are
     equal, and the search's plus and the `Projects` plus share a right
     edge.

**Verify.** `cd app && flutter test test/model/nav_model_test.dart test/kit test/ui/shell_test.dart test/ui/settings_test.dart test/ui/inspector/files_test.dart test/gallery_test.dart test/overflow_test.dart`
passes on Linux (Settings and Files also draw `NavRow`).

## Task 9: The header names the project and does not repeat the ask

**Goal.** The identity line reads `marshal › Agent · <host>`, with the root
in its tooltip; the headline is not drawn while the thread waits on the
person.

**Files.** `app/lib/src/ui/thread/thread_header.dart`,
`app/test/ui/thread/thread_header_test.dart` (new)

**Steps.**
1. In `ThreadHeader.build` (`thread_header.dart:53`), the visible identity
   uses `projectName(thread.project)` (import `nav_model.dart` with
   `show projectName`); the `Tip` carries the same line with the full root.
2. Draw the headline (`:96-104`) only when it is not empty and the thread's
   status is not `needsYou`; the ask in the conversation is where the
   question is read, and the dot's tooltip already says *Needs you*.
3. Tests, pumped through `EmberApp` with a `ThreadRef` built directly:
   `'the identity line names the project, the tooltip its root'` and
   `'a thread that needs you has no headline'` (a running thread with a
   headline still shows it).

**Verify.** `cd app && flutter test test/ui/thread test/tooltips_test.dart test/overflow_test.dart`
passes.

## Final verification

1. `gofmt -l internal web` prints nothing; `go vet ./internal/acp/... ./web/...`.
2. `scripts/test.sh` — the packages the diff touches, uncached.
3. `go test ./web/bridge/contract/dartgen` — the Dart module is current.
4. `cd app && dart format --set-exit-if-changed lib test && flutter analyze`.
5. `cd app && flutter test` — the whole Dart suite on Linux, goldens
   included.
6. A manual look: `scripts/dev-studio.sh`, then open an agent thread that
   has changed nothing and waits on a question. Expect: the thread named by
   its prompt in the sidebar and header, one filled sidebar row, the header
   reading `marshal › Agent · <host>` with no repeated question, a snapshot
   warning (if one fires) as one muted line with *Details*, the question's
   option descriptions under it, `waiting` counting up across status
   updates, the Changes tab saying the agent has not changed a file, and
   the composer under the conversation with Send at its right. The studio
   restart ends running turns, so it is the last step.

## Integration notes

- The tasks run in order. Tasks 1–3 are Go and land before task 4, which
  reads the new fields; task 2 lands before task 3 regenerates the module.
  Tasks 4, 5 and 8 share model files, and tasks 6, 7 and 8 each regenerate
  the goldens, so none is parallel-safe.
- `contract.g.dart` is regenerated by tasks 2 and 3 and never hand-edited.
- No route is added: `route-parity.md` is untouched.
- An older bridge sends no `title` and no `at`: the Studio falls back to the
  short id and the agent's update time, as today.
