# Backend and TUI codebase audit

## Status

This document records the findings from a read-only audit of Marshal's backend,
runtime, tools, pipeline, persistence, providers, indexing, daemon/ACP seam, and
TUI. The audit itself made no code changes. The companion
[triage and batching plan](backend-tui-triage.md) turns these findings into a
recommended delivery order.

The web bridge and `web/**` were explicitly excluded. The daemon conversion and
the separation of the TUI from the agent process are still underway; behavior
that is explicitly specified by that work was not treated as a defect merely
because the conversion is incomplete.

## Method and evidence standard

The audit combined targeted code-path inspection with focused existing tests.
Findings below are supported by concrete code paths. Where an existing test
directly demonstrated the behavior, that is called out separately. A passing
existing test is not evidence that the reported failure case is covered; several
passing tests specifically cover only the current, narrower behavior.

Severity reflects potential impact, not ease of repair:

| Severity | Meaning |
| --- | --- |
| Critical | A security or permission boundary can be bypassed. |
| High | Data loss, workspace escape, unauthorized mutation, or an unbounded hang is plausible. |
| Medium | Incorrect execution, persistence, completion, or responsiveness behavior. |
| Low | Misleading or inconsistent interaction with limited direct damage. |

## Executive summary

| ID | Severity | Area | Finding |
| --- | --- | --- | --- |
| AUD-01 | Critical | Shell policy | Compound `sh -c` and `eval` payloads bypass normal shell analysis. |
| AUD-02 | High | Native file tools | A failed write can truncate the original without leaving a rollback backup. |
| AUD-03 | High | Pipeline operations | Plan-supplied paths can escape the pipeline worktree. |
| AUD-04 | High | Shell policy | Local `git config` mutations are classified as reads. |
| AUD-05 | High | Hooks | Hook timeouts do not reliably bound execution when descendants inherit stdout. |
| AUD-06 | Medium | Pipeline scheduling | Parsed task dependencies are ignored during execution. |
| AUD-07 | Medium | Pipeline recovery | A crashed run cannot resume past its own stale lock. |
| AUD-08 | Medium | Provider streaming | Premature EOF can be converted into a successful turn. |
| AUD-09 | Medium | Session persistence | Rewinding the first turn is not persistent across reload. |
| AUD-10 | Medium | TUI/daemon seam | Browse and settings actions can block the TUI update goroutine on RPCs. |
| AUD-11 | Medium | Semantic index | A transient read error can delete valid indexed content. |
| AUD-12 | Medium UX | Command dispatch | Busy-only commands silently erase the submitted command and do nothing. |
| AUD-13 | Medium UX | Settings lists | Deletion is immediate, mostly undisclosed, and repeat-press prone. |
| AUD-14 | Low UX | Help and mouse state | Mouse and quit help can contradict actual behavior. |

The recommended repair order is: shell-policy bypasses, file and pipeline write
safety, hook cancellation, pipeline scheduling and recovery, TUI responsiveness,
streaming completion integrity, persistence/index correctness, then UX polish.

## Detailed findings

### AUD-01 — Critical: compound shell wrappers bypass Ask-mode and denial policy

**References**

- `internal/tools/policy/shell_parse.go:434-451`
- `internal/tools/policy/shell_parse.go:572-592`
- `internal/tools/policy/evaluation.go:214-256`

**Behavior**

`sh -c`, other shell wrappers, and `eval` send their payload through
`splitCommandWord`. That helper flattens simple statements into one argv and
ignores non-call syntax. The resulting argv is classified as one operation
instead of recursively analyzing every statement the shell will execute.

**Trigger**

```sh
sh -c 'echo ok; touch should-not-exist'
```

The analyzer sees a read-classified `echo` operation with additional operands;
it does not construct a separate `touch` operation. The real shell executes both
statements. The same shape can hide `rm`, `sudo`, redirects, pipelines, or
conditional operations. `eval 'echo ok; touch x'` has the same problem.

**Impact**

Ask mode's read-only ceiling can be bypassed, and configured denial floors for
privilege or catastrophic operations can also be bypassed.

**Recommended correction**

Parse wrapped payloads as shell syntax trees and recursively walk every statement
through the normal analyzer. Preserve the existing operation/effect aggregation
rather than classifying only the first simple command.

