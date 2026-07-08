# Agent Loop Reliability — Endless Tool-Use Without Decisions

## Symptom

The Marshal agent loops endlessly, repeatedly using tools (or emitting empty/idle responses) without ever producing a final answer or making a change. Stall detection and the finalize (salvage) path fail to arrest the loop.

## Root-cause diagnosis (verified against source)

Three independent causes, most-likely first:

### 1. Unbounded empty-response loop — `internal/agent/runner.go:268-278` (CRITICAL)

In the `NativeTools` branch, when the model returns **no tool calls and empty text**, the runner appends a "Call a tool or give a final answer." system message and `continue`s **without incrementing `iteration`**:

```go
if r.NativeTools {
    if len(res.ToolCalls) == 0 {
        if strings.TrimSpace(res.Text) == "" {
            messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: "Call a tool or give a final answer."})
            continue   // ← iteration NOT incremented; no stall check; no finalize pressure
        }
        ...return  // happy path
    }
    iteration++
    ...continue
}
```

The loop guard `if iteration >= r.MaxToolIterations { break }` therefore never fires. A weak local model that returns empty text (common when confused by a tool result) loops **forever** — `iteration` stays 0, the finalize-pressure threshold (`MaxToolIterations - iteration <= 2`) is never reached, and `maybeFinalizeOnStall` is never invoked on this branch. No test covers this path.

### 2. Stall detection is blind to non-tool responses — `progress.go` + `runner.go`

`progressTracker.record()` is only called inside `executeToolCall` (runner.go:694, 779, 794). The empty-response `continue`, the `ActionAskUser` branch, and the parse-failure/correction branches never record anything. `assess()` needs 3 exact repeats or 3 duplicate-churn calls to escalate; interleaved empty responses extend the real wall-clock time-to-detection arbitrarily and the soft-stall nudge / hard-stall finalize only fire after a tool execution, never after an idle turn.

### 3. `ActionAskUser` non-progress loop — `runner.go:376`

The `ActionAskUser` case appends the user answer and `continue`s without incrementing `iteration`. A model that repeatedly asks (and the user declines) can loop ask→decline→ask unbounded. Bounded in practice by user patience, but it is another non-progressing `continue` the budget doesn't count.

### 4. (Minor, contributing) Compaction can drop the facts the model needs — `compact.go`

`compactMessages` only shrinks tool-result messages. When the loop is long, compaction can remove the very tool results the model needed to answer, prompting re-reads (more tool calls) that defeat the finalize-pressure intent. Not a primary cause but it amplifies 1 and 2.

## Proposed fixes

All edits are in `internal/agent/`. Each fix is independently shippable; together they close the infinite loop.

### Fix A — Count every turn toward the budget (closes #1, #3)

In `RunTask`'s main `for` loop, increment `iteration` on **every** model response that does not produce a terminal result, not only on tool calls. Specifically:

- NativeTools empty-response branch (runner.go:271-273): increment `iteration` before `continue`.
- NativeTools empty-response branch: after a small threshold of consecutive empty responses (e.g. 2), call `finalize` with a new `reasonEmpty` rather than re-prompting, so a model that goes silent is salvaged instead of spun.
- Non-native `ActionAskUser` branch (runner.go:376-390): increment `iteration` before `continue` so repeated asks count against the budget.
- Parse-failure branch already increments via `consecutiveParseFailures` and breaks at `maxConsecutiveParseFailures` (3); leave as-is.

Net effect: `MaxToolIterations` becomes a true *turn* budget (rename in docs/comments, keep the field name for back-compat), and every non-terminal response consumes one slot.

### Fix B — Make stall detection see idle turns (closes #2)

Add a `recordIdle()` method on `progressTracker` that appends a synthetic `catOther` entry with a sentinel name (e.g. `"<idle>"`) and the response's finish reason. Call it on:
- the NativeTools empty-response branch,
- the non-native parse-failure branch (already tracked separately, but record for churn visibility),
- the `ActionAskUser` branch when the user declines.

