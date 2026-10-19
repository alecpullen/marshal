# Ask/Act interaction model and shell policy

Status: design sections approved; written spec awaiting user review.
Date: 2026-10-19.
Source baseline: `f637b8edd42b6529b0334c0ffae37ebe100966c2`.
Implementation: not started.

## Intent and success criteria

Simplification is the primary goal. The user normally works in Copilot and
already plans through the built-in brainstorming → writing-plans → execution
skill workflows. Choosing a separate planning mode adds no useful step to that
workflow. Web research, diagnosis, and review are activities available within
the interaction model, rather than additional modes.

Marshal will have exactly two interaction modes:

- **Ask:** investigate and discuss without admitting new mutations.
- **Act:** carry out the request, with the normal collaborative behavior of
  today's Copilot.

**Auto** is an Act modifier that preauthorizes destructive workspace operations
and avoids clarification questions. **System** is an independent grant of
broader access. Neither is a third interaction mode.

The redesign also replaces shell guardrails that mistake command data for
executable behavior. Normal commits, builds, tests, and project-local installs
must work without guardrail interruptions. Genuinely destructive workspace
commands require specific approval in Act and are preauthorized in Act + Auto.

Success means:

1. The ordinary workflow starts in Act, with only Ask and Act in mode pickers
   and the Tab cycle.
2. Brainstorming, saving a spec, writing a plan, reviewing it, and implementing
   it can all happen in Act without mode switches. Existing skill review gates
   remain meaningful.
3. A commit message mentioning `sudo`, `reboot`, `mkfs`, or `git reset --hard`
   is treated as message data.
4. A real dangerous operation, including one in a command substitution or
   shell wrapper, receives the appropriate decision.
5. Ask constrains every new action, including child-agent actions, independently
   of configured allow rules or an earlier approval.
6. Mode changes preserve the conversation and already-running commands. The
   UI clearly shows work that is still running.
7. TUI, ACP, the web control plane, configuration, and custom-agent settings
   agree on one canonical model.

## Current behavior and confirmed defects

The current `ApprovalMode` enum in `internal/tools/policy/policy.go` contains
Plan, Default, Edit, Copilot, and Auto. Plan/Default share a read-only policy
transform; Copilot/Auto share an auto-approval transform. Behavioral differences
also live in `internal/agent/prompts.go` and question dispatch. Mode lists are
mirrored in TUI, ACP, web bridge validation, web UI, and settings.

System currently combines broader filesystem access with relaxed guardrails.
The new model separates those responsibilities: System controls access scope;
Act/Auto control workspace approvals.

A temporary Go test overlay evaluated these strings through the real policy
engine in Copilot, without executing any shell commands:

| Command | Observed current result without System |
|---|---|
| `git commit -m 'fix: ordinary message'` | Allow |
| `git commit -m 'fix: prevent sudo invocation'` | Deny: `sudo` |
| `git commit -m 'fix: handle reboot state'` | Deny: `reboot` |
| `git commit -m 'fix: track mkfs errors'` | Deny: `mkfs` |
| `git commit -m 'docs: use git reset --hard carefully'` | Deny: `git reset --hard` |
| `git commit -m '--hard' -m reset` | Deny: classified as hard reset |
| `git commit -m config -m '--global'` | Confirm: classified as global Git configuration |

The cause is twofold: `analyzeCommandDepth` scans each stage's complete printed
text with `strings.Contains`, and `ClassifyCommand` searches arguments for
subcommands without restricting them to their command positions. System still
denies the `reboot` and `mkfs` message cases.

There is also a guardrail check in the native command tool immediately before
launch. Fixing only the agent's approval decision would leave a second path
that could reject an approved command. Both paths must consume the same
operation analysis and permission contract.

The overlay probe passed as a diagnostic test: it recorded the existing
defects. It does not validate an implementation of this design.

## Canonical state and ownership

The effective interaction state contains:

