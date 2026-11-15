# Backend and TUI audit triage

## Purpose

This document sequences the findings in the
[backend and TUI codebase audit](backend-tui-audit.md). It is a delivery and
batching recommendation, not an implementation plan or a claim that any fix has
already landed.

The web bridge remains out of scope. The ordering deliberately works with the
ongoing daemon conversion and TUI/agent separation rather than treating those
in-flight designs as defects.

## Prioritization rules

1. **Permission boundaries first.** Ask mode and configured denial floors must
   describe the operations that actually execute.
2. **Data preservation and isolation next.** A failed tool call must not destroy
   user work, and an isolated pipeline must not write outside its worktree.
3. **Unbounded hangs before general correctness.** Cancellation and timeouts
   should bound hooks and daemon-backed UI actions.
4. **Execution and completion integrity next.** Dependencies, crash recovery,
   stream termination, persistence, and indexing should not silently produce a
   false successful state.
5. **UX polish last, but not deferred indefinitely.** Silent command loss,
   destructive repeat deletion, and misleading help remain user-facing defects.

Each batch should start with regression tests that fail for the stated reason,
keep one behavioral theme per review, and land with its narrow package gate.
Relative sizes below are **S** (small), **M** (medium), and **L** (large); they
are coordination estimates, not time commitments.

## Recommended landing order

| Order | Batch | Findings | Priority | Size | Reason for this position |
| ---: | --- | --- | --- | --- | --- |
| 1 | Shell policy fail-closed | AUD-01, AUD-04 | P0 | M | Closes two Ask-mode policy bypasses in one package. |
| 2 | Write safety and containment | AUD-02, AUD-03 | P0 | M | Prevents data loss and pipeline worktree escapes. |
| 3 | Hook cancellation | AUD-05 | P0 | M | Makes an existing timeout and cancellation contract real. |
| 4 | Pipeline scheduling and recovery | AUD-06, AUD-07 | P1 | M | Corrects execution order and restores crash resume. |
| 5 | Nonblocking daemon-backed TUI actions | AUD-10 | P1 | M–L | Removes UI-wide stalls at the daemon seam. |
| 6 | Streaming completion integrity | AUD-08 | P1 | M–L | Prevents truncated streams from becoming successful turns. |
| 7 | Session and index durability | AUD-09, AUD-11 | P2 | S–M | Two contained persistence corrections with clear tests. |
| 8 | Interaction safety and truthful feedback | AUD-12, AUD-13, AUD-14 | P2–P3 | M | Resolves silent, destructive, and misleading interactions. |

P0 means “fix before relying on the affected safety boundary.” P1 is the next
correctness and responsiveness tranche. P2 is important follow-up work; P3 is
low-severity cleanup. The ordering is a recommendation, not a reason to delay a
P0 fix while waiting for an earlier batch to finish.

## Batch 1 — Shell policy must fail closed

**Findings:** AUD-01, AUD-04

**Primary package:** `internal/tools/policy`

### Scope

- Replace wrapper-payload flattening with recursive shell-syntax analysis for
  `sh -c`, related wrappers, and `eval`.
- Aggregate the effects of every wrapped statement through the existing policy
  evaluation path.
- Distinguish `git config` reads from local, global, and system mutations.
- Default unrecognized `git config` forms to unknown rather than read.

### Why these belong together

Both findings are misclassification bugs in the same security boundary. Fixing
them together lets the regression suite assert one coherent rule: the classifier
must account for every operation a command can perform, and unfamiliar forms
must not receive a weaker classification by default.

### Tests and gate

Add compound, pipeline, conditional, redirect, nested-wrapper, and `eval` cases,
plus `git config` read/write/unset/global/system/unknown cases. Include a
read-classified first statement followed by a denied operation.

```sh
go test ./internal/tools/policy -count=1
scripts/test.sh changed
```

### Completion condition

No wrapped compound payload or `git config` mutation is admitted as read-only,
and existing valid read-only forms retain their current behavior.

## Batch 2 — Preserve user files and contain pipeline writes

**Findings:** AUD-02, AUD-03

**Primary packages:** `internal/tools/native`, `internal/pipeline`

Use two independently reviewable commits or pull requests within this batch:

### 2A — Atomic native file commits

- Add failure-injection coverage around whole-file and patch/edit writes.
- Write to a same-directory temporary file, check write/sync/close errors,
  preserve mode, and atomically rename.
- Preserve original-content backup evidence before the destructive commit.
- Verify that a failed write leaves the original bytes and rollback path intact.

```sh
go test ./internal/tools/native -count=1
```

### 2B — Pipeline path containment

- Reject absolute and parent-escaping `marshal.file` and `marshal.patch` paths.
- Check symlink-resolved containment during preflight and immediately before
  mutation.