Adjust `assess()` so a run of `N` consecutive idle entries (e.g. 3) escalates directly to `assessHardStall`, triggering `finalize` immediately. Keep `exactRepeat`/`duplicateChurn` semantics for real tool calls unchanged. This makes the hard-stall path fire on "model goes silent" as well as "model repeats calls".

### Fix C — Finalize on sustained silence (defense-in-depth with A+B)

In `RunTask`, after Fix A, when `consecutiveEmpty >= 2` (new counter), short-circuit to `finalize(ctx, ..., reasonEmpty)` and return, mirroring the `reasonMalformed` exit. Add `reasonEmpty finalizeReason = "empty"` and a matching salvage preamble in `synthesizeFallback` ("The model stopped producing output. Here is the best summary I can construct from the work completed so far.").

### Fix D — Guard the finalize-pressure + compaction interaction (closes #4, minor)

Two small changes:
- In `compactMessages`, never compact the most recent `keepRecent` tool results even when over budget — instead drop the *oldest* non-tool assistant/system messages first. Currently it only ever shrinks tool results, which is the wrong thing to lose when the model needs facts to answer.
- Ensure the finalize-pressure system message is treated as non-compactable (it already is, since it's not a tool-result message, but verify the `cutoff` math in `compactMessages` doesn't sweep it when `keepRecent` is small).

## Task list (implementation order)

1. **Add `recordIdle` + idle-aware `assess`** in `progress.go`:
   - `func (t *progressTracker) recordIdle(reason string)` appending `{name: "<idle>", cat: catOther, novel: false}`.
   - In `assess()`: if the last 3 entries are all `<idle>`, return `assessHardStall`.
   - Unit tests in `progress_test.go`: idle-then-hard-stall, idle-interleaved-with-tools.

2. **Add `reasonEmpty`** in `finalize.go`:
   - `const reasonEmpty finalizeReason = "empty"`.
   - `synthesizeFallback` case for `reasonEmpty`.
   - Test in `finalize_test.go`.

3. **Wire idle recording + turn counting in `runner.go`**:
   - NativeTools empty-response branch: increment `iteration`, `recordIdle`, bump `consecutiveEmpty`; if `consecutiveEmpty >= 2`, `return r.finalize(ctx, ..., reasonEmpty)`.
   - `ActionAskUser` branch: increment `iteration`, `recordIdle` when user declines.
   - Reset `consecutiveEmpty = 0` on any non-empty response.
   - Call `maybeFinalizeOnStall` after recording idle too (so hard-stall fires).
   - Update the `finalizePressureThreshold` comment to note `iteration` now counts all turns.

4. **Compaction fix in `compact.go`**:
   - Change the compaction order: when over budget, drop oldest non-tool assistant/system messages *first*, only compacting tool results as a last resort; never compact the most recent `keepRecent` tool results.
   - Tests in `compact_test.go` (new or existing): verify a long tool-heavy transcript keeps recent results and drops old prose.

5. **Runner integration tests in `runner_test.go`**:
   - Model returns empty text repeatedly → assert `finalize` is called with `reasonEmpty` and the task completes (not loops).
   - Model returns empty text 2× then a final answer → assert the answer wins.
   - Model alternates empty + duplicate tool call → assert hard-stall finalize fires within budget.
   - Repeated `ask_user` with declined answers → assert iteration counts and budget eventually finalizes.

## Validation

- `go build ./cmd/marshal` and `go vet ./...` clean (the one pre-existing `assignment copies lock value` vet warning at `app.go:573` is unrelated).
- `go test ./internal/agent/...` green, including the new tests above.
- Manual: run `marshal` against a goal that previously hung (e.g. a vague "improve this" with no clear target) and confirm it now terminates with a salvaged summary instead of looping.

## Out of scope

- Changing `MaxToolIterations` default (16) — leave as-is; the bug is that the budget wasn't being consumed, not that it's too high.
- Reworking the JSON action envelope or native tool-calling protocol.
- Telemetry/eval dashboards (already shipped).

## Note for implementer

Source edits required (`internal/agent/{runner,progress,finalize,compact}.go` + tests). Switch to an implementation-capable agent to execute.