**Required regression coverage**

Cover compound statements, pipelines, conditionals, redirects, nested shell
wrappers, and `eval`, including a read-classified first command followed by a
write or denied operation.

### AUD-02 — High: failed file writes can destroy the original without a rollback backup

**References**

- `internal/tools/native/file.go:995-1016`
- `internal/tools/native/file.go:1216-1227`

**Behavior**

Patch-based and whole-file writes call `os.WriteFile` directly on the
destination. An existing file is truncated before the write completes. Backups
are recorded only after the write succeeds.

**Trigger**

Overwrite an existing file when the filesystem fills, a quota or file-size limit
is reached, or an I/O error occurs after truncation.

**Impact**

The tool can return an error after leaving the destination empty or partially
written. The failed attempt has not preserved the original content as a rollback
backup, so a reported failure can cause irreversible source-file data loss.

**Recommended correction**

Write to a same-directory temporary file; check write, sync, and close errors;
preserve the destination mode; and atomically rename over the destination.
Preserve original-content backup evidence before the destructive commit so a
failed commit does not strand the only recoverable copy.

**Required regression coverage**

Inject failures before and during the commit and assert that the original bytes,
mode, and rollback evidence survive. Cover both whole-file writes and patch or
edit writes.

### AUD-03 — High: deterministic pipeline operations can write outside the worktree

**References**

- `internal/pipeline/ops.go:17-70`
- `internal/pipeline/ops.go:183-225`
- `internal/pipeline/parse_blocks.go:133-148`

**Behavior**

`marshal.file` and `marshal.patch` operations directly use
`filepath.Join(dir, planSuppliedPath)`. Compilation and preflight do not reject
parent traversal or verify symlink-resolved containment.

**Trigger**

A plan can contain:

````markdown
```marshal.file path="../outside.txt" replace="true"
replacement
```
````

Additional parent components can reach the original checkout or other user
files. These direct writes do not pass through native file-tool path enforcement.

**Impact**

A supposedly isolated pipeline run can overwrite files outside its worktree,
including user work.

**Recommended correction**

Require clean workspace-relative paths, reject absolute and parent-escaping
paths, and perform symlink-aware containment checks during preflight and again
immediately before mutation.

**Required regression coverage**

Cover absolute paths, `../` escapes, normalized paths that remain valid, symlink
escapes, and both preflight and mutation-time checks.

### AUD-04 — High: local `git config` mutations are classified as reads

**Reference**

- `internal/tools/policy/shell_classify.go:100-107`

**Behavior**

Every non-global `git config` invocation receives `ShellRead`, regardless of
whether it reads or writes.

**Trigger**

```sh
git config user.name audit-poc
git config core.hooksPath /some/path
git config --unset some.key
```

These commands mutate `.git/config` but are admitted as read-only in Ask mode.
Changing `core.hooksPath` can also affect later command execution.

**Impact**

Ask mode's read-only guarantee is violated, and repository behavior can be
altered without approval.

**Recommended correction**

Whitelist actual read forms such as `--get`, `--get-all`, and `--list`.
Classify local setters and unsetters as writes, global/system mutations as
environment changes, and unrecognized forms as unknown rather than read.

**Required regression coverage**

Cover reads, local writes, global/system writes, `--add`, `--unset`, and
unrecognized forms.

### AUD-05 — High: hook timeouts do not reliably bound hook execution

**References**

- `internal/hooks/runner.go:63-65`
- `internal/hooks/runner.go:165-200`

**Behavior**

Hooks use `exec.CommandContext`, which kills only the immediate shell. `runHook`
then synchronously waits in `io.Copy` for stdout EOF before calling `Wait`. A
descendant that inherits stdout keeps the pipe open after the immediate shell is
killed.

**Trigger**

Configure a short hook timeout with a command such as:

```sh
sleep 30 & wait
```

On timeout, the shell can die while `sleep` survives and retains stdout. A
persistent descendant can keep the copy blocked indefinitely.

**Impact**

`timeout_ms` and cancellation fail to bound pre-tool or turn-end hooks,
potentially freezing tool dispatch or turn completion.

**Direct runtime evidence**

The existing `TestHookTimeoutFailOpen` uses a 10 ms timeout and a two-second
sleep. It passed, but the test itself took **2.00 seconds** and the measured
command took **2.13 seconds**, demonstrating that the timeout did not bound the
call.