- Keep valid normalized workspace-relative paths working.

```sh
go test ./internal/pipeline -count=1
```

Land 2B before Batch 4 so pipeline path validation and controller changes are
reviewed against a stable base. The native-file and pipeline changes do not need
to share an implementation helper unless the existing architecture already
provides one.

### Completion condition

A failed native write cannot truncate away the only copy of a file, and no
plan-supplied path can mutate a location outside the pipeline worktree.

## Batch 3 — Make hook cancellation bound the whole process tree

**Finding:** AUD-05

**Primary package:** `internal/hooks`

### Scope

- Add a regression test with a background descendant that inherits stdout.
- Run each hook in its own process group and terminate the group on timeout or
  cancellation.
- Make stdout/stderr draining cancellation-aware, or close the pipes as part of
  termination.
- Preserve the existing fail-open policy and normal output/exit handling.

### Tests and gate

Assert bounded return with a generous upper bound rather than a fragile exact
wall-clock assertion. Verify descendant cleanup, ordinary successful hooks,
nonzero exits, and cancellation before startup.

```sh
go test ./internal/hooks -count=1
scripts/test.sh changed
```

### Completion condition

The existing 10 ms-timeout scenario no longer waits for a two-second descendant,
and a persistent descendant cannot hold the hook open indefinitely.

## Batch 4 — Honor pipeline dependencies and recover stale runs

**Findings:** AUD-06, AUD-07

**Primary package:** `internal/pipeline`

**Dependency:** Land after Batch 2B.

### Scope

- Schedule only tasks whose dependencies are complete, preserving file order
  among ready tasks.
- Keep unknown-dependency and cycle rejection intact.
- Add safe same-run stale-lock detection using host/PID liveness or an explicit
  recovery decision.
- Take over stale locks atomically without displacing a demonstrably live owner.
- Preserve resume behavior for already completed tasks.

### Why these belong together

Both fixes affect the controller's decision about whether and how a run may
proceed. Their tests should share crash/resume fixtures so dependency readiness
and lock recovery cannot be “fixed” in isolation while breaking the other path.

### Tests and gate

Cover reversed dependency order, multiple ready tasks, partial resume, stale
same-run and different-run locks, live owners, ambiguous liveness, and concurrent
takeover attempts.

```sh
go test ./internal/pipeline -count=1
scripts/test.sh changed
```

### Completion condition

A valid plan executes in dependency order, and a crashed run can resume without
manual lock deletion while a live run remains protected.

## Batch 5 — Keep daemon-backed TUI actions asynchronous

**Finding:** AUD-10

**Primary packages:** `internal/app/tui`, `internal/acpclient/rpc`

**Dependency:** Land before Batch 8, which touches some of the same interaction
surfaces.

### Scope

- Move browse inspect/copy/open detail retrieval onto the existing asynchronous
  detail-fetch or call-queue path.
- Move settings persistence off the Bubble Tea update goroutine.
- Replace `context.Background()` on these paths with bounded, model-owned
  contexts.
- Show loading/saving state, apply each result once, and surface failures.
- Preserve the client seam: the TUI must not acquire routing, policy, or prompt
  logic to implement the fix.

### Tests and gate

Use blocking fake sessions to prove that rendering, input, resize, and Ctrl+C
continue while an operation is outstanding. Cover success, error, cancellation,
stale responses, and repeated actions.

```sh
go test ./internal/app/tui/... ./internal/acpclient/rpc/... -count=1
scripts/test.sh changed
```

### Completion condition

A slow or unresponsive daemon produces visible pending/error state rather than
freezing the TUI update loop.

## Batch 6 — Distinguish clean stream completion from truncation

**Finding:** AUD-08

**Primary packages:** `internal/llm/provider`, `internal/agent`

### Decision gate

Before editing the adapters, record the compatibility contract for bare EOF.
The recommended default is to accept EOF after an explicit protocol terminal
marker or finish reason and to surface an interrupted result otherwise. If a
specific legacy provider requires bare-EOF compatibility, make that exception
explicit and tested rather than applying it uniformly.

Do not silently delete `TestChatStreamingCleanCloseSynthesizesDone`; update it
only as part of the documented protocol decision.

### Scope

- Apply the chosen completion contract to OpenAI-compatible, Anthropic,
  OpenAI-compatible Responses, and Ollama-native streaming.
- Ensure the agent does not convert an interrupted stream into a successful
  turn.
- Define handling for partial tool arguments so repair cannot disguise a
  truncated protocol stream as complete.

### Tests and gate

For every adapter, cover explicit terminal completion, bare EOF, EOF during tool
arguments, transport errors, and agent-visible interruption.

