# P8 focused input routing — implementation plan

Date: 2026-10-28. Status: executed (2026-10-28); see “Execution record” below.
Base: `34114b4f4771531fe6627f78c31ab60730e7c19f`.
Requirements: [triage item 9](../triage.md#p2--interaction-and-visibility),
including the audit findings for [plugin paste](../audit.md#plugin-manager-ignores-bracketed-paste),
[Escape in skill and plugin editors](../audit.md#escape-leaves-skill-and-plugin-frames-while-editing-a-field),
and [agent editor paste and question marks](../audit.md#agent-editor-misroutes-paste-and-intercepts-question-marks).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** send keyboard and bracketed-paste input to the visible focused
control, and make Escape cancel an active edit before leaving its frame.

**Non-goals:** change plugin installation/scan safety, skill identity or
runtime reload behavior, shared editor semantics, model selection, question
forms, web UI, or unrelated TUI shortcuts. This is the eighth plan in the
sequence and addresses one P2 group.

**Decisions and assumptions:**

- Input ownership is exclusive: plugin full-content review first; agent
  picker overlay first; then the active field editor or collection prompt;
  then the drilled frame; finally the root filter or panel shortcuts. A paste
  in a read-only plugin review is consumed without changing hidden state.
- For skills, plugins and agents, Escape while `FieldList.Editing()` is true
  goes to the field list and cancels that edit, enum picker or collection-add
  prompt. A later Escape returns one frame or closes the panel. The existing
  standalone agent picker and plugin review each handle Escape first.
- A paste into a drilled frame is offered to its active list. The existing
  field list may open an editable scalar on paste; paste on a non-text row
  does nothing. At an unedited root, paste filters the root list. Never send
  the same input to both a filter and an editor.
- `?` belongs to an active agent editor/collection prompt or picker. When no
  editor or picker owns focus, it continues to toggle the roster legend.
  Do not introduce a new global help binding.
- A cancelled plugin Source or Scope edit does not call a setter or invalidate
  a scan. A successful commit still follows the existing setter and
  `invalidateReview` path; leaving the frame still disposes the outstanding
  scan or review. Existing removal disarm and scan lease protections remain.
- Work from isolated fixtures and local sources with no real credentials,
  services, or ambient config. Preserve unrelated untracked work and stage
  named files only. Tasks are sequential and independently verifiable.

## Contracts

No exported or cross-task API changes. Use existing `FieldList.Editing()`,
`settings.FieldListUpdate`, `picker.Model.Update`, and the panel-owned stacks;
do not change `listpanel` or the picker package for this phase. The existing
`settings.BrowserClosedMsg`, picker result messages, plugin scan messages and
config persistence behavior stay unchanged.

## Task 1: Give skill editors first refusal on Escape

**Goal:** Escape cancels an in-progress skill field edit without closing its
frame, then a subsequent Escape navigates back.

**Files:** `internal/app/tui/skills/panel.go`,
`internal/app/tui/skills/panel_test.go`.

**Steps:**

1. In `Panel.Update`, within the `tea.KeyPressMsg` Escape branch, obtain
   `p.ActiveList()`. If it is `Editing()`, send Escape to that list through
   `settings.FieldListUpdate` and return its command without popping the
   frame, closing the panel, or invoking the setter. Otherwise keep the
   existing `DisarmCurrent`, stack pop, and `BrowserClosedMsg` paths.
2. Add `TestEscapeCancelsSkillEditorBeforeLeavingFrame`: open the real
   install frame through `Panel.Update`, edit and paste into Source, press
   Escape and assert the original committed source, frame, and filter are
   unchanged; reopen and commit a value with Enter; then assert the next
   Escape goes back and another closes. Include an active Scope enum picker,
   keyboard input, and paste delivered through `Panel.Update`.
3. Add `TestSkillEscapePreservesRemovalConfirmationSafety`: after arming a
   removal, leaving its detail frame still disarms it and returning requires
   two presses. Use existing temporary fixtures and removal helpers; do not
   delete outside the fixture.

**Verify:**

```bash
go test ./internal/app/tui/skills -run 'Test(EscapeCancelsSkillEditorBeforeLeavingFrame|SkillEscapePreservesRemovalConfirmationSafety)' -count=1
go test ./internal/app/tui/skills -count=1
gofmt -l internal/app/tui/skills
```

Expected: tests pass and gofmt prints no paths.

## Task 2: Route plugin paste and preserve scan ownership

**Goal:** plugin paste reaches its visible input and cancelling an edit does
not exit the install frame or abandon its scan.

**Files:** `internal/app/tui/plugins/panel.go`,
`internal/app/tui/plugins/panel_test.go`.

**Steps:**

1. In `Panel.Update`, retain `reviewDetail` as the first key owner. In the
   Escape branch, when `p.activeList().Editing()` is true forward Escape to
   that list and return; otherwise retain existing disarm, frame pop,
   `invalidateReview`, and panel-close behavior.
2. Add a `tea.PasteMsg` branch: if `reviewDetail != nil`, consume it. If a
   frame is drilled or the active list is editing, return
   `settings.FieldListUpdate(p.activeList(), msg)`. Otherwise forward to
   `p.filter.Update(msg)`, refresh `p.list`, and return the resulting command.
   Do not commit a pasted value until the editor's existing Enter path.
3. Add `TestPluginPasteFollowsFocus` using the real panel update path: paste
   a root query, paste a local source into an idle scalar and an active edit,
   and verify neither path changes the other input. Verify paste on a non-text
   row and in the full-content review changes neither source nor filter.
4. Add `TestPluginEscapeCancelsEditBeforeScanAbandonment` with the existing
   injected scan dependencies and a controlled delayed scan: while Source
   editing is active, Escape keeps the frame, committed source, lease and
   generation intact; a matching result can still be reviewed. After another
   Escape actually leaves the install frame, a late result is disposed by
   the existing lease path. Separately commit a changed Source and Scope and
   assert review invalidation still occurs. Include enum-picker cancellation
   and existing removal confirmation disarming.

**Verify:**

```bash
go test ./internal/app/tui/plugins -run 'Test(PluginPasteFollowsFocus|PluginEscapeCancelsEditBeforeScanAbandonment)' -count=1
go test ./internal/app/tui/plugins -count=1
go test -race ./internal/app/tui/plugins -count=1
gofmt -l internal/app/tui/plugins
```

Expected: tests pass; gofmt prints no paths; no network is used.

## Task 3: Route agent editor and picker input to their owners

**Goal:** paste and `?` reach the focused agent control, and Escape cancels
an active list edit before returning to the roster.

**Files:** `internal/app/tui/agents/panel.go`,
`internal/app/tui/agents/panel_test.go`.

**Steps:**

1. In `Panel.Update`, keep the picker overlay's existing first refusal on key
   events. Before the Escape and `?` shortcuts, check
   `p.activeList().Editing()`: forward either key to the list and pass the
   command through `p.maybePersist`. A cancelled edit produces no commit or
   persistence; when not editing, preserve existing stack pop, deletion
   disarm, panel close and legend toggle behavior.
2. Replace the `tea.PasteMsg` branch: send paste to `p.pickerModel.Update`
   while `pickerActive` and the model is non-nil; otherwise send it to the
   active list via `settings.FieldListUpdate` and `p.maybePersist` when
   drilled or editing; at an unedited root send it to `p.filter.Update` and
   refresh `p.list`. A picker paste filters its own choices and does not
   commit until its existing Enter/result path. If an enum picker owns the
   field list, paste has the shared list's existing non-text behavior.
3. Add `TestAgentPasteFollowsFocusedControl`: paste into the root filter, a
   drilled scalar edit, an idle editable scalar, the custom-name picker, and
   a choice picker; assert the hidden filter and previously committed config
   do not change. Finish with Enter and process the returned picker/list
   messages to verify the expected commit and existing persistence receipt.
4. Add `TestAgentQuestionMarkAndEscapeRespectEditor`: type `?` in a scalar
   and collection-add prompt and assert it becomes input, not a legend
   toggle; in a picker it reaches the picker. Escape cancels the field or
   prompt without changing saved config or popping the frame; subsequent
   Escape navigates back. When no editor is active, `?` still toggles the
   legend. Cover picker Escape and the existing delete confirmation behavior.

**Verify:**

```bash
go test ./internal/app/tui/agents -run 'Test(AgentPasteFollowsFocusedControl|AgentQuestionMarkAndEscapeRespectEditor)' -count=1
go test ./internal/app/tui/agents -count=1
gofmt -l internal/app/tui/agents
```

Expected: tests pass and gofmt prints no paths.

## Task 4: Verify focused input through the TUI host

**Goal:** a real model routes bracketed paste and Escape to the open panel
without modifying the chat composer underneath.

**Files:** `internal/app/tui/model_test.go`.

**Steps:**

1. Add `TestManagementPanelPasteAndEscapeThroughModel`, using isolated
   in-process fixtures and the existing `Model.Update`/dock test helpers.
   Table-drive a skill install Source, a plugin install Source, and an agent
   scalar or picker. For each, open the actual panel and editor, seed a chat
   draft, send `tea.PasteMsg` and Escape through `Model.Update`, and assert
   the visible control receives the paste, the first Escape cancels it and
   leaves the frame open, and the composer draft remains unchanged. Assert a
   subsequent Escape navigates out. Include 80×24 and a wider geometry.
2. Separately exercise the agent picker through its returned message path and
   verify a pasted `?` or typed `?` does not toggle the hidden legend. Keep
   tests local and assert observable state or emitted messages rather than
   assuming a non-nil command means a successful action.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestManagementPanelPasteAndEscapeThroughModel$' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
gofmt -l internal/app/tui/model_test.go
```

Expected: tests pass and gofmt prints no paths.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p8-focused-input ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/skills internal/app/tui/plugins internal/app/tui/agents internal/app/tui/model_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p8-focused-input go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Compare full-suite failures to the latest recorded baseline in triage and
report results without suppressing failures. The existing scripted usability
suite is help-only; do not claim it verifies management-panel paste. If a
controlling terminal is available, manually try keyboard and bracketed paste,
Escape and resize at 80×24, plus a multiplexer/SSH session. Otherwise record
the limitation. After verification update the three audit findings and triage
item 9 with actual evidence, and commit the named documentation and plan with
the implementation.

## Plan self-review

- Each finding has an owning task, named behavioral tests, and a runnable
  package verification. The host integration test proves focused input does
  not leak into the underlying composer.
- Existing scan leases, review provenance, config commit semantics and
  removal confirmation are explicitly preserved and tested.
- The plan is authored against the landed P7 commit; no implementation was
  performed during planning.

## Execution record

Executed 2026-10-28 from base `34114b4f` on branch `p8-focused-input`, one
verified commit per task:

| Task | Commit | Scope |
| --- | --- | --- |
| 1 | `52da0209` | skills: Escape cancels a field edit, enum picker or add prompt first |
| 2 | `8be3d5c1` | plugins: paste follows focus; Escape keeps the frame and its scan lease |
| 3 | `b591a1da` | agents: `?` and Escape reach the active editor; paste follows focus |
| 4 | `66f4d9ab` | host: focused input verified through `Model.Update` at 80×24 and 140×40 |

`gofmt` was clean for every touched package after each task and
`git diff --check` reports nothing. Final verification:

- `go build ./cmd/marshal` — succeeds.
- `go test ./internal/app/tui/... -count=1 -timeout 2m` — passes, including
  `skills`, `plugins` and `agents`.
- `go vet ./...` — clean.
- `go test ./test/usability/... -run TestScripted` against the built binary —
  passes. It remains help-only; it does not exercise management-panel paste.
- `go test ./... -timeout 8m` — no new failures against the baseline recorded
  in triage: the same four packages fail (`internal/app`, `internal/snapshot`,
  `internal/tools/desktop/browser`, `internal/worktree`), and every package
  this phase changed passes.

Limitations: no controlling terminal, multiplexer or SSH environment was
available, so keyboard and bracketed paste were exercised through the model and
panel update paths rather than by hand. No behaviour outside the three panels
and their tests changed.