| Field | Meaning |
|---|---|
| `mode` | Exactly `ask` or `act`. |
| `auto` | Boolean; true is valid only with `mode=act`. |
| `system_access` | Session grant for broader access. Defaults false for a new or resumed conversation. |
| `access_revision` | Session-local revision used to invalidate stale permission decisions and presentations. |

The app runtime owns changes to this state through the session's existing
access-settlement boundary. Policy, tool dispatch, and execution admission use
the authoritative effective snapshot. Configuration contains starting
defaults, not a second mutable copy of the current grants. State publication
must never expose a partially applied combination.

Advance the session-local access revision whenever the effective tuple changes.
A workspace or policy-config change must also invalidate affected pending
authorizations, even if that tuple is unchanged. A standalone toolset without
an authoritative permission/admission seam fails closed; the configured Act
default is not permission to infer authority from a missing runtime.

TUI and ACP submit changes and render the committed result. They do not own
policy decisions, classification, prompt logic, or a separate access state.
The web control plane consumes the JSON contract and keeps its stdlib-only
boundary; it must not import internal Go types.

The old five-value enum must not remain the hidden implementation of the two
modes. Legacy values are translated at configuration/protocol boundaries only.
Remove the old Plan directive and mode-driven skill loading. Existing task
classification may still organize the agent loop, but it cannot grant editing
authority or impose planning merely because a mode was selected.

### Starting defaults and lifetime

Canonical configuration is:

```toml
[agent]
mode = "act"
auto = false
```

- A missing starting mode means Act. Auto defaults false.
- Explicit `mode=ask` always results in Auto off. Explicit Ask + Auto true in
  the same input is invalid; a higher-precedence Ask default clears an inherited
  lower-precedence Auto default.
- System is not a persisted starting default. New and resumed conversations
  start with System off, regardless of a previous session's grant.
- A new or resumed conversation uses configured mode/Auto defaults. It does
  not restore manual interaction overrides from transcript or database state.
- Manual mode/modifier changes are session-only and do not save configuration.
- An unrelated config reload preserves the effective session override and
  System grant. An explicit change to the configured starting mode/Auto tuple
  replaces that tuple's session override. It does not revoke System.
- A failed reload changes no effective state and does not settle a permission
  request as approved.

No database schema change is required to persist grants: effective grants must
not be persisted for restoration in the first place.

## Interaction contract

### Ask

Ask can investigate, explain, compare approaches, review code, and discuss or
outline designs and plans. It does not save those documents or implement them.

New actions are limited to:

- Read-only native tools, such as file/search/symbol reads and repository
  status, diff, log, and blame.
- Shell operations positively classified as read-only, including their flags,
  redirections, substitutions, and other stages. A familiar executable name
  alone is insufficient.
- Read-only network tools under the applicable network permission policy.
- MCP tools with an explicit read-only classification. Unclassified MCP tools
  remain mutation-capable; a suggestive tool name or an untrusted hint must
  not silently classify one as read-only.
- Read-only delegation, with the same permission ceiling applied to the child.
- Session bookkeeping and instruction loading, including history, todos,
  tool selection, and skill loading, provided those operations cannot execute
  a mutation as a side effect of loading.
- An explicit capability request through `mode.request`.

Tests, builds, dependency installs, arbitrary scripts/interpreters, unknown
shell effects, file writes, config writes, and other mutations require Act.
Diagnostics that execute a mutation-capable configured command also require
Act; cached diagnostics and read-only language-server queries can remain
available. Risk classifications must reflect this distinction.

Ask's restriction is evaluated before allow rules, ordinary approvals, and
auto-approval settings. An allow rule cannot turn Ask into an editing mode.
Approving one ordinary tool call cannot do so either; the user must explicitly
grant Act. Marshal's own transcript, database, logs, caches, and other managed
session bookkeeping continue operating.

System broadens Ask's available read scope without adding write authority.
Ask does not force a numbered plan, a short Q&A response, or a special role.

