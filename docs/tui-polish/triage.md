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

3. **Bind plugin confirmation to the reviewed content and protect installation.**
   Group [changed source after scan](audit.md#plugin-install-can-confirm-a-scan-of-a-different-source),
   [clipped executable commands](audit.md#plugin-review-clips-executable-commands-without-a-detail-view),
   [orphaned scan results](audit.md#leaving-an-in-flight-plugin-scan-loses-ownership-of-its-result),
   and [scan cleanup racing installation](audit.md#leaving-a-plugin-install-can-remove-the-clone-during-its-copy).
   Correlate scan results with their flow, make reviewed content accessible,
   and stage replacements so failure does not destroy an existing install.
   Verify with controlled delayed scan/copy and failure fixtures.

4. **Preserve drafts and protect secret entry.** Address
   [doctor key exposure and draft loss](audit.md#doctor-repair-exposes-api-keys-and-discards-the-composer-draft),
   [question draft loss](audit.md#answering-a-question-discards-an-unrelated-composer-draft),
   [approval-edit draft loss](audit.md#editing-an-approval-overwrites-the-composer-draft),
   and [large paste position](audit.md#large-pastes-ignore-the-composer-cursor).
   Preserve text, cursor and paste attachments across transient surfaces; use a
   masked secret field. Test completion and cancellation separately.

5. **Make exit postmortem consent readable.** Fix
   [clipped or omitted disclosure](audit.md#consent-disclosure-can-be-clipped-or-omitted)
   independently of ordinary approval handling. Verify the disclosure and
   choices together at minimum dimensions and after resize.

6. **Make generated-plan recovery safe.** Fix
   [review diagnostics overflow](audit.md#plan-review-cannot-reach-overflow-diagnostics)
   and [the edit hint that leads to discard](audit.md#the-plan-review-recovery-hint-conflicts-with-discard-behavior).
   Preserve a candidate when the user intends to edit it and report discard
   failures. Ensure blocked diagnostics are reachable.

7. **Fix current-session skill identity and visibility.** Group
   [relocated global install path](audit.md#global-skill-installation-ignores-relocated-config-directories),
   [scope selection mismatch](audit.md#skill-scope-selection-does-not-determine-which-version-loads),
   and [stale runtime index](audit.md#skill-list-and-runtime-index-diverge-after-install-or-removal).
   Decide explicitly whether changes become usable immediately or require a
   restart; the UI and runtime must agree on the selected version.

## P2 — interaction and visibility

8. **Fix shared list sizing before panel-specific layout work.**
   [Physical line budgeting](audit.md#shared-field-lists-budget-logical-rows-instead-of-display-lines)
   affects several panels. Then address
   [preflight selection and diagnostics](audit.md#preflight-cast-list-can-hide-the-selected-role-or-blocking-error),
   [plan-run gate overflow](audit.md#plan-run-gate-can-hide-the-question-it-asks-the-user-to-answer),
   [inspector metadata](audit.md#inspector-silently-omits-metadata), and
   [session rules](audit.md#session-sheet-can-hide-standing-approval-rules).
   Verify selection, scroll reachability and long text at 80×24 and wider sizes.

9. **Correct focused input routing.** Fix
   [plugin paste](audit.md#plugin-manager-ignores-bracketed-paste),
   [Escape in skill/plugin editors](audit.md#escape-leaves-skill-and-plugin-frames-while-editing-a-field),
   and [agent editor paste and question marks](audit.md#agent-editor-misroutes-paste-and-intercepts-question-marks).
   Include keyboard and bracketed-paste cases for each active editor and picker.

10. **Stabilize model selection and sign-in.** Address
    [selection moving during discovery](audit.md#model-discovery-can-move-the-selection-while-the-user-is-choosing),
    [late skipped probe results](audit.md#skipped-provider-probes-can-interrupt-later-wizard-steps),
    and [MCP authorization URL fallback](audit.md#mcp-sign-in-fallback-cannot-expose-a-long-authorization-url).
    Test out-of-order probe completions and manual sign-in without a browser.

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
