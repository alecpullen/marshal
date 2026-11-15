# Project and factory settings: combined UI

Date: 2026-11-15
Status: design and implementation plan approved 2026-11-15. The
product choices marked agreed below come from discussion; implementation
details are proposals. It extends the existing
[factory defaults specification](project-factory-defaults-spec.md), including
its implementation record, and requires a new plan before code changes.

## Intent and agreed choices

Configure a project's ordinary agent defaults and factory defaults together,
grouped by concept. Make it easy to reach the relevant project configuration
from the factory, including on a phone. Factory settings remain project-scoped;
do not present them as global factory-wide configuration.

The factory workspace and both factory role model profiles follow the
corresponding ordinary agent default until the user overrides each one. The
implementer and reviewer both follow the same project agent profile initially,
but each may be overridden independently. A user can return any override to
following the project default; later changes to the agent default then take
effect for future handoffs. Existing created work retains the resolved
configuration saved with it. The handoff remains the primary place to inspect
and override the configuration for one work item.

This does not add external worker backends, planner delegation, workflow
editing, live adoption of existing work, or new forge support. The preview
environment presentation is separate work.

## Approaches considered

1. **Separate Agent and Factory sections on one project screen:** simple to
   implement, but the relationship between corresponding values is less clear.
2. **One project settings screen grouped by concept (selected):** place agent
   defaults and their factory overrides together, with factory-only choices in
   their own group. This makes inheritance visible at the point of choice.
3. **Factory-only settings area:** keeps factory choices together but obscures
   their relationship to the project's ordinary agent defaults and suggests a
   broader scope than intended.

## Proposed interaction

- Group ordinary and factory workspace choices together; group the ordinary
  agent profile with the implementer and reviewer profile choices. Each factory
  choice explicitly offers **Use project default**, displays its current
  effective value and offers an independent override. Preserve a saved value
  even if it is no longer in a picker; explain that it needs attention.
- Put factory-only choices in a clearly labeled group: target branch, reviewer
  instructions, expected CI checks and maximum rounds. Place CI polling and
  wait limits under Advanced. The host is currently local-only and does not
  need a chooser. Keep the existing per-handoff choices for native execution,
  implementer automatic permissions and no-required-CI acknowledgement.
- Explain that project settings affect new handoffs, not drafts or work already
  created. Settings remain optional; indicate when the project itself has no
  workspace or profile instead of presenting an empty value as a configured
  environment or profile.
- In the handoff form, show effective values and where each came from, allow
  work-item overrides and a way back to the inherited value. Do not eagerly
  send a displayed fallback as an explicit override. Keep the distinction
  between inheriting expected checks and explicitly requiring none; the latter
  still needs the existing per-handoff acknowledgement.
- Make the combined controls usable at phone widths, with labels, touch-sized
  controls and save, error and unsaved-state feedback. Offer navigation from
  factory work to the relevant project settings, preserving user input when
  navigating away or making any loss explicit.

Exact grouping, layout, save behavior, labels and the interaction for choosing
CI checks are implementation design choices to verify against the existing
Studio UI. The currently available handoff form does not expose every backend
field; the plan must identify which controls to add and how to show resolved
settings without introducing a second source of truth.

## Resolution and compatibility

The shipped factory-defaults backend treats ordinary agent and factory
workspace values independently. Supporting the agreed live inheritance needs a
backend change, not only new controls. Proposed precedence for workspace and
each role's model profile is: explicit handoff override, explicit factory
default, project agent default, then the existing built-in or validation
behavior. The backend owns resolution for all clients at handoff creation;
the UI must not emulate it as the only enforcement path. Preserve existing
request replay identity and the stored snapshot semantics.

Preserve explicitly saved factory values as overrides. Projects without a
factory override acquire the agreed following behavior; assess existing
projects carefully because they previously had independent settings and this
changes their effective configuration. In particular, inherited workspace
versus an explicit native-execution choice can conflict. Define an unambiguous
resolution and migration/compatibility policy before implementation rather
than silently selecting a workspace or changing native execution. Profile
routing can contain more than a profile string: retain existing per-role
overrides and avoid dropping fields when editing either a project or a factory
setting.

Project settings use a whole-record PUT. Preserve unrelated fields, including
unknown or newly introduced settings, and account for stale concurrent edits
between surfaces rather than silently replacing another user's changes. The
precise concurrency mechanism needs to be chosen against the existing API.
Surface validation, missing dependencies and unavailable profiles/workspaces
without treating failed loading as a request to clear saved settings.

## Validation and implementation planning

The plan should verify following behavior as project defaults change, separate
role overrides and reset-to-follow, explicit handoff overrides, and unchanged
created drafts. Cover no project default, existing factory defaults, native vs
workspace conflicts, expected-CI inheritance versus explicit empty selection,
settings preservation and concurrent/stale saves, and backend errors. Exercise
the real combined and handoff screens at desktop and phone widths, including
keyboard access and recoverable unsaved edits.

Recheck the current P5 branch and the parallel preview-environment work before
choosing files or a base. Keep the change within the existing web/ standard
library boundary and avoid P5's fleet inbox, viewer and attachment ownership.
The implementation plan is
[Project factory settings](plans/2026-11-15-project-factory-settings-plan.md).

## Implementation record

Implemented on branch `project-factory-settings` from `40c2194d` (current
`main`, after the planning PRs), as the four tasks of the
[plan](plans/2026-11-15-project-factory-settings-plan.md) plus a verification
task. The design's open questions — the exact precedence, the native-versus-
workspace policy, and the concurrency mechanism — are settled as follows.

### Precedence and native behaviour

Resolution is `handoff > project factory defaults > project agent defaults >
existing normalisation/validation`. The project agent level covers exactly
three fields: the workspace, the implementer route and the reviewer route. Each
role falls back independently to `{"profile": <ProjectSettings.Routing.profile>}`;
every other member of a stored routing object is preserved, and a project whose
routing has no usable `profile` contributes no fallback.

An explicit handoff `native: true` suppresses only the *project agent* workspace
fallback. A project *factory* workspace combined with native stays the explicit
conflict, with both sources named in the error. The backend never infers
`native: true` from an empty project workspace. Existing projects with no
factory override therefore follow their agent defaults for future creates only:
prior drafts, approvals, retry replacements and replayed requests keep the
configuration they were created with.

### Conditional settings writes

`GET /api/projects/settings` now returns an `ETag`: a quoted, lowercase SHA-256
of the JSON encoding of the typed settings. `PUT` accepts `If-Match`; the
comparison, the map update and the persistence happen under `Workspace.mu`, and
a mismatch is 412 with no write, no intake mirroring and no audit. A single
quoted 64-hex tag is accepted; `*`, lists, unquoted, weak or wrong-length values
are 400. A `PUT` without `If-Match` keeps its legacy unconditional behaviour.

The combined settings screen keeps one versioned saved record for every write,
spreads the fetched record so unknown fields and nested routing members survive,
and on 412 keeps the user's edits and offers an explicit reload — never a silent
retry. A successful write updates the stored record and the version token but
does not revert a field the user changed while that write was in flight.

### Verification

`web/bridge` covers the inheritance and conditional-write contracts in
`factory_defaults_test.go`, `factory_defaults_http_test.go` and
`projects_test.go`; `web/ui` covers the screens in `Project.test.ts` and
`FactoryNew.test.ts`. The real bundle is exercised at desktop and 390px by
`web/ui/e2e/project-settings.pw.ts`, run through
`scripts/e2e/run_browser_checks.sh`, which starts a fixture bridge with no
reachable provider.