### Act

Act is the normal working mode. It inspects, edits, builds, tests, installs
project-local dependencies, and commits as required by the user's request.
Routine operations do not require approval of each file change.

It asks clarification questions when the answer materially changes the work
and cannot be resolved from available evidence. Otherwise it proceeds with
reasonable assumptions. It still follows the active skill's workflow and
review gates.

Act requires specific command approval for destructive workspace operations.
Approval is scoped to the presented command and execution context, rather
than a standing permission to run any destructive command.

### Auto

Auto is valid only in Act. It:

- Proceeds without clarification questions, using and stating reasonable
  assumptions.
- Preauthorizes destructive operations scoped to the active workspace.
- Does not grant System, root privileges, publishing approval, or approval of
  uncertain command effects.
- Does not override explicit deny or confirm rules.
- Does not manufacture skill approvals. A required spec/plan review or another
  explicit workflow gate still causes the agent to stop and present the
  artifact for review. A later user response can authorize the next stage.

If a choice cannot be settled within the user's instructions, the agent reports
the blocker. If a required runtime approval is pending, it is shown as waiting
for approval rather than falsely reporting success or silently discarding it.
Clients without an approval responder receive the existing non-approved
failure/cancellation outcome rather than an invented grant.

### System and privileged execution

System grants broader access for the conversation, including file/search paths
outside the active workspace. Act + System permits routine edits in that
broader scope; Ask + System permits reads only.

System does not relax workspace destructive-command approval. Act + System
still asks; Act + Auto preauthorizes workspace destruction regardless of
whether System is enabled. Destructive operations outside the workspace and
global environment changes need System plus specific approval in either case.

Root remains separate. Preserve the existing dedicated `shell.sudo` path,
secret-capable transport requirement, fresh/unlock behavior, and restrictions
on child-agent privileged execution. Actual privilege wrappers in ordinary
shell execution are redirected/refused with guidance to use `shell.sudo`;
merely mentioning their names in data is harmless.

The bare `shell.sudo` payload uses the same shell analysis, including the
catastrophic floor and publishing rules. Authentication is not a bypass of
those decisions. Where the existing command/password prompt can also collect
specific approval of the exact command, use that interaction rather than
adding a duplicate prompt. A warm unlock grant cannot substitute for a
required command approval.

## Controls, transitions, and running work

| Input | Result |
|---|---|
| `/ask` | Ask, Auto off; preserve System. |
| `/act` | Act, Auto off; preserve System. |
| `/auto on` | Act + Auto, including when issued from Ask; preserve System. |
| `/auto off` | Clear Auto; preserve the current mode and System. |
| `/auto` | Toggle Auto; enabling it enters Act. |
| `/system on`, `/system off`, `/system` | Set/toggle System; preserve mode and Auto. |
| `/system unlock` | Retain the existing separate sudo-unlock control. |
| `/mode ask`, `/mode act`, mode picker | Same normal Ask/Act transitions as the direct mode commands. |
| Tab / Shift+Tab | Toggle Ask ↔ normal Act. Auto is never restored implicitly. |

The mode picker contains only Ask and Act. Remove the old Plan, Default, Edit,
and Copilot commands and mode-picker entries. `/plan` is not repurposed into
another workflow shortcut in this change; planning remains available through
the existing skill workflow. Removed commands return an actionable explanation
of the replacement instead of secretly selecting a legacy mode.

The status must retain the persistent mode and all active modifiers, for
example `Ask`, `Ask · System`, `Act · Auto`, and `Act · Auto · System`.
Approval, browsing, answering, and activity indicators must not conceal the
effective permission state. Running-work information remains visible in the
existing status/work surfaces. No new mandatory panel or confirmation is
introduced for an explicit human mode command.

### Act → Ask during execution

Switching to Ask affects new admissions immediately. It does not cancel or
restart a command already launched before the accepted transition. Such a
command may still write files; the running-work surface must make its
continued existence clear. The mode receipt explains this when work is active.

