# TUI polish audit

Working inventory for later triage. This is an ongoing code and UX audit, not
an exhaustive list or an implementation plan. The web control plane is out of
scope. Severity and solutions are provisional until triage.

The highest-risk item found so far is [skill removal reconstructing the wrong
filesystem path](#skill-removal-reconstructs-the-wrong-filesystem-path): a
confirmed removal can escape the skill store. Approval and plugin-review
disclosure findings also deserve early attention.

## Findings

### Interactive approvals omit the details being approved

The interactive approval model computes a summary containing the tool
arguments, reason, requester and other context, but its own `View` renders a
different compact layout and never displays that summary. Non-shell approvals
therefore omit arguments and requester context. When Risk is nonempty, they
also omit Reason, including the reason on model-cost consent; when Risk is
empty, Reason is used as a fallback and can still be clipped. The fallback
renderer contains more detail, but is replaced during normal handling.
Users can confirm without seeing the full operation or the reason for consent.

- Evidence: `internal/app/tui/approval.go:205`, `:331` and `:412`;
  `internal/app/tui/console.go:216` and `internal/app/tui/model.go:3206`.
- Potential direction: show the complete relevant details in the interactive
  prompt and keep them accessible before confirmation.

### Long shell commands are clipped in the interactive approval prompt

The interactive approval view writes the shell command as a single line. The
console truncates each line to the frame width, with no way in this prompt to
view the remainder. A user can approve a command whose trailing arguments or
additional shell operations are not visible.

- Evidence: `internal/app/tui/approval.go:356` and
  `internal/app/tui/console.go:52`.
- Potential direction: wrap or provide a scrollable full command display,
  ensuring the complete command remains available before confirmation.

### Oversized inline prompts can lose their first rows

Inline prompts can render more rows than the terminal has available. The final
frame drops surplus rows from the top, but transcript scrolling does not scroll
the prompt itself. In particular, a long sudo command or reason can push the
start of the command off screen while leaving the password field and Enter
action available. Long approval diffs and multi-question forms need the same
overflow review.

- Evidence: `internal/app/tui/sudoauth.go:72`,
  `internal/app/tui/model.go:3531`, and `internal/app/tui/view.go:121` and
  `:169`.
- Potential direction: constrain and scroll prompt content, keep decision
  controls reachable, and make the complete command inspectable before
  authentication.

### Escape does not disarm a pending approval

The first Enter arms the selected approval action, initially Allow. Pressing
Escape once starts a two-press deny gesture but leaves the earlier action
armed. If the user then presses Enter, the Escape warning is cleared and the
armed action is submitted immediately. The prompt header says “Esc deny”
without explaining that it takes two presses.

- Evidence: `internal/app/tui/approval.go:252` and
  `internal/app/tui/help/help.go:90`.
- Potential direction: disarm any pending action when Escape is pressed and
  make confirmation and denial gestures explicit in the displayed hints.

### Consent disclosure can be clipped or omitted

The exit postmortem's agent-analysis prompt puts its explanation on one long
line. The panel frame truncates that line at ordinary terminal widths, and the
panel drops the explanation entirely when its height budget is tight. The
user can still select the default Yes without seeing that the agent may edit
a file at an absolute path outside the workspace.

- Evidence: `internal/app/tui/postmortempanel/postmortempanel.go:129` and
  `internal/app/tui/chrome/frame.go:85`.
- Potential direction: wrap the disclosure and ensure its essential content is
  visible before the choice can be confirmed, including at small sizes.

### Doctor repair exposes API keys and discards the composer draft

Selecting the doctor fix closes the panel, resets the normal chat input and
uses it to enter the API key. That input renders unmasked, exposing the key to
anyone viewing the terminal. An existing draft is also discarded.

- Evidence: `internal/app/tui/model.go:2374`,
  `internal/app/tui/console.go:139`, and `internal/app/tui/keypress.go:26`.
- Potential direction: use a dedicated masked secret entry and preserve the
  draft across the flow.

### Inspector silently omits metadata

The inspector limits fields to half the panel height and silently stops
rendering them. Scrolling applies to section bodies, not the fields. Long
early values can hide later information such as approval, changed arguments,
call ID and risk, without indicating that anything is missing.

- Evidence: `internal/app/tui/inspector/inspector.go:172` and
  `internal/app/tui/browse_inspect.go:145`.
- Potential direction: make all metadata reachable and indicate overflow.

### Ctrl+X footer guidance can conflict or repeat

The busy footer always advertises Ctrl+X to clear the queue and can also
advertise it to stop a drilled-in agent. The handler prioritizes stopping the
agent, so the queue hint is misleading in that state. A nonempty queue can
also cause the clear-queue hint to be repeated.

- Evidence: `internal/app/tui/help/help.go:149` and
  `internal/app/tui/keypress.go:370`.
- Potential direction: generate hints from the same action precedence as the
  handler and avoid duplicates.

### Large pastes ignore the composer cursor

Pastes of eight or more lines or over 1,500 bytes are condensed into a chip.
The chip is always appended to the end of the input and the cursor is moved
there, even when the user pasted in the middle of a draft. Small pastes use
the textarea's ordinary insertion behavior. This can change the intended order
of a prompt without an obvious indication of what happened.

- Evidence: `internal/app/tui/model.go:2817` and
  `internal/app/tui/paste.go:25` and `:39`.
- Potential direction: insert the chip at the current cursor position and
  test it in multiline drafts.

### Session sheet can hide standing approval rules

The Ctrl+B sheet gives the selected section only the rows left after the
other sections, often just one row at a short terminal height. The rules
section truncates its list to that budget; the sheet has no rule scrolling or
full-detail command for that section. Later standing approvals therefore
cannot be inspected through the sheet, and there is no overflow indication.

- Evidence: `internal/app/tui/sessionsheet/panel.go:41`, `:97` and `:142`;
  `internal/app/tui/sessionsheet/section_rules.go:20`.
- Potential direction: provide a scrollable or full-detail rules view and
  show when the summary is truncated. Make the Enter hint conditional on an
  actual action for the selected section.

### Model discovery can move the selection while the user is choosing

In the all-providers model picker, each asynchronous probe result or error
rebuilds the picker. The rebuild restores the filter text but not the selected
item; the new picker starts at its default selection. A result arriving just
before Enter can therefore cause the user to choose a different model.

- Evidence: `internal/app/tui/connect/connect.go:966` and `:1006`;
  `internal/app/tui/picker/picker.go:61`.
- Potential direction: preserve selection by stable item value across refreshes
  and define a visible fallback if the selected item disappears.

### Skipped provider probes can interrupt later wizard steps

Skipping a provider probe advances to model selection without canceling the
request. The result handler does not check whether the wizard is still waiting
for that probe. A late success can recreate the model picker or move the user
back from a subsequent step; a late failure can leave a stale error.

- Evidence: `internal/app/tui/connect/connect.go:966`, `:1218` and `:1237`.
- Potential direction: cancel or identify probe requests and ignore results
  that no longer belong to the active step.

### Answering a question discards an unrelated composer draft

The question form temporarily hides the normal composer and owns its own
fields, but completion or cancellation resets the composer and its condensed
paste attachments. A draft being prepared while an agent is running is lost
when its question is answered or skipped.

- Evidence: `internal/app/tui/console.go:196`,
  `internal/app/tui/model.go:3346` and `:3373`, and
  `internal/app/tui/paste.go:71`.
- Potential direction: preserve and restore the composer state across the
  question rather than clearing it.

### Editing an approval overwrites the composer draft

Choosing Edit for a pending approval puts the command or arguments into the
main composer with `SetValue`, replacing any draft being prepared during the
turn. Both saving and canceling the edit then reset that input, including its
paste attachments.

- Evidence: `internal/app/tui/model.go:3148` and `:3280`.
- Potential direction: use a separate edit field or save and restore the
  complete composer state when the edit ends.

### Skipping a multi-question form discards earlier answers

The footer labels Escape as “skip”, and the question model describes Escape as
marking the remaining questions unanswered. In fact, answers are finalized
only when the whole form completes; Escape returns the initially unanswered
array, including for questions the user already answered.

- Evidence: `internal/app/tui/help/help.go:86`,
  `internal/app/tui/question.go:29`, `:189` and `:328`, and
  `internal/app/tui/model.go:3361`.
- Potential direction: retain completed answers on skip, or clearly describe
  and confirm an action that discards the entire form.

### A legitimate “other” option collides with the custom-answer sentinel

Question options use their labels as values, while the appended custom-answer
choice uses the literal value `other`. An option labelled exactly `other` is
therefore interpreted as the custom choice rather than as the chosen answer.
This affects both single- and multiple-choice questions.

- Evidence: `internal/app/tui/question.go:15`, `:167`, and `:215`;
  `internal/tools/native/question.go:215`.
- Potential direction: give the custom choice an identity that cannot collide
  with a supplied option value.

### Plan review cannot reach overflow diagnostics

The authored-plan review clips its diagnostics to the dock height with a fixed
focus at the first row. Its keyboard handler has no scrolling, so diagnostics
past the visible rows cannot be read there, even when one of them blocks
acceptance. Long diagnostic lines are also truncated by the panel frame.

- Evidence: `internal/app/tui/sddreview/review.go:52` and `:69`.
- Potential direction: make diagnostics scrollable and wrap long messages.

### The plan-review recovery hint conflicts with discard behavior

When a generated candidate has errors, the review panel says to edit the plan
and run `/sdd`, but Escape is its only exit and discards the candidate. The
host ignores errors from removing it, so even the outcome of discard can be
unclear. A user who exits the panel to follow the edit instruction may find the
file gone.

- Evidence: `internal/app/tui/sddreview/review.go:52` and `:108`,
  `internal/app/tui/model.go:2687`.
- Potential direction: provide a keep-and-edit exit distinct from discard,
  and report removal failures.

### Preflight cast list can hide the selected role or blocking error

The preflight panel clips from the first row regardless of the selected role,
and its navigation changes the role without scrolling the list. At restricted
heights, moving to a later role can leave the selection off screen; blocking
errors or warnings below the visible region can also be unreachable even
though they prevent the run or affect the user's decision.

- Evidence: `internal/app/tui/castlist/castlist.go:319` and `:374`.
- Potential direction: scroll to the selected row and provide access to all
  diagnostic rows, including wrapped messages.

### Plan-run gate can hide the question it asks the user to answer

The gate panel places the question and report above the answer field, then
clips around the final input row. For long questions or reports, the start of
the question can be pushed out of view. The panel offers no scroll keys for
the hidden rows, but Enter still sends the answer and Escape stops the run.

- Evidence: `internal/app/tui/gatepanel/gatepanel.go:57` and `:80`.
- Potential direction: keep the answer input reachable while allowing the
  full question and relevant report to be reviewed.

### MCP sign-in fallback cannot expose a long authorization URL

The MCP authorization panel presents the URL as one line inside a frame that
truncates long lines. It has no copy or scrolling action, despite relying on
manual access to the displayed URL when opening a browser fails. Typical
authorization URLs can exceed the panel width, leaving the fallback unusable.

- Evidence: `internal/app/tui/mcpauth/panel.go:105`, `:121` and `:148`;
  `internal/app/tui/chrome/frame.go:85`.
- Potential direction: provide a keyboard action that copies the complete URL,
  as the provider sign-in flow does, and show whether copying succeeded.

### Plugin install can confirm a scan of a different source

After a plugin source is scanned, the Source field remains editable, but
changing it does not invalidate the scan or its Confirm action. Confirm uses
the previously scanned source and directory, even though the field now shows
the new value. This can install different executable plugin content from what
the user believes they selected.

- Evidence: `internal/app/tui/plugins/panel.go:257`, `:292` and `:434`.
- Potential direction: invalidate the scan whenever the source changes and
  bind the confirmation visibly to the exact scanned source.

### Plugin review clips executable commands without a detail view

The scan review shows hook and MCP commands as read-only scalar rows. The
shared row renderer truncates long values, and these read-only rows cannot be
opened to inspect the rest. Confirm remains available even when a command's
suffix is hidden.

- Evidence: `internal/app/tui/plugins/panel.go:309`,
  `internal/app/tui/listpanel/fieldlist.go:545` and `:621`, and
  `internal/app/tui/chrome/row.go:92`.
- Potential direction: offer a wrapped or scrollable full-content view for
  every executable entry before installation.

### Shared field lists budget logical rows instead of display lines

`chrome.Row` can return a two-line stacked row, but `FieldList.View` adds it
as one element and uses element counts to window around the cursor. When
several values stack, the rendered output can exceed its height budget and a
parent frame can clip selected rows, errors, or controls at the bottom. This
shared engine is used by settings and several management panels.

- Evidence: `internal/app/tui/chrome/row.go:31`,
  `internal/app/tui/listpanel/fieldlist.go:589` and `:621`, and
  `internal/app/tui/chrome/chrome.go:41`.
- Potential direction: window physical rendered lines while preserving the
  selected field's visibility; verify with stacked rows at narrow widths.

### Plugin manager ignores bracketed paste

The plugin panel handles keypresses and asynchronous results but has no
`tea.PasteMsg` branch. Pasting a URL into the install Source field or a query
into the filter therefore does nothing in terminals that send bracketed
paste. The skills panel explicitly forwards paste to the relevant control.

- Evidence: `internal/app/tui/plugins/panel.go:553` and
  `internal/app/tui/skills/panel.go:504`.
- Potential direction: route paste to the active field or filter, and cover
  both paths through the panel's real update handler.

### Escape leaves skill and plugin frames while editing a field

Both panels intercept Escape before the shared field editor sees it. When a
user is editing a Source field and presses Escape to cancel the edit, the
panel instead pops the entire install frame; the plugin panel also abandons
its scan. This differs from the shared editor's normal Escape behavior.

- Evidence: `internal/app/tui/skills/panel.go:477`,
  `internal/app/tui/plugins/panel.go:553`, and
  `internal/app/tui/listpanel/fieldlist.go:408`.
- Potential direction: give an active edit first refusal on Escape, then pop
  the frame on a subsequent Escape.

### Agent editor misroutes paste and intercepts question marks

The agent roster sends every paste to its root filter, even when a picker or
an editable field in a drilled-in frame owns the keyboard. It also intercepts
`?` for the legend before forwarding input to a field editor. Pasting an
agent name or system prompt can change the hidden filter instead, and a
question mark cannot be typed into a scalar editor.

- Evidence: `internal/app/tui/agents/panel.go:612`, `:626` and `:661`.
- Potential direction: route text and paste to the focused editor or picker
  before applying roster shortcuts.

### Skill and plugin removal confirmations remain armed after navigation

The first Remove activation sets an armed flag keyed only by name. Neither
panel disarms it when navigating away or leaving the detail frame. Returning
later can remove the item with one Enter, and same-name entries in different
scopes share the flag.

- Evidence: `internal/app/tui/skills/panel.go:202` and `:477`;
  `internal/app/tui/plugins/panel.go:166` and `:553`;
  `internal/app/tui/listpanel/fieldlist.go:167`.
- Potential direction: disarm on navigation and exit, and identify an item by
  both scope and name.
- Status (2026-10-27): fixed. Both panels key the armed flag on a private
  identity — scope plus the discovered source for skills, scope plus the
  lockfile name for plugins — register a `settings.SetFieldDisarm` callback on
  the Remove row, and call `DisarmCurrent` before Escape pops or closes the
  frame. The list engine's arrow keys already invoked a row's disarm, so
  leaving the row now clears it. Covered by
  `TestRemoveConfirmationDisarmsOnNavigationAndEscape` and
  `TestRemoveConfirmationIsScopedBySource` / `TestRemoveConfirmationIsScoped`.

### Leaving an in-flight plugin scan loses ownership of its result

A scan runs with a background context and creates a temporary clone. Leaving
the frame before the result arrives cannot discard a clone whose path has not
yet been returned. If the panel closes, the dock no longer routes the result
to it, leaving the clone behind; if the panel remains open, a late result can
attach to a different install flow.

- Evidence: `internal/app/tui/plugins/panel.go:356`, `:424`, `:553` and
  `:587`; `internal/app/tui/dock/dock.go:53`.
- Potential direction: retain or cancel ownership of each scan until cleanup,
  and correlate results with the active install operation.

### Leaving a plugin install can remove the clone during its copy

The install command copies the scanned clone asynchronously after removing the
existing destination. Navigating out far enough with Escape can concurrently
call `discardScan` on that same clone. (For an update opened from a detail
frame, the first Escape pops to that frame; a subsequent Escape cleans the
scan.) The copy can then fail or leave a partial destination after the prior
installation was already removed.

- Evidence: `internal/app/tui/plugins/panel.go:424`, `:434` and `:553`.
- Potential direction: transfer clone ownership to the installer and replace
  the destination atomically only after a successful staged copy.

### Skill removal reconstructs the wrong filesystem path

Skill discovery accepts both top-level `.md` files and bundles whose directory
name need not equal the skill's frontmatter name. The TUI removes a path built
from the skill name without retaining the discovered source path. Removing a
single-file skill can report success while leaving it in place; a mismatched
bundle name can target another path.
The parsed frontmatter name is only checked for being nonempty, so a name
containing `..` can make the confirmed removal target escape the skill store
and recursively delete an unrelated path.

- Evidence: `internal/skills/loader.go:17` and `:46`,
  `internal/skills/skill.go:141`;
  `internal/app/tui/skills/panel.go:290`.
- Potential direction: retain the source identity and remove exactly the
  selected file or bundle, with the scope boundary validated.
- Status (2026-10-27): fixed. `skills.ScopedSkill` now carries the direct
  child of its store (`SourceName`) and its `Lstat` snapshot at discovery, and
  `skills.RemoveScopedSkill` validates that name (`filepath.IsLocal`, no
  separator, no nil snapshot, known scope), opens the store with `os.OpenRoot`,
  rejects symlinks, unsupported entry types, a missing child, and a child that
  is no longer `os.SameFile` as the snapshot, then calls
  `Root.RemoveAll(SourceName)`. The skills panel removes through that helper
  with the root chosen from the scope. The identity comparison detects a
  replacement before removal but is not an atomic lock; `os.Root` is the
  containment boundary if an entry changes mid-removal. Covered by
  `TestRemoveScopedSkill*`, `TestListScopesRecordsRemovalSource`, and
  `TestRemoveUsesDiscoveredSource` / `TestRemoveReportsStaleOrSymlinkSource`.

### Global skill installation ignores relocated config directories

The skills panel lists global skills under `config.UserDir`, which honors the
Marshal and XDG config overrides. Its install action instead uses `ScopeDir`,
which hardcodes `~/.config/marshal/skills`. With a relocated config directory,
an install can report success but not appear in the panel or runtime.

- Evidence: `internal/app/tui/skills/panel.go:93` and `:268`;
  `internal/skills/install.go:16`.
- Potential direction: resolve install and discovery through the same effective
  global directory.

### Skill scope selection does not determine which version loads

The panel lists global and project skills separately, but Load now looks up
only the name in the runtime's merged index. A project skill overrides a
same-named global skill in that index, so choosing Load now on the global row
can load the project version without saying so.

- Evidence: `internal/app/tui/skills/panel.go:153`, `:186` and `:303`;
  `internal/skills/loader.go:46` and `:77`.
- Potential direction: make the effective version and precedence explicit or
  load the selected scoped identity.

### Skill list and runtime index diverge after install or removal

The list rescans disk, but Load now uses the runtime index captured when the
panel was created. Installation and removal refresh the list without updating
that index. A newly installed skill may be displayed but fail to load in the
current session, and removed or changed content can remain in the index.

- Evidence: `internal/app/tui/skills/panel.go:153`, `:186`, `:303` and
  `:449`; `internal/app/runtime.go:1761`.
- Potential direction: reconcile the runtime index after changes, or make a
  restart requirement explicit and prevent misleading current-session actions.

## Removal candidates

### Browse Enter contains an unreachable subagent-card branch

Browse-mode Enter calls `subagentAtCursor(true)` and would drill into a child
if it succeeded. That function explicitly returns false for `onlyCard`, because
the in-place card it referred to no longer exists. Its other caller passes
false and remains useful for the `f` action.

- Evidence: `internal/app/tui/browse.go:258`, `:283` and `:381`.
- Potential direction: remove the unreachable Enter branch and simplify the
  now-unnecessary parameter after checking intended keyboard discoverability.

### Plan review has an unused import retained by a dummy reference

`sddreview` imports `strings` solely for a package-level `var _ =
strings.TrimSpace`; neither is used by the panel. Both can be removed together
if no concurrent work intends to use them.

- Evidence: `internal/app/tui/sddreview/review.go:9` and `:130`.

### Transcript click target retains an unreachable subagent branch

The click target still carries a subagent pointer and the click handler has a
drill-in branch for it, but no region populates that field after the in-place
cards were removed. The source explicitly identifies this as unreachable.

- Evidence: `internal/app/tui/click.go:9` and `:155`.
- Potential direction: remove the unused field and branch if future card
  restoration is not planned; keep current transcript and tray drill paths.

## Verification observations

- `go test ./internal/app/tui/... -timeout 2m` had one failure:
  `TestStatusLineUntrustedIsWarningColored` expects ANSI 172 or 33, while the
  rendered warning uses ANSI 215. The test also fails in isolation. Determine
  whether the expected palette or the assertion is stale before changing it.
- Other TUI packages in that run passed. No manual terminal, screen reader,
  multiplexer, or full-suite verification has yet been performed for this audit.
- Built a fresh binary at `/private/tmp/marshal-tui-audit` and ran the scripted
  usability suite. Its help-open-and-close scenario passed; it does not cover
  the other TUI workflows recorded here.
- The later prompt and navigation findings are based on code-path inspection;
  reproduce them interactively and add focused regression coverage during triage.
- The browse branch above is a candidate for removal; no change has been made.

## Coverage and remaining verification

This inventory reflects code-path review of the main composer and prompt
routing; approvals, sudo, access, questions and postmortem; transcript browse,
inspector and copy; tray and mouse routing; provider discovery and sign-in;
settings, model options, agents, skills and plugins; session sheet; plan
preflight, plan review and run gates; and shared panel, list and frame layout.
Existing tests for resize, themes, monochrome rendering and keyboard paths were
also inspected, and the TUI package suite was run as noted above.

This is not a proof that every path is defect-free. The remaining verification
is primarily behavioral: reproduce the findings at 80×24 and larger sizes,
exercise live resize and long content, and check keyboard and mouse interaction
in real terminals, over SSH and in a multiplexer. Check light and dark themes,
16-color and `NO_COLOR` modes, and assistive technology manually. Expand the
real-binary scripted scenarios beyond opening help. Recheck async and failure
paths with controlled fixtures during triage, and examine other removal
candidates before deleting code.
