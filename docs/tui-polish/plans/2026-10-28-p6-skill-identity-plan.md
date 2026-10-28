# P6 skill identity and visibility — implementation plan

Date: 2026-10-28. Status: proposed; not executed.
Base: `7b30f12582b3f92b67daca1b5485a94bb82034b1`.
Requirements: [triage item 7](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss)
and the audit findings on [global installation](../audit.md#global-skill-installation-ignores-relocated-config-directories),
[scope selection](../audit.md#skill-scope-selection-does-not-determine-which-version-loads),
and [the runtime index](../audit.md#skill-list-and-runtime-index-diverge-after-install-or-removal).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** global installation uses the same directory the panel reads, and
the panel never claims to load a selected skill when the current session would
load different or stale content.

**Non-goals:** hot-reload the runtime index, modify `skill.load` or skill
precedence, alter already-active skill bodies, change trust policy or plugin
loading, redesign installation/removal, or address other triage items. This is
the sixth implementation plan in sequence and addresses the final open **P1**
item.

**Decisions and constraints:**

- Choose the restart-based option allowed by triage. The runtime index is a
  session-owned shared index that also receives verified plugin skills
  (`internal/app/plugins.go`); replacing it with a directory-only index would
  lose those entries and could destabilize a running turn's prompt prefix.
  Installing, removing or editing a skill on disk does not mutate this index
  or rewrite already-loaded skill bodies. The panel explicitly says to restart
  Marshal for disk changes to affect the runtime; active skills remain active
  until unloaded or the session ends. A removed skill can also still be loaded
  by the agent's `skill.load` tool from the old index until restart.
- The UI lists each actual discovered global/project entry. The same name can
  appear in both scopes or multiple sources in one scope; identify rows by
  scope plus discovered `SourceName`, never name alone. Existing P0 removal
  provenance and confirmation protections remain in place.
- `Load now` is enabled only when the selected entry still exists unchanged,
  its entire parsed `skills.Skill` exactly matches the current shared index's
  value for that name, and the name is not already active in the session.
  The index has no source provenance, so the UI says **content matches the
  current session**, not that a particular scope is the runtime's origin.
  A matching body from another source is semantically the same selected value.
  Different content, an absent index entry, an unavailable runtime index, a
  replaced/missing source, or an already-active name makes the action inert
  with an explanation. For an already-active name, say that the active body
  is retained and must be unloaded before loading another version; do not
  imply that the current loader refreshes its content.
- For an unselectable entry, explain that the current session may have an
  older version or a different source may take precedence, and restart may be
  needed. Do not promise that restart selects a shadowed global entry: project
  and plugin precedence can still choose another value. Never add the selected
  entry to the live index merely to bypass precedence.
- On the actual load action, repeat the source and index checks; build a
  temporary one-entry index from the exact checked runtime value and call the
  existing `skills.LoadSkillIntoSession`. This prevents a concurrent update to
  the shared index between validation and lookup from switching the body being
  loaded. Keep the existing loader, session policy and transcript behavior;
  no new tool or runtime policy belongs in the TUI.
- Global installs target `p.globalSkillsDir()` and project installs target
  `p.projectSkillsDir()`. Preserve `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME`
  precedence via `config.UserDir`; reject unknown scopes and project install in
  an untrusted project at the action boundary. Do not change `skills.ScopeDir`
  for other callers in this TUI-focused work.
- Use isolated fixture homes and local skill sources; no external services,
  credentials, live config or package-global factories. Preserve unrelated
  untracked work and stage named paths only.

Tasks run sequentially and may be committed after their verification as
`p6-skill-identity: task N — <title>`. Both touch the skills panel. The user
reviews this plan before execution.

## Contracts

Task 2 adds one private helper in `internal/app/tui/skills/panel.go`:

```go
// loadableSelection returns the exact current-session value to load, or a
// user-facing reason why this selected on-disk entry cannot be loaded now.
func (p *Panel) loadableSelection(s skills.ScopedSkill) (skills.Skill, string)
```

It re-runs `skills.ListScopes` with the panel's effective roots, matches scope
and source name and verifies the discovered source has the same identity and
parsed content as the selected detail. It compares that content with the
runtime index and checks the session's active-skill state. It returns a nonempty
reason on any failure; a directory read error is not an empty valid index.
No exported cross-package contract is added.

## Task 1: Install to the effective scope directory

**Goal:** installing a global skill writes into the same config directory the
panel and runtime use; project installs remain rooted in the project.

**Files:** `internal/app/tui/skills/panel.go`,
`internal/app/tui/skills/panel_test.go`.

**Steps:**

1. In `runInstall`, replace the `skills.ScopeDir` call with an explicit scope
   switch: `global` uses `p.globalSkillsDir()`, `project` uses
   `p.projectSkillsDir()` only if `p.projectTrusted`, and all other cases
   return a visible error without starting a command. Preserve `busy` handling
   and pass the chosen target to `skills.Install`. On successful installation
   or removal, keep the disk list refresh and show a persistent status/message
   stating that restarting Marshal applies disk changes to the runtime index;
   do not report an install or removal as applied to the current session.
2. Add `TestInstallUsesEffectiveGlobalSkillsDir`: with an isolated home and
   local source, run the real panel action and result path once with
   `MARSHAL_CONFIG_DIR`, once with `XDG_CONFIG_HOME`, and once with neither.
   Assert the installed file/bundle is discovered from the selected effective
   directory and not written to the default home in the overridden cases.
3. Add `TestInstallProjectScopeAndTrust`: a trusted project installs below
   its own `.marshal/skills`; untrusted or unknown scope emits a visible
   failure and leaves both stores unchanged. Add
   `TestSkillDiskChangeReportsRestart` for successful install and P0-protected
   remove, and assert the runtime index object and its pre-existing contents
   are not changed.

**Verify:**

```bash
go test ./internal/app/tui/skills -run 'Test(InstallUsesEffectiveGlobalSkillsDir|InstallProjectScopeAndTrust|SkillDiskChangeReportsRestart)' -count=1
go test ./internal/app/tui/skills -count=1
gofmt -l internal/app/tui/skills
```

Expected: tests pass with the package's existing `testenv.Run` isolation and
formatting prints no paths. Task 1 fixes the directory and discloses restart;
Task 2 prevents a misleading `Load now` selection.

## Task 2: Bind Load now to matching session content

**Goal:** the selected on-disk skill is never silently replaced by a
different runtime or already-active skill when loading.

**Files:** `internal/app/tui/skills/panel.go`,
`internal/app/tui/skills/panel_test.go`.

**Steps:**

1. Implement `loadableSelection` from Contracts. For the matching scoped
   source, check `os.SameFile` on non-nil `SourceInfo`, compare the full
   parsed `Skill` value with the detail's selected value and with
   `p.skillIndex.Load(name)`, and reject if `p.state` or `p.skillIndex` is
   nil. Reject an already-active name via `state.HasActiveSkill` with a
   specific explanation. Return an error reason when `ListScopes` fails; do
   not silently fall back to a freshly constructed index. Revalidate at action
   invocation, not only when drawing the row.
2. In `detailFrame`, retain the Load now action but derive its label and
   description from `loadableSelection`. When unavailable, pressing Enter
   performs no load and displays the reason on the active list. When available,
   create a fresh `skills.NewIndex()`, call `Set(skill.Name, skill)` with the
   returned exact skill, and call
   `skills.LoadSkillIntoSession` with that index. Do not call `Set` on the
   session's shared index. Preserve its existing successful load message.
   Make the status message clear that it is a current-session content match,
   not a verified source identity.
3. In `buildFields`, replace the name-only field ID with a stable identity
   incorporating scope and source name using an unambiguous encoding (for
   example, length-prefix each component). Keep display names and existing
   `autoload` preference semantics; revise tests that address rows by their old
   ID without changing saved config keys. Add a persistent explanation in the
   panel that on-disk changes need restart and may be shadowed by precedence.
4. Add `TestLoadNowUsesMatchingSessionContent`: a matching inactive entry is
   loaded with exactly its selected body, while an absent runtime entry,
   different global/project contents, plugin-overridden value, and already
   active name each remain unloaded and show an actionable reason. Include
   identical content in two scopes and assert the text describes content
   equality without claiming provenance.
5. Add `TestLoadNowRevalidatesSelectedSource` for deletion, replacement and
   same-path content changes after opening a detail, and for a changed shared
   index after the row is rendered; no stale selection is loaded. Add
   `TestDuplicateSkillRowsHaveDistinctIdentity` for same names in both scopes
   and same scope with different filenames. Exercise the real
   `Panel.Update`/command path and assert both rows navigate to their own source.
6. Add `TestRestartOnlySkillChangesDoNotMutateRuntimeIndex`: install, update
   and remove local fixtures while holding a runtime index with a plugin-only
   skill and a session-active skill. Assert the index's contents are unchanged,
   the panel communicates restart, and reopening the panel does not make a
   newly installed or mismatched skill loadable. Then construct a new index via
   the production `skills.LoadSkills` path and a new panel, and assert the
   effective matching skill can be loaded in the new session. Keep the existing
   P0 removal tests and autoload tests intact.

**Verify:**

```bash
go test ./internal/app/tui/skills -run 'Test(LoadNow|DuplicateSkillRows|RestartOnlySkillChanges|RemoveConfirmation|RemoveUses)' -count=1
go test ./internal/app/tui/skills ./internal/skills -count=1
go test -race ./internal/app/tui/skills ./internal/skills -count=1
gofmt -l internal/app/tui/skills
```

Expected: tests pass and formatting prints no paths. The race test is scoped
to packages affected by the panel's reads of the shared index.

## Final verification and integration

After both tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p6-skill-identity ./cmd/marshal
go test ./internal/app/tui/... ./internal/skills -count=1 -timeout 2m
go test ./internal/agent -run '^TestPromptPrefixStableAcrossSteps$' -count=1
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/skills
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p6-skill-identity go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual results and compare any full-suite failures to the latest
integrated baseline in [triage](../triage.md); do not suppress failures. The
scripted usability suite currently covers help, not skill management; the
real panel/update tests above are required. If a controlling terminal is
available, inspect duplicate-scope labels and the restart explanation at 80×24
using isolated local fixtures; otherwise record that limitation. Update the
three corresponding audit findings and triage item 7 only after verification,
and commit the named docs and plan with the work.

## Plan self-review

- The three findings have owning tasks and named checks. Restart semantics are
  explicit, including active bodies, shadowed names and plugin overrides.
- No runtime index replacement or prompt-prefix mutation is planned. The
  selected file and shared index are revalidated at action time, and the value
  passed to the loader is the value that was checked.
- Existing paths and anchors were inspected at the base. The new tests and
  implementation were not prototyped during planning.
