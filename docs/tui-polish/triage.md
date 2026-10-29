# TUI polish triage

This is the proposed order for the findings in [the audit](audit.md). The
priorities reflect user impact and fix order, not a claim that every finding
has been reproduced interactively. Entries are grouped by the behavior and
shared implementation they affect; each group should be verified against its
linked audit entries before implementation.

**P0** means a confirmed user action can delete unrelated data. **P1** means
an approval can be misleading, an existing draft or installation can be lost,
or a core workflow is materially broken. **P2** means a recoverable usability
or visibility problem. **P3** means cleanup or a test maintenance issue.

## P0 — contain skill removal

1. **Remove the selected skill by its actual source identity.** Address
   [skill removal path traversal and incorrect targets](audit.md#skill-removal-reconstructs-the-wrong-filesystem-path)
   first. Preserve discovery's file or bundle path, validate scope containment,
   and avoid deriving a deletion target from frontmatter. Verify with temporary
   fixtures for traversal names, single-file skills, mismatched bundle names,
   and both scopes. Include confirmation-state isolation in the same area:
   [armed removal persists across navigation](audit.md#skill-and-plugin-removal-confirmations-remain-armed-after-navigation).

   **Status (2026-10-27): done** — tasks 1–3 of the
   [P0 safe-removal plan](plans/2026-10-27-p0-safe-removal-plan.md) (commits
   `943d090d`, `88a558e2`, `573d6d00`). Discovery records the direct child and
   an `Lstat` snapshot, removal runs under `os.Root` on that source alone, and
   both panels key their confirmation by scope plus entry identity and disarm
   it on navigation or frame exit. Fixture tests cover traversal names,
   single-file and bundle skills, mismatched frontmatter names, both scopes,
   symlinked and replaced entries, and confirmation isolation. Full suite: no
   new failures against the recorded baseline (22 pre-existing failures in
   `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser` and
   `internal/worktree`); the scripted usability suite passes.

## P1 — restore trustworthy decisions and prevent data loss

2. **Repair approval and sudo presentation as one decision flow.** Cover
   [missing approval details](audit.md#interactive-approvals-omit-the-details-being-approved),
   [clipped shell commands](audit.md#long-shell-commands-are-clipped-in-the-interactive-approval-prompt),
   [oversized inline prompts](audit.md#oversized-inline-prompts-can-lose-their-first-rows),
   and [Escape retaining an armed approval](audit.md#escape-does-not-disarm-a-pending-approval).
   The complete operation and selected decision must remain reviewable at the
   supported minimum size, and cancellation must not leave an approval armed.
   Exercise long commands, diffs, arguments, reasons and resize through the real
   update path, including the sudo path.

   **Status (2026-10-27): done** — tasks 1–4 of the
   [P1 approval and sudo plan](plans/2026-10-27-p1-approval-sudo-plan.md)
   (commits `48bd264d`, `a4f07e36`, `e6ec9208`, `ab9accdf`). Escape disarms an
   armed action and the hints name both two-press gestures; a reusable
   `promptReview` wraps and pages the body at a fixed physical height; approval
   and sudo build that body from the sanitized operation and pin their controls
   below it; page keys page the prompt while `Ctrl+U`/`Ctrl+D` and the mouse keep
   scrolling the transcript; and a prompt that is below the supported minimum,
   behind a full-frame dock, or has no body rows accepts no positive decision
   while Escape still denies. Tests exercise the real `Model` update and render
   path at 120×40 and 80×24, including long commands, diffs and reasons, resize,
   prompt replacement, forced choices and two-step confirmation. Full suite: no
   new failures against the recorded baseline (pre-existing failures remain in
   `internal/snapshot`, `internal/tools/desktop/browser` and
   `internal/worktree`); the TUI packages, build, `go vet`, `gofmt` and the
   scripted usability suite are clean.

3. **Bind plugin confirmation to the reviewed content and protect installation.**
   Group [changed source after scan](audit.md#plugin-install-can-confirm-a-scan-of-a-different-source),
   [clipped executable commands](audit.md#plugin-review-clips-executable-commands-without-a-detail-view),
   [orphaned scan results](audit.md#leaving-an-in-flight-plugin-scan-loses-ownership-of-its-result),
   and [scan cleanup racing installation](audit.md#leaving-a-plugin-install-can-remove-the-clone-during-its-copy).
   Correlate scan results with their flow, make reviewed content accessible,
   and stage replacements so failure does not destroy an existing install.
   Verify with controlled delayed scan/copy and failure fixtures.

   **Status (2026-10-27): done** — tasks 1–4 of the
   [P2 plugin install safety plan](plans/2026-10-27-p2-plugin-install-safety-plan.md)
   (commits `5f129508`, `9ab6d5bb`, `c6db5b98`, `daf74b1b`, plus review fixes in
   `f0076e65`). The dock notifies a
   panel the host permanently releases (`CloseAware`/`SwapRetained`/
   `ReleaseRetained`), so a released panel disposes of work whose result will
   never reach it. A scan lease owns each scan's temporary clone until exactly
   one claimant takes it, and a review is bound to the frame generation, the
   entered source and the scope it was scanned for; a late or superseded result
   cannot become the current review. Installation stages the copy inside the
   store, compares it against the reviewed hash, swaps the destination behind a
   unique same-store backup and commits the lockfile last, so an ordinary copy,
   hash, rename or lockfile failure leaves the previous install and its
   lockfile entry intact. Hook, MCP server and MCP policy rows open a wrapped,
   pageable full-content view with terminal controls escaped and MCP arguments
   quoted individually, including an MCP server's environment, headers and
   trust. Tests use isolated local fixtures and injected
   per-instance dependencies for delayed scans, source and scope changes,
   undelivered results, tampered clones, changed lockfiles and per-call
   filesystem failures; the 80×24 review, source change, cancel-during-scan and
   update paths are covered by those tests rather than an interactive terminal
   session. Full suite: no new failures against the recorded baseline
   (pre-existing failures remain in `internal/snapshot`,
   `internal/tools/desktop/browser` and `internal/worktree`); the TUI packages,
   build, `go vet`, `gofmt` and the scripted usability suite are clean.

4. **Preserve drafts and protect secret entry.** Address
   [doctor key exposure and draft loss](audit.md#doctor-repair-exposes-api-keys-and-discards-the-composer-draft),
   [question draft loss](audit.md#answering-a-question-discards-an-unrelated-composer-draft),
   [approval-edit draft loss](audit.md#editing-an-approval-overwrites-the-composer-draft),
   and [large paste position](audit.md#large-pastes-ignore-the-composer-cursor).
   Preserve text, cursor and paste attachments across transient surfaces; use a
   masked secret field. Test completion and cancellation separately.

   **Status (2026-10-27): done** — tasks 1–4 of the
   [P3 drafts and secret entry plan](plans/2026-10-27-p3-drafts-and-secret-entry-plan.md)
   (commits `120c0a24`, `2a081201`, `02e137a5`, `43af57e7`). A settled question
   no longer resets the composer or its placeholder, and a replacement question
   gets a form of its own; choosing Edit for an approval now opens an isolated
   editor bound to that owner and request instead of overwriting the composer,
   and a stale edit is retired before it can settle a replacement; the doctor
   repair collects the key in a dedicated masked field routed ahead of the
   composer, completion and the condensing branch, so the key never reaches the
   frame, history or a paste attachment, and only `esc` reaches a field that is
   below the supported minimum or behind a panel. Condensed pastes insert as a
   complete chip at the cursor, and a refused insert leaves the draft and its
   attachments unchanged with a flash naming the limit that refused it. Tests
   drive the real `Model.Update` path at 80×24 and 100×40 with isolated fixtures
   and no real keys. Full suite: no new failures against the recorded baseline
   (pre-existing failures remain in `internal/app` (6),
   `internal/snapshot`, `internal/tools/desktop/browser` and
   `internal/worktree`); the TUI packages, build, `go vet`, `gofmt` and the
   scripted usability suite are clean.

5. **Make exit postmortem consent readable.** Fix
   [clipped or omitted disclosure](audit.md#consent-disclosure-can-be-clipped-or-omitted)
   independently of ordinary approval handling. Verify the disclosure and
   choices together at minimum dimensions and after resize.

   Resolved 2026-10-28 (commits `8c37a029`, `e2997d16`; tasks 1–2 of the
   [P4 postmortem consent plan](plans/2026-10-28-p4-postmortem-consent-plan.md)).
   The agent-analysis stage now renders its two mandatory disclosure lines with
   the question and both choices in one pass, so they are verified together at
   80×24, 120×40 and 200×60 and under `NO_COLOR` in the composed frame. A
   geometry too small for that shows a resize instruction instead and refuses
   the affirmative answer, and while the terminal is below its 80×24 minimum —
   where the parent replaces the whole frame — `y`, `Y` and Enter are consumed
   before the dock sees them. Consent is re-earned per geometry: entering the
   agent stage starts ungated, a resize or a replaced panel invalidates a
   rendering, and a render the disclosure no longer fits retracts it. `n`, `N`,
   Esc and Ctrl+C stay available throughout, including while an access request
   is queued behind the question. Tests drive the real `Model.Update` path and
   assert controller calls and report state, not command shape. Full suite: no
   new failures against the recorded baseline (pre-existing failures remain in
   `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser` and
   `internal/worktree`); the TUI packages, build, `go vet`, `gofmt` and the
   scripted usability suite are clean. Interactive manual exercise of the two
   questions and a resize below the minimum was not possible in this
   environment (no controlling terminal for a real TUI session); the
   composed-frame and resize tests render the real `Model` at those geometries
   and are the recorded substitute.

   Review follow-up (two `glm-5.3-flash` reviewers on the branch diff): the
   parent below-minimum gate was previously unproven — every existing assertion
   also held with the gate reverted, because the panel refuses on its own at
   those geometries. The hidden-frame subtest now renders the panel's disclosure
   directly, so only the parent gate can refuse the answer and removing it fails
   the test. The postmortem gate also moved ahead of the queued access-request
   gate, which had been swallowing `n`/`N`/Esc and made the question
   unanswerable in that stacked state. Added a fit-then-shrink retraction case,
   widened the too-small absence check to every mandatory row, and corrected
   wording that overstated the resize-instruction fallback and credited the
   stage transition with an invalidation it cannot observably cause (the flag is
   already false at every transition; the reset stays as a defensive invariant).

6. **Make generated-plan recovery safe.** Fix
   [review diagnostics overflow](audit.md#plan-review-cannot-reach-overflow-diagnostics)
   and [the edit hint that leads to discard](audit.md#the-plan-review-recovery-hint-conflicts-with-discard-behavior).
   Preserve a candidate when the user intends to edit it and report discard
   failures. Ensure blocked diagnostics are reachable.

   Resolved 2026-10-28 (commits `8ee41eef`, `2f7cff04`; tasks 1–2 of the
   [P5 plan-review and recovery plan](plans/2026-10-28-p5-plan-review-recovery-plan.md)).
   Escape now keeps the candidate rather than discarding it, and a transcript
   message names the retained path and the edit-then-`/sdd` next step, for
   clean, blocked and nil inspections alike; Enter still accepts only a clean
   candidate and never starts the pipeline. Discard takes two consecutive `d`
   presses, targets only the active review's candidate through the existing
   `sddauthor.RemoveCandidate` containment checks, and a failure keeps the panel
   open, preserves the candidate and shows the error instead of claiming
   removal. The panel-to-host messages carry their originating panel, so a
   delayed keep, accept or discard cannot act on a replacement review, and a
   hidden or resized frame disarms the confirmation (the parent also consumes
   `d`/`D` below 80×24). The body wraps diagnostics and long paths to physical
   cell width and scrolls with Up/Down, PgUp/PgDn and Home/End under an
   overflow-position row, so every diagnostic and its complete message is
   reachable at 80×24, 120×40 and 200×60 without losing the candidate or
   accidentally accepting it. Tests drive the real `Model.Update` path and the
   composed frame, covering clean, blocked and nil inspections, `NO_COLOR`,
   control characters, discard failure, sibling preservation, stale messages and
   hidden or resized geometry. Interactive manual exercise at 80×24 was not
   possible in this environment (no controlling terminal for a real TUI
   session); the real-`Model.Update` and composed-frame tests render the actual
   `Model` at those geometries and are the recorded substitute. Full suite: no
   new failures against the recorded baseline (pre-existing failures remain in
   `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser` and
   `internal/worktree`); the TUI packages, build, `go vet`, `gofmt` and the
   scripted usability suite are clean.

7. **Fix current-session skill identity and visibility.** Group
   [relocated global install path](audit.md#global-skill-installation-ignores-relocated-config-directories),
   [scope selection mismatch](audit.md#skill-scope-selection-does-not-determine-which-version-loads),
   and [stale runtime index](audit.md#skill-list-and-runtime-index-diverge-after-install-or-removal).
   Decide explicitly whether changes become usable immediately or require a
   restart; the UI and runtime must agree on the selected version.

   **Status (2026-10-28): done** — tasks 1–2 of the
   [P6 skill identity plan](plans/2026-10-28-p6-skill-identity-plan.md)
   (commits `015e594f`, `34549cb5`). Global installation now targets the
   effective `config.UserDir` skills directory and project installation is
   gated on folder trust. Load now is enabled only for an entry that still
   exists unchanged and whose parsed content exactly equals the current
   session's index value; a missing entry, a replaced or edited source, a
   differing or plugin-overridden value, an unavailable index, and an
   already-active name all stay inert with a stated reason, and the panel says
   "content matches the current session" rather than claiming a source
   identity. Disk changes do not mutate the session-owned runtime index: the
   panel and the install/remove results say a restart applies them. Skill rows
   carry a scope-plus-source identity, so duplicate names are distinct rows.
   Full suite: no new failures against the recorded baseline (22 pre-existing
   failures in `internal/app`, `internal/snapshot`,
   `internal/tools/desktop/browser` and `internal/worktree`); the TUI packages,
   `internal/skills`, build, `go vet`, `gofmt` and the scripted usability suite
   are clean.

## P2 — interaction and visibility

8. **Fix shared list sizing before panel-specific layout work.**
   [Physical line budgeting](audit.md#shared-field-lists-budget-logical-rows-instead-of-display-lines)
   affects several panels. Then address
   [preflight selection and diagnostics](audit.md#preflight-cast-list-can-hide-the-selected-role-or-blocking-error),
   [plan-run gate overflow](audit.md#plan-run-gate-can-hide-the-question-it-asks-the-user-to-answer),
   [inspector metadata](audit.md#inspector-silently-omits-metadata), and
   [session rules](audit.md#session-sheet-can-hide-standing-approval-rules).
   Verify selection, scroll reachability and long text at 80×24 and wider sizes.

   **Status (2026-10-28): done** — tasks 1–6 of the
   [P7 layout and overflow plan](plans/2026-10-28-p7-layout-overflow-plan.md)
   (commits `895bf532`, `fcd7251d`, `040d31a4`, `c77fbeb7`, `105f269f`,
   `b6951ad4`). Physical-line budgeting landed in the shared field-list engine
   first (`windowLines`, with shared `chrome.ClipLines` left untouched), then
   each panel got its own offset, wrapping and paging: the cast list follows
   the selected role and pages every wrapped diagnostic, the gate wraps the
   whole question and report above a pinned input row, the inspector gained a
   view-only metadata tab, and the sheet gained a read-only rules detail mode.
   Every panel keeps the frame inside its `maxHeight`; overflow indicators are
   textual (`↑ more` / `↓ more`, `N–M/T`, `⇞⇟ page`) so they survive without
   colour. Verification: `go build ./cmd/marshal` succeeds;
   `go test ./internal/app/tui/... -count=1`, `go vet ./...`, gofmt for the six
   touched packages and `git diff --check` are all clean; the scripted usability
   suite passes. The full suite shows no new failures against the recorded
   baseline — the same four packages (`internal/app`, `internal/snapshot`,
   `internal/tools/desktop/browser`, `internal/worktree`) and passes in every
   package this phase changed, including `internal/app/tui`. `web/bridge`
   timed out once at 301 s inside the 5-minute whole-suite budget and passed on
   re-run; `web/` is stdlib-only behind `TestWebIsStdlibOnly`, so this phase
   cannot reach it. Limitations: no controlling terminal, multiplexer or SSH
   environment was available, so resize, paging and key routing were exercised
   through the model and panel update paths (including composed frames at 80×24
   and 120×40) rather than by hand; the scripted usability suite remains
   help-only and does not cover these panels.

9. **Correct focused input routing.** Fix
   [plugin paste](audit.md#plugin-manager-ignores-bracketed-paste),
   [Escape in skill/plugin editors](audit.md#escape-leaves-skill-and-plugin-frames-while-editing-a-field),
   and [agent editor paste and question marks](audit.md#agent-editor-misroutes-paste-and-intercepts-question-marks).
   Include keyboard and bracketed-paste cases for each active editor and picker.

   **Status (2026-10-28): done** — tasks 1–4 of the
   [P8 focused input plan](plans/2026-10-28-p8-focused-input-plan.md)
   (commits `52da0209`, `8be3d5c1`, `b591a1da`, `66f4d9ab`). The skills and
   plugins panels now give an active field edit, enum picker or add prompt the
   first refusal on Escape, so a cancelled edit no longer leaves the frame (or,
   for plugins, abandons the scan its lease owns); the plugin panel gained the
   missing `tea.PasteMsg` branch, and paste in all three panels follows focus —
   full-content review first, picker overlay next, then the drilled frame or
   active edit, and only then the root filter. The agent roster forwards both
   `?` and Escape to a field that is being edited, sends paste to its picker
   overlay while one is open, and keeps the legend toggle for when nothing owns
   the key. Verification: `go build ./cmd/marshal` succeeds;
   `go test ./internal/app/tui/... -count=1`, `go vet ./...`, gofmt for the
   touched packages and `git diff --check` are all clean; the scripted usability
   suite passes against the built binary. New behaviour is evidenced by
   `TestEscapeCancelsSkillEditorBeforeLeavingFrame`,
   `TestSkillEscapePreservesRemovalConfirmationSafety`,
   `TestPluginPasteFollowsFocus`,
   `TestPluginEscapeCancelsEditBeforeScanAbandonment`,
   `TestAgentPasteFollowsFocusedControl`,
   `TestAgentQuestionMarkAndEscapeRespectEditor` and
   `TestManagementPanelPasteAndEscapeThroughModel`, which drives the real panels
   through `Model.Update` at 80×24 and 140×40 and asserts the composer draft
   underneath is untouched. The full suite shows no new failures against the
   recorded baseline — the same four packages (`internal/app`,
   `internal/snapshot`, `internal/tools/desktop/browser`, `internal/worktree`)
   and passes in every package this phase changed. Limitations: no controlling
   terminal, multiplexer or SSH environment was available, so keyboard and
   bracketed paste were exercised through the model and panel update paths
   rather than by hand; the scripted usability suite remains help-only and does
   not cover management-panel paste.

10. **Stabilize model selection and sign-in.** Address
    [selection moving during discovery](audit.md#model-discovery-can-move-the-selection-while-the-user-is-choosing),
    [late skipped probe results](audit.md#skipped-provider-probes-can-interrupt-later-wizard-steps),
    and [MCP authorization URL fallback](audit.md#mcp-sign-in-fallback-cannot-expose-a-long-authorization-url).
    Test out-of-order probe completions and manual sign-in without a browser.

    **Status (2026-10-29): done** — tasks 1–6 of the
    [P9 model discovery and sign-in plan](plans/2026-10-29-p9-model-discovery-sign-in-plan.md)
    (commits `db7f5e96`, `e139103a`, `fef23349`, `625c6058`, `aa8fffcf`,
    `c7c1ac5e`). Discovery can no longer move a choice: the picker replaces its
    rows in place and re-binds the selection by value, and requires an explicit
    new choice when the selected row disappears. Setup probes and `/models`
    refreshes are now correlated — the wizard by instance and attempt, the
    parent per provider — so skipped, retried, cancelled, replaced or superseded
    completions change neither wizard state nor the session and disk catalogs.
    The MCP authorization URL is pageable and copyable, with truthful OSC 52
    feedback when the local clipboard cannot confirm. Verification:
    `go build ./cmd/marshal` succeeds; `go test ./internal/app/tui/... -count=1`,
    `go test -race ./internal/app/tui/connect ./internal/app/tui/picker ./internal/app/tui/mcpauth`,
    `go vet ./...`, gofmt for the touched files and `git diff --check` are all
    clean; the scripted usability suite passes against the built binary. The
    full suite shows no new failures against the recorded baseline — the same
    four packages (`internal/app`, `internal/snapshot`,
    `internal/tools/desktop/browser`, `internal/worktree`) fail identically at
    the plan's base commit `bc564242`, and every package this phase changed
    passes. Limitations: no controlling terminal, multiplexer or SSH
    environment was available, so pagination, copy, resize and out-of-order
    completions were exercised through the model and panel update paths
    (composed frames at 80×24, 120×40 and 140×40) rather than by hand; a real
    OAuth round trip and a real terminal clipboard were not exercised, so the
    OSC 52 acceptance path stays unproven on device; the scripted usability
    suite remains help-only. A three-reviewer branch review (`glm-5.3-flash`)
    then found, and this branch fixed: the picker clipping its own error line at
    the smallest dock heights (the list and footer now yield rows to it), no
    paging cue when the authorization-URL viewport is a single row, a parent
    discovery result landing for a provider already removed from the config, and
    Enter replacing the specific missing-selection message with a vaguer one.
    Remaining review notes are accepted as-is: the auth panel's viewport clamp
    lives in `View` (documented, and every paging key clamps again), and the
    `models` probe field id is spelled in both packages because `connect` cannot
    import `tui`.

11. **Clarify and preserve question answers.** Address
    [skip discarding prior answers](audit.md#skipping-a-multi-question-form-discards-earlier-answers)
    and [the `other` sentinel collision](audit.md#a-legitimate-other-option-collides-with-the-custom-answer-sentinel).
    Confirm the intended skip contract before changing it and test both single
    and multiple choice.

12. **Make status hints match the active action.** Fix
    [conflicting or duplicated Ctrl+X guidance](audit.md#ctrlx-footer-guidance-can-conflict-or-repeat),
    including narrow terminal fallbacks and drilled-in subagents.

## P3 — cleanup and baseline

13. Remove the reviewed dead branches and dummy import together with related
    work: [browse Enter](audit.md#browse-enter-contains-an-unreachable-subagent-card-branch),
    [transcript click target](audit.md#transcript-click-target-retains-an-unreachable-subagent-branch),
    and [plan review dummy import](audit.md#plan-review-has-an-unused-import-retained-by-a-dummy-reference).
    Confirm the intended drill-in behavior before deleting branches.
14. Resolve the reproducible status-color test mismatch recorded under
    [verification observations](audit.md#verification-observations). The
    status uses `theme.Current().StatusWarning`, and the current default theme
    defines that slot as ANSI 215; the test expects the old hard-coded 172.
    Update the assertion to verify the semantic warning style across themes
    rather than one palette's literal value.

## Cross-cutting verification

Use isolated fixtures and focused regression tests for each fix; add real-binary
scenarios for approvals, prompts, and management flows rather than relying on
the existing help-only scenario. Finish with the TUI package suite and the full
Go suite. Manually check resize, keyboard and mouse, long content, monochrome,
light and dark themes, and at least one multiplexer/SSH setup. Record any
environments that could not be checked.