**Recommended correction**

Run hooks in their own process group and terminate the whole group on
cancellation. Make pipe draining cancellation-aware or close the pipes during
termination.

**Required regression coverage**

Use a descendant that retains stdout and assert that the hook returns near its
configured timeout, the descendant is terminated, and fail-open behavior remains
intact.

### AUD-06 — Medium: pipeline task dependencies are parsed but ignored

**References**

- `internal/pipeline/plan.go:28-32`
- `internal/pipeline/plan.go:100-151`
- `internal/pipeline/controller.go:1230-1242`

**Behavior**

`TaskSpec.DependsOn` records prerequisites, and parsing rejects unknown
dependencies and cycles. Execution nevertheless iterates tasks in file order and
checks only whether the current task is already done.

**Trigger**

A valid acyclic plan where Task 1 declares `Depends on: 2` and Task 2 has no
dependencies executes Task 1 first.

**Impact**

Tasks can be implemented and committed before their prerequisites, producing
incorrect changes or avoidable gate failures.

**Recommended correction**

Schedule only tasks whose dependencies are complete, using topological readiness
while preserving file order among ready tasks.

**Required regression coverage**

Cover reversed file order, multiple ready tasks, resumed partially completed
runs, missing dependencies, and cycles.

### AUD-07 — Medium: a crash leaves the same pipeline run locked against resume

**References**

- `internal/pipeline/controller.go:1122-1145`
- `internal/pipeline/runstore.go:224-310`

**Behavior**

On resume, the controller loads the existing manifest's `RunID`. If a crash left
`run.lock`, takeover is permitted only when the lock's `RunID` differs from the
manifest's.

**Trigger**

Crash or kill Marshal after it writes the manifest and acquires the run lock,
then start the same pipeline run again. The stale lock has the same `RunID`, so
resume returns “already locked.”

**Impact**

The documented crash-resume path is unavailable without manually deleting the
lock.

**Recommended correction**

Add a safe stale-owner check for same-run locks using host/PID liveness or an
explicit recovery decision, then take over atomically. Do not blindly take over
a demonstrably live owner.

**Required regression coverage**

Cover a stale same-run lock, a live same-run owner, a stale different-run lock,
host/PID ambiguity, and concurrent takeover attempts.

### AUD-08 — Medium: streaming providers can turn premature EOF into a successful turn

**References**

- `internal/llm/provider/openai_compatible.go:550-654`
- `internal/llm/provider/anthropic.go:556-657`
- `internal/llm/provider/openai_compatible_responses.go:499-659`
- `internal/llm/provider/ollama_native.go:420-480`
- `internal/agent/chat.go:1079-1135`

**Behavior**

The adapters synthesize `ChatEventDone` after clean EOF even when no terminal
marker or finish reason arrived. The agent can then return a successful result.

**Trigger**

A provider or intermediary closes a stream before its protocol's terminal event
or finish reason.

**Impact**

A truncated response can become a completed assistant turn. Partial tool
arguments may also be repaired and treated as complete, hiding interruption from
both the user and higher-level control flow.

**Compatibility note**

This is partly intentional compatibility behavior:
`TestChatStreamingCleanCloseSynthesizesDone` explicitly pins it and passed during
focused verification. The audit finding is the inability to distinguish that
compatibility case from premature protocol termination.

**Recommended correction**

Distinguish clean protocol completion from bare EOF. Accept EOF after an explicit
finish or terminal event, but surface an interrupted or truncated result
otherwise. Settle the compatibility policy before changing all adapters.

**Required regression coverage**

For every adapter, cover explicit terminal completion, bare EOF, EOF during tool
arguments, transport errors, and the agent's handling of an interrupted stream.

### AUD-09 — Medium: rewinding the first turn is not persistent across reload

**References**

- `internal/app/session/messages.go:498-516`
- `internal/db/sessions.go:260-280`

**Behavior**

Rewinding before the first turn stores branch leaf `0`. On reload,
`GetLeafMessageID` treats both `0` and `NULL` as uninitialized and falls back to
the newest message.

**Trigger**

Run `/rewind 1`, then restart or reconstruct the session before adding another
message.

**Impact**

The rewind appears to work, but the original branch is resurrected after reload.