This exception applies to already-launched processes, not an entire worker's
future authority. A child agent, pipeline role, or repeating watch cannot keep
admitting writes under a captured Act grant. Each subsequent tool/command
admission follows the owner's current Ask ceiling. A command approved but not
yet launched is a new admission and must be revalidated.

Preserve the existing explicit stop controls. A mode transition does not end
the turn, rebuild a provider, replace command/watch managers, recreate a job,
or reset the conversation. Cancellation, session replacement, and shutdown
retain their existing lifecycle ownership.

## Shell analysis and decisions

### Shared structured analysis

Retain the existing shell parser; replace the overlapping substring and
whole-string classification paths with one structured analysis. Its inputs
include the exact command, captured working directory, active workspace root,
relevant known execution environment, and normalized permission configuration.

Analysis describes executable operations, effects, targets, scope, uncertainty,
and stable reason codes. Decision logic must use those facts, rather than
matching the wording of presentation strings. The analysis must:

1. Distinguish the executable, actual subcommand, flags and their values,
   operands, redirections, and ordinary data arguments.
2. Inspect every executable stage in chains, pipelines, subshells, executable
   command/process substitutions, and recognized inline shell payloads.
3. Decode quoted/escaped executable names and recognized transparent wrappers.
   Preserve a finite recursion bound; reaching it yields uncertainty rather
   than silent permission.
4. Treat single-quoted text and quoted literal argument contents as data.
   Inspect expansions that really execute even when they occur inside a
   double-quoted argument or an expandable heredoc.
5. Resolve known working-directory changes and command-specific directory
   options, including Git's `-C`. Account for relative paths, existing symlinks,
   mixed targets, and the active worktree rather than assuming the main
   checkout is the working directory.
6. Classify a command conservatively when paths, executable names, flags,
   payloads, or effects remain uncertain. No legacy keyword-scanning fallback
   may turn uncertainty into either a spurious dangerous-operation match or
   an implicit Auto grant.

Known build/test entry points and ordinary language/package commands remain
usable as such; this design does not inspect every instruction inside every
program they execute. Conversely, an arbitrary interpreter payload cannot be
called read-only merely because its command name is familiar.

### Policy matrix

Ask's read-only ceiling and explicit user rules apply before the following
Act matrix. A compound command receives all applicable constraints: a routine
first stage cannot hide a destructive, external, or unknown later stage.

| Operation | Act | Act + Auto |
|---|---|---|
| Routine workspace reads/edits, builds, tests, commits | Allow | Allow |
| Project-local dependency installation | Allow | Allow |
| Destructive workspace operation | Specific approval | Allow |
| Routine read outside the workspace's permitted read scope | Require System, then allow | Require System, then allow |
| Routine file edit outside the workspace | Require System, then allow | Require System, then allow |
| Destructive operation outside the workspace | Require System and specific approval | Require System and specific approval |
| Global installation or environment change | Require System and specific approval | Require System and specific approval |
| `git push` or a configured publishing/floor command | Specific approval | Specific approval |
| Valid command with uncertain operation/scope | Specific approval | Specific approval |
| Catastrophic-floor operation | Deny | Deny |

Project-local installs include local npm/pnpm/yarn installs and module-local
Go dependency changes. A Python install must have a known project-local target
or virtual environment to enter this category. Global flags, external install
prefixes, and machine package managers enter the global/environment category.
An uncertain target enters uncertainty. Incidental normal build/package-cache
writes do not make a project-local build/install a global environment change;
explicit cache destruction or global configuration changes do.

Destructive workspace classification includes hard resets, forced clean,
discarding work through restore/forced checkout, forced branch deletion,
recursive deletion, destructive find payloads, file shredding/truncation,
bulk recursive ownership/permission changes, and destructive overwrites.
Recognize equivalent executable forms rather than only one spelling of a flag.
Dry-run/read-only variants must be distinguished. Ordinary file edits and
routine single-file removal do not reintroduce approval-per-change.