```sh
go test ./internal/llm/provider ./internal/agent -count=1
scripts/test.sh changed
```

### Completion condition

Clean protocol completion remains compatible, while premature EOF is visible as
an interrupted or truncated turn rather than silent success.

## Batch 7 — Preserve rewind state and valid index content

**Findings:** AUD-09, AUD-11

**Primary packages:** `internal/app/session`, `internal/db`, `internal/index`

These are independent fixes that can share a persistence-correctness batch with
separate commits:

### 7A — Rewind persistence

- Distinguish an explicitly stored zero leaf from an uninitialized `NULL` leaf.
- Round-trip zero, `NULL`, and nonzero values through the real database path.

```sh
go test ./internal/app/session ./internal/db -count=1
```

### 7B — Index purge behavior

- Mark scanned paths as present before skipping unreadable content.
- Purge only paths absent from the scan.
- Cover transient read failure, genuine deletion, and later successful reindex.

```sh
go test ./internal/index -count=1
scripts/test.sh changed
```

### Completion condition

A first-turn rewind survives reload, and a transient read error no longer
removes previously valid semantic-index entries.

## Batch 8 — Make interactions safe and truthful

**Findings:** AUD-12, AUD-13, AUD-14

**Primary packages:** `internal/app/tui`, `internal/commands`

**Dependency:** Land after Batch 5.

Split this into two reviews if the settings change becomes substantial:

### 8A — Command feedback and accurate help

- Preserve busy refusals for `/new`, `/clear`, `/swarm`, `/sdd`, and `/review`,
  but explain why the command did not run.
- Restore argument-heavy drafts where practical.
- Make Ctrl+S behavior honest when `mouse_capture=false`.
- Distinguish daemon detach from quit/termination in help text.

### 8B — Settings deletion safety

- Add a two-press deletion confirmation and visible `d delete` hints.
- Reconcile selection by stable row identity rather than position alone.
- Test first-press arming, confirmation, cancellation/timeout, middle-row
  deletion, and refresh behavior.

### Tests and gate

```sh
go test ./internal/app/tui/... ./internal/commands -count=1
scripts/test.sh changed
```

### Completion condition

No affected command disappears silently, deletion requires deliberate
confirmation, and mouse/quit messaging matches the active mode.

## Parallelization and dependency notes

Parallel work is optional; the sequential order above is the lowest-conflict
route. If capacity is available, these units have disjoint primary packages and
can be developed independently in separate worktrees:

- Batch 1 (shell policy)
- Batch 2A (native file writes)
- Batch 3 (hooks)
- Batch 6 (providers/agent), after its completion-contract decision
- Batch 7B (index)

Observe these constraints:

- **Batch 2B before Batch 4:** both touch `internal/pipeline`.
- **Batch 5 before Batch 8:** both touch TUI interaction surfaces, especially
  settings.
- **Batch 7A and 7B are independent:** they may be separate pull requests even
  if tracked as one batch.
- Do not treat disjoint files in one package as automatically safe for concurrent
  edits in a shared worktree; each concurrent unit needs its own isolated build
  and test scope.

## Validation strategy

### Per batch

1. Add the regression test and confirm it fails for the audited reason.
2. Implement the smallest fix that satisfies the finding's completion condition.
3. Run the batch's package gate.
4. Run `scripts/test.sh changed` to include dependents.
5. Record any intentionally changed compatibility behavior in the relevant docs.

### Final gate after the full remediation set

```sh
scripts/test.sh full
go vet ./...
gofmt -l .
CGO_ENABLED=1 go build ./cmd/marshal
USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run TestScripted -timeout 5m
```

`gofmt -l .` should print nothing. The usability command intentionally follows
the binary build. No web UI work is implied by this plan; if a future fix changes
a shared web contract, that becomes a separate scope decision.

## Tracking checklist

- [ ] Batch 1 — AUD-01, AUD-04: shell policy fail-closed
- [ ] Batch 2A — AUD-02: atomic native file commits
- [ ] Batch 2B — AUD-03: pipeline path containment
- [ ] Batch 3 — AUD-05: hook process-tree cancellation
- [ ] Batch 4 — AUD-06, AUD-07: pipeline scheduling and recovery
- [ ] Batch 5 — AUD-10: nonblocking daemon-backed TUI actions
- [ ] Batch 6 — AUD-08: streaming completion integrity
- [ ] Batch 7A — AUD-09: rewind persistence
- [ ] Batch 7B — AUD-11: index purge behavior
- [ ] Batch 8A — AUD-12, AUD-14: command feedback and accurate help
- [ ] Batch 8B — AUD-13: settings deletion safety
- [ ] Final full-project validation completed