**Recommended correction**

Fall back to `MAX(id)` only for `NULL`. Treat an explicitly stored zero as the
intentional empty root.

**Required regression coverage**

Round-trip an explicit zero leaf, a NULL legacy value, and a nonzero leaf through
the real persistence path.

### AUD-10 — Medium: browse and settings actions can block the entire TUI on RPCs

**References**

- `internal/app/tui/browse.go:254-310`
- `internal/app/tui/browse_actions.go:130-131`
- `internal/app/tui/browse_inspect.go:14-31`
- `internal/app/tui/settings/browser.go:487-493`
- `internal/app/tui/settings/browser.go:692-738`
- `internal/acpclient/rpc/transcript.go:70-74`
- `internal/acpclient/rpc/settings.go:254-267`
- `internal/acpclient/rpc/settings.go:610-616`

**Behavior**

Browse inspect, copy, and open gather node details synchronously before returning
a command. In daemon mode, `StackNode` is a blocking RPC. File opening can gather
details once per child row. Settings commits similarly call persistence
synchronously; some paths use `context.Background()`, removing cancellation and
deadline support.

**Trigger**

Attach to a daemon session and inspect, copy, or open a browse node, or commit a
settings change, while the daemon or filesystem is slow or unresponsive.

**Impact**

The Bubble Tea update loop waits for the operation. Rendering, further input,
resize handling, and Ctrl+C processing can stall without operation-specific
feedback.

**Recommended correction**

Move these operations onto the existing asynchronous call queue or the
command-based detail-fetch path. Use bounded, model-owned contexts and show
loading or saving state until the result arrives. Report failures explicitly.

**Required regression coverage**

Use a blocking fake session and prove that input and cancellation remain
responsive, results are applied once, and failures produce visible feedback.

### AUD-11 — Medium: a transient file-read error deletes valid semantic-index entries

**References**

- `internal/index/indexer.go:62-66`
- `internal/index/indexer.go:90-96`
- `internal/repo/scanned.go:9-11`

**Behavior**

Files with `ReadErr` are skipped before being marked `seen`. The purge pass then
treats them as deleted and removes their existing chunks.

**Trigger**

A previously indexed file remains present in the scan but encounters a
permission race or transient read failure during reindexing.

**Impact**

Previously valid retrieval context disappears until a later successful reindex,
even though the file was not deleted.

**Recommended correction**

Mark scanned paths as present before skipping unreadable content. Purge only
paths absent from the scan entirely.

**Required regression coverage**

Cover a transient read failure for an already indexed file, a genuinely deleted
file, and a subsequent successful reindex.

### AUD-12 — Medium UX: busy-only commands silently clear the composer and do nothing

**References**

- `internal/app/tui/keypress.go:357-370`
- `internal/app/tui/commands_dispatch.go:163-166`
- `internal/app/tui/commands_dispatch.go:418-420`
- `internal/app/tui/commands_dispatch.go:447-491`
- `internal/app/tui/model.go:4181-4183`

**Behavior**

Enter clears the input before command dispatch. While a turn is running, `/new`,
`/clear`, `/swarm`, `/sdd <plan>`, `/sdd new`, and `/review` return without a
note or restored draft.

**Trigger**

Type one of those commands during a running turn and press Enter.

**Impact**

The command disappears with no explanation or visible next step. It is recorded
in prompt history and recoverable with Up, but the UI does not tell the user
that. Existing tests pin the refusal, not the silence.

**Recommended correction**

Preserve the busy refusal, show a specific explanation, and preferably restore
argument-heavy drafts.

**Required regression coverage**

For each affected command, assert that the refusal remains, a useful note is
shown, and the chosen draft-preservation behavior is consistent.

### AUD-13 — Medium UX: settings deletion is immediate, mostly undisclosed, and repeat-press prone

**References**

- `internal/app/tui/listpanel/fieldlist.go:96-113`
- `internal/app/tui/listpanel/fieldlist.go:303-327`
- `internal/app/tui/settings/browser.go:654-655`
- `internal/app/tui/settings/browser.go:794-838`

**Behavior**

The shared list handles `d` as an immediate deletion, after which the browser
flushes persistence. General row hints omit deletion. Cursor reconciliation is
positional, so after deleting a middle row, the next row occupies the same cursor
position.