For Auto's filesystem preauthorization, every affected target must be known to
be within the active workspace. Deleting the workspace root itself is not a
routine bounded target and requires specific approval even in Auto. Git's
workspace-scoped destructive operations use the repository they actually target.
Mixed inside/outside targets use the outside-workspace requirements. Unresolved
target scope never receives workspace preauthorization.

### Small hard-deny floor

Keep a small operation-based floor in every mode and with System enabled:

- Disk/filesystem formatting and raw-disk/block-device overwrites.
- Recursive deletion of the filesystem root or the user's entire home,
  including recognized equivalent forms that delete their contents.
- Machine shutdown and reboot.

These are checks on executable operations and affected targets. Mentions in
commit messages, search patterns, paths, or documentation do not trigger them.
Ordinary sinks such as `/dev/null` are not raw disks. Root/home comparisons use
resolved known paths, not substring tests. Other destructive system targets
use the System + specific-approval category rather than expanding this floor
into a large new hardcoded deny list.

Reject malformed shell syntax and command-display control characters with a
clear validation error before execution. This is distinct from a valid but
uncertain command, which may receive specific approval in Act. Neither case
uses the legacy substring fallback.

### Explicit rules and defaults

Explicit user deny rules remain effective in Auto. Explicit confirm rules are
also respected. Generic allow rules cannot bypass Ask, missing System access,
the hard-deny floor, publishing/floor approval, external destructive/environment
approval, uncertainty approval, or the normal-Act requirement to approve a
destructive workspace command.

Rules apply to the actual parsed operations; a permitted first command does
not authorize a later stage. Plain shell command patterns use parsed command
prefix matching rather than substring matching across message data. For
example, a plain deny for `sudo` applies to an invoked privilege wrapper,
not `git commit -m 'document sudo'`. Existing explicitly authored glob/regex
rules remain opt-in expressions with their documented argument matching;
they must not be recreated as hidden built-in keyword checks.

Replace the shipped blanket deny defaults for recoverable workspace operations
with the matrix above. Also remove shipped confirm-list entries for routine
project-local installs, such as `go get` and `npm install`, so they do not
override the new ordinary-operation default. Retain configured publishing/floor
commands and the intrinsic `git push` approval requirement. Preserve explicit
user rules during migration; do not erase them because they resemble an old
default. A preserved rule's decision identifies that rule in the reason.

Decision precedence is: input validation and the hard floor; explicit denies;
the Ask capability ceiling; missing System access; mandatory command approvals
(publishing/floor, external destructive/environment, uncertainty, and explicit
confirm rules); normal-Act destructive workspace approval; then ordinary
permission/Auto preauthorization. No generic allow rule suppresses a mandatory
approval. For a command with both missing access and a required command approval,
obtain the access grant first, then revalidate and approve the exact operation.

The old `tools.shell.auto_approve` and `guardrail_dynamic_argv0` controls are
retired. They are accepted as deprecated configuration inputs with a diagnostic
and omitted on a subsequent explicit save. Neither enables Auto or disables
uncertainty checks. The new Act/Auto contract and explicit rules replace those
overlapping controls.

### Approval and execution admission

An approval identifies the owning session/actor, request, exact command,
working directory, target workspace, presented permission revision, and
applicable analysis. It authorizes one admission, not a reusable destructive
command prefix. Edited commands are analyzed and authorized again.

After a response, recheck the current capability ceiling, command identity,
scope, and relevant configuration before launch. A stale or changed
presentation cannot apply a partial grant or execute under former Act
permissions. A changed request is refreshed/reapproved as appropriate. An
already-admitted process retains its captured execution specification.