**Trigger**

In a settings collection such as MCP arguments, environment entries, providers,
or hooks, press `d`; press it again to remove what was originally the next entry.

**Impact**

A destructive configuration change can occur without confirmation or a visible
key hint. Repeated presses can delete multiple entries faster than the user can
re-read the changed selection.

**Recommended correction**

Add a two-press confirmation, show `d delete` in contextual hints, and preserve
selection by stable row identity across refreshes.

**Required regression coverage**

Cover first-press arming, second-press deletion, cancellation or timeout of the
armed state, middle-row deletion, and stable selection after refresh.

### AUD-14 — Low UX: mouse and quit help contradict actual behavior

#### Mouse capture notice

**References**

- `internal/app/tui/keypress.go:152-176`
- `internal/app/tui/view.go:63-68`
- `docs/daemon/p0-text-selection.md:412-414`

With `mouse_capture=false`, Ctrl+S still toggles `mouseReleased` and announces
“released” or “captured.” `View` forces `MouseModeNone` whenever configuration
disables capture, so the second notice can claim wheel scrolling is captured
while mouse mode remains disabled.

Either let the session-only override deliberately re-enable capture, or suppress
the toggle and explain that configuration must change.

#### Idle Ctrl+C help

**References**

- `internal/commands/commands.go:174-175`
- `internal/app/tui/detach_test.go:31-55`

The static cheatsheet says idle Ctrl+C quits. Daemon sessions instead detach and
continue running; `/exit` is the termination route.

Make the footer mode-aware or use neutral wording, and explain `/detach` and
`/exit` separately.

## Daemon/ACP result

The daemon/ACP review did not substantiate a high-confidence, non-designed
correctness defect. Focused replacement, attach, close, resume, version-skew,
lost-connection, and multi-viewer tests passed. This is a bounded result, not a
claim that the in-flight daemon conversion is complete or defect-free.

## Validation performed during the audit

The following checks passed:

- `scripts/test.sh quick`
- `go vet ./...`
- `go test ./internal/acpclient/rpc -run 'Test.*(Replace|Replacement|Attach|Close)' -count=1`
- `go test ./internal/launch -run 'Test(AVersionSkewWritesOneNote|AResumeReattachesToTheChosenSession|ALostConnectionNamesTheReattach)$' -count=1`
- `go test ./internal/acp -run 'Test(TwoViewersEachSeeEveryUpdate|AViewerLeavingLeavesTheOthersServed|ViewersAreServedAtOnce)$' -count=1`
- `go test ./internal/tools/policy -run '^(TestShellParseEveryNestedExecutable|TestClassifyEnvironmentCommands)$' -count=1`
- `go test ./internal/pipeline -run '^(TestRunFailsWhenAnotherRunHoldsTheLock|TestRunTakesOverStaleLockFromDifferentRun|TestParsePlanDependsOn)$' -count=1`
- `go test ./internal/app/tui -run '^(TestNewSessionStillRefusedWhileBusy|TestMouseToggleReleasesAndReclaims|TestDetachLeavesARunGoing|TestCopyEmitsTheFailedTestOutputOverOSC52)$' -count=1`
- `go test ./internal/app/tui/help -run '^TestFooterMouseHintNamesDragWhileCaptured$' -count=1`
- `go test ./internal/app/tui/listpanel -run '^(TestListDrillEditsSlice|TestMaskedRowKeepsOnEmptyAndClearsOnD)$' -count=1`

The hook timing check also passed as a test while demonstrating AUD-05:

```sh
/usr/bin/time -f 'elapsed=%e' go test ./internal/hooks \
  -run '^TestHookTimeoutFailOpen$' -count=1 -v
```

Result: test duration **2.00 seconds**, elapsed **2.13 seconds**, despite its
10 ms hook timeout.

The existing provider test `TestChatStreamingCleanCloseSynthesizesDone` passed
and pins the compatibility behavior discussed in AUD-08.

No new regression tests were added during the audit. The focused tests establish
the current baseline; they do not cover most of the failure cases above.

## Recommended next step

Use the companion [triage and batching plan](backend-tui-triage.md). Each fix
batch should begin with regression tests that reproduce its finding, keep the
existing daemon/TUI architectural boundaries intact, and finish with the narrow
package gate listed there before the final whole-project validation.