There must be one semantic source for approval and execution. The native
preflight can reject malformed input, a hard-floor operation, a missing
capability, or a stale/mismatched authorization, but cannot resurrect the old
blanket denial after a guarded operation was validly approved. The same
analysis contract applies to `shell.run`, `test.run`, and bare `shell.sudo`
payloads. Existing process admission, steering, cancellation, and shutdown
arbitration remain authoritative for process lifetime.

Approval presentation states the operation and target, for example “Discard
tracked changes in this worktree,” rather than merely showing a matched
keyword. Display the exact command using the existing display-control
protection. A denied/expired/canceled approval returns that actual outcome;
it must not be described as a mode switch or successful execution.

## Capabilities, children, and clients

Keep `mode.request` as the model-facing capability request. Its canonical
editing request is `mode: "act"`; it may independently request `system: true`
and supply a reason. It cannot request or grant Auto. Legacy editing hints are
translated to a request for Act, not to a selected autonomy setting.

The existing typed resolver and dedicated access panel continue to own
settlement. Use only the missing-capability choices: allow Act, allow System,
or allow Act + System. Remove the Edit/Copilot/Auto selection. System-only
approval preserves the mode and Auto. Combined approval applies atomically.
Agent-requested Act grants normal Act, not Auto. Denial, Esc, cancellation,
stale identity, and shutdown retain the existing exactly-once settlement rules.

Child agents and role runners have permissions bounded by the owning
conversation's current state and their own narrower tool/role restrictions.
They cannot obtain Act, Auto, or System beyond that ceiling. System inheritance
still requires both the parent's grant and the child's explicit request.
Read-only delegation remains available from Ask. Custom-agent configuration,
pipeline auto-policy clones, and shared/filtered registries must not bypass
this boundary. Preserve existing model-cost consent and privileged-child
restrictions.

Mode/modifier changes are runtime operations. They do not save a file, rebuild
providers, replace managers, or rewrite messages before the transcript within
an active turn. Communicate an accepted change through append-only runtime
context or the working-state tail, and build future turns with the appropriate
stable instructions. Preserve prompt-prefix stability.

### ACP and web contract

Extend session creation, state responses, permission presentations, and
`mode_changed` updates to report canonical `mode`, `auto`, `system_access`, and
`access_revision` where effective access is represented. Keep existing JSON
field spelling such as `sessionId` at its established boundaries.

`session/set_mode` accepts an atomic patch with optional `mode`, `auto`, and
`system_access`; at least one change is required. The result and notification
report the full committed access state. Rules are:

- A canonical mode selection alone enters normal Ask/Act with Auto false.
- `auto: true` without a mode enters Act + Auto, matching `/auto on`.
- `auto: false` without a mode preserves the mode.
- Explicit Ask + Auto true is invalid and applies nothing.
- An omitted System field preserves System; explicit false revokes it.
- Modifier-only updates do not depend on a dummy mode selection.

The runtime validates and applies the entire patch before acknowledgment.
Legacy accepted mode values are translated at the ingress boundary for this
release; responses always report the canonical state. A legacy capability
approval can grant the requested Act/System capabilities but cannot smuggle
Auto through an old editing-mode string.

Update all in-repo clients together: TUI settings/help, custom-agent settings,
ACP session creation/switching, bridge project/recipe/workspace policy handling,
web mode controls, spawn defaults, stored preferences, and status projections.
Do not leave a five-mode list in one client. The bridge maintains its own JSON
types rather than importing internal Go types. Existing unsupported secret
transport behavior is unchanged.

## Configuration and legacy migration

Normalize each configuration layer before merging it, preserving field
presence. Later layers retain their existing precedence. Canonical `agent.mode`
and `agent.auto` replace `agent.approval_mode`. Apply the same model to
custom-agent interaction settings, bounded by the parent at runtime.

| Legacy mode | Canonical result |
|---|---|
| `plan`, `default` | Ask, Auto false |
| `copilot` | Act, Auto false |
| `auto` | Act, Auto true |
| `edit` | Ask, Auto false, with a notice that per-change approval was retired |

Legacy Auto deliberately adopts the new workspace preauthorization contract;
the migration diagnostic explains that change. Legacy Plan does not load a
planning skill or impose a planning response. Stored web `read` preferences
normalize to Ask; this does not invent a third canonical value.

When a layer supplies canonical `mode`, ignore that layer's deprecated
`approval_mode` with a diagnostic. A canonical `auto` field overrides an
autonomy value derived from the legacy mode. Do not silently obtain Auto from
an ignored legacy value. When an explicitly supplied higher layer chooses Ask,
clear any inherited Auto default.

The migration notice identifies the source setting and effective replacement
without blocking the conversation behind another approval. New configuration
saves emit canonical fields only, and only through an explicit existing save
surface; loading or manually switching modes does not rewrite config files.

Unknown explicit values and inconsistent patches are errors, not a fallback to
Act. At startup, invalid interaction configuration fails with an actionable
diagnostic. During settings/reload or RPC updates, it leaves the prior valid
effective state intact. Distinguish an absent value, whose default is Act,
from a supplied unrecognized value.

Compatibility acceptance is limited to boundaries in this release. Removing
those adapters later is a separate change; it must not be required to finish
this spec. No legacy mode behavior survives inside the canonical runtime.

## Acceptance and verification

Tests use `testenv.Run`, explicit fixtures, test-owned endpoints, injected auth,
and fake process runners where appropriate. They must not read developer
credentials, execute destructive probes, invoke real sudo, or use ambient
services. Establish a baseline before implementation and distinguish existing
failures from new regressions.

Required cases:

1. Fresh defaults are Act/Auto off/System off. Every mode picker and Tab cycle
   contains exactly Ask and Act. Ask → Act never restores Auto.
2. Each direct command and atomic ACP patch follows the transition table.
   Invalid patches apply nothing; explicit false and omitted modifiers remain
   distinguishable. Status reflects every active modifier without relying on
   color alone, including while a permission or another panel is open.
3. Brainstorming → spec → writing-plans → reviewed implementation works in Act
   without a mode change. Auto stops at required review gates and never
   invents an approval.
4. All reproduced commit-message cases allow in normal Act under the new
   defaults. Include multiline messages, multiple `-m` arguments, escaped
   quotes, and harmless words/paths containing the old blocked substrings.
5. `git commit -m 'docs: $(sudo id)'` treats the single-quoted text as data;
   a double-quoted executable substitution is analyzed as execution. A
   `git commit ... && git reset --hard HEAD` chain still sees the hard reset.
6. Actual workspace destructive commands require approval in Act, run after
   that approval without a second guardrail denial, and are preauthorized
   in Auto. Cover pipelines, transparent wrappers, inline shells, and
   equivalent flag spellings.
7. Check outside-workspace, symlink-escaping, mixed, `git -C`, changed-directory,
   workspace-root deletion, and uncertain targets. Auto never infers a bounded
   workspace grant from unresolved target scope. System alone does not
   preauthorize workspace or external destruction.
8. Global installs/config changes retain System + specific approval in Auto.
   Project-local installs and ordinary cache writes do not acquire those
   requirements. Explicit external/cache destruction still does.
9. Publishing/configured-floor approvals, explicit deny/confirm rules, and the
   catastrophic floor remain effective in Auto and with System. Floor and
   privilege-wrapper words in literal data do not trigger operation checks.
10. Valid uncertain commands prompt in Act/Auto; malformed syntax and display
    controls are rejected clearly without the substring fallback.
11. Ask blocks writes, tests/builds, unknown scripts, unclassified MCP tools,
    and configuration mutations despite permissive rules or stale approvals.
    Known read operations, approved network reads, session bookkeeping,
    read-only delegation, and Ask + System reads still work.
12. Switching to Ask leaves a previously launched fake/managed command alive
    with the same execution identity and captured settings, and shows it as
    running. It blocks a pending/unlaunched write and subsequent child, watch,
    or pipeline write admissions. Explicit stop controls still work.
13. Race tests exercise approval versus mode change, config reload, workspace
    change, cancellation, and shutdown. No old revision can grant a new
    admission, clear a newer request, or acknowledge unapplied authority.
14. New/resumed conversations follow configured defaults without restoring a
    manual System/Auto grant. Unrelated reloads preserve overrides. Explicit
    changed starting defaults replace the appropriate override without
    rebuilding managers as part of the access change itself.
15. Every legacy mode, conflicting canonical/legacy fields, layered overrides,
    retired shell setting, custom-agent setting, and stored web preference
    has an explicit migration case. Edit does not become auto-editing Act.
16. ACP and the web UI report the same committed state as TUI and policy.
    Typed and legacy capability replies cannot grant unrequested Auto.
17. Prompt-prefix stability, model-facing prompt/schema budgets, the existing
    access-panel behavior, background lifecycle, sudo authentication, and
    `TestWebIsStdlibOnly` retain their regression coverage.

Run the relevant focused and race tests during implementation, then the
repository checks appropriate to the final change:

```sh
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -race ./internal/tools/policy ./internal/app/session ./internal/acp
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build -o /private/tmp/marshal-interaction-modes ./cmd/marshal
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-interaction-modes go test ./test/usability/... -run TestScripted -v -timeout 5m
```

For changed web UI code, run `npm test` and `npm run build` in `web/ui`, then
rebuild `cmd/webbridge` against the generated assets. Keep generated assets
consistent with repository conventions. The implementation plan will name
package-level checks and concrete test functions after inspecting the final
implementation base; this spec does not claim those checks have run.

## Decisions, assumptions, and non-goals

Approved decisions are the two-mode model; Act as the built-in default; no
approval-per-file-change option; Auto's workspace preauthorization; System as
broader access; the shell matrix and small catastrophic floor; continuation
of already-running commands; canonical replacement across clients; and the
legacy migration table above.

Implementation assumptions made explicit in this spec:

- The current shell parser, command service, typed capability resolver, dock
  host, and sandbox backends are reused rather than replaced.
- A workspace boundary means the active worktree/root at admission, not the
  directory from which Marshal originally launched.
- Mode policy classifies requested operations. It is not an OS filesystem
  sandbox or a proof of every side effect of a build, hook, script, MCP
  service, or other opaque program. Existing sandbox guarantees remain those
  of the selected backend. This work does not infer arbitrary program bodies.
- Plain prefix rules are corrected to match operations. Deliberately authored
  argument glob/regex policies remain explicit user policy, not hidden
  built-in heuristics.
- Routine edits continue to prefer `file.write`/`file.edit` for diff review,
  backups, and rollback. System no longer changes the meaning of a shell
  redirection: shell writes follow the same operation/scope policy. This
  does not add automatic file-tool rollback to arbitrary shell mutations.

Explicit non-goals:

- Plan, Web, Diagnose, Review, or any other third interaction mode.
- A new planning workflow or a new mandatory planning/implementation button.
- A retained hidden Edit mode or an advanced per-change approval preference.
- A new network-permission model, provider/router behavior, secret transport,
  privileged daemon, or sudo authentication design.
- A wholesale sandbox replacement or exhaustive interpreter/script analysis.
- Automatically stopping existing commands when entering Ask.
- Automatically rewriting config on load, persisting session grants, or
  building an implementation plan before written-spec review.
- Changes to the unrelated work already present in the working tree.

Open questions: none. The four design sections were explicitly approved in
conversation. The written artifact remains subject to the user's review.

## Handoff

Review this written spec and revise any decisions before planning. After
written-spec approval, use the built-in `marshal-writing-plans` workflow for
the implementation plan unless the user requests the `/sdd` authoring path.
No product implementation is authorized by this document's creation alone.
