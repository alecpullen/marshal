# ACP Stress Test Report — Round 2 (2026-07-24, post-bug-fixes)

Re-ran the ACP stress test after the four bug fixes from
`2026-07-24-acp-stress-test-bug-fixes.md` landed (commits `81ce9dd`
through `0414f78` on `sdd2/context-rollover`). Same setup: throwaway
copy at `/tmp/kilo/marshal-acp-test`, Ollama Cloud `kimi-k2.7-code`,
`tool_calling = "native"`, `auto_approve = true`.

## Fixes verified working

### Bug 1 (nil-runner panic) — FIXED
The `Lookup` closure now returns `false` when `rt.Runner == nil`, logging
the stored provider error. `PromptTurn` returns a `-32000` server error
instead of panicking. No panic observed in any round-2 run.

### Bug 2 (headless trust) — FIXED
`HeadlessResolver` is wired into `acp.Run`. The log confirms:
`"no stored trust for project; granting session trust in headless mode"`.
Project-local config loads correctly in headless ACP without manual
trust-store setup.

### Bug 3 (zoneless timestamp) — FIXED
`flexTime` tolerates zoneless `trusted_at` in the trust store. Not
directly exercised in round 2 (the HeadlessResolver bypasses the store
for un-trusted projects), but the fix is present and unit-tested.

### Bug 4 (tool-name truncation) — FIXED
`normalizeToolName` maps truncated names back to registered tools. The
model's `read` → `file.read` and `write_patch` → `file.write_patch`
calls now resolve. The permission bridge fires: round-2 runs show 3–9
`session/request_permission` calls per turn, all auto-approved. No
"unknown tool" errors.

## New bug surfaced (Bug 5)

### Bug 5: native tool-calling path doesn't document the patch format

**Severity:** blocks all file-editing tasks with native tool-calling.

**Location:** `internal/agent/prompts.go:145` (`nativeOutputFormat`),
`internal/agent/prompts.go:147-168` (`renderRoleAddendum`).

**Symptom:** the agent successfully reads files (via `file.read`) and
attempts patches (via `file.write_patch`), but every patch call fails
with "patch validation failed" or "parse patch error." The agent retries
9+ times, exhausts the tool budget, and gives up without making any
file changes. `git diff --stat` shows zero changes after a full turn.

**Root cause:** when `NativeTools = true`, the system prompt uses
`nativeOutputFormat` (line 145):

```
Use the available native tools when you need repository facts or need
to make changes. When the task is complete, respond with a concise
final answer in normal prose.
```

This is a single sentence with **no patch format documentation**. The
non-native path (`baseOutputFormat`, lines 125-143) includes a full
example of the `<<<<<<< SEARCH / ======= / >>>>>>> REPLACE` block
format. The `renderRoleAddendum` function (line 163) also skips the
per-role patch example when `nativeTools` is true:

```go
if !nativeTools {
    b.WriteString("\n\nExample:\n")
    b.WriteString(r.example)
}
```

The `file.write_patch` tool description (file.go:145) says "Apply a
search/replace patch block format to files in the workspace" but never
shows the syntax. The model (kimi-k2.7-code) has no way to learn the
expected format and produces invalid patches (likely unified diffs or
inline replacements).

**Fix (not applied):** add a `nativePatchFormat` constant documenting
the `<<<<<<< SEARCH / ======= / >>>>>>> REPLACE` syntax with a concrete
example, and include it in the system prompt when `nativeTools` is true
and the `file.write_patch` tool is registered. Alternatively, enrich the
`file.write_patch` tool description with the format example so it
travels with the tool definition to the model.

## What worked in round 2

- **ACP transport:** full lifecycle (`initialize` → `session/new` →
  `session/prompt` → `session/update` → `session/request_permission` →
  `session/close`) with no crashes or protocol errors.
- **HeadlessResolver:** project config loaded automatically, no manual
  trust-store setup needed.
- **Tool-name normalization:** `file.read` and `file.write_patch` calls
  resolved correctly despite kimi truncating the names.
- **Permission bridge:** 3–9 permission requests per turn, all
  auto-approved, all delivered back to the runner. The full
  `request_permission` → `PermissionBridge` → `ResponseChan` → runner
  round-trip works.
- **Agent loop:** the model made real tool calls, received results, and
  iterated. The "near the tool budget" reminder fired correctly at the
  iteration limit.

## What did not work

- **File editing:** zero patches applied. The model couldn't produce
  valid `<<<<<<< SEARCH / ======= / >>>>>>> REPLACE` blocks because the
  format wasn't documented in the native-tool prompt path.
- **Plan execution:** zero of 5 tasks completed (same as round 1, but
  for a different reason — round 1 was the tool-name truncation, round 2
  is the missing patch format docs).

## Recommendation

Fix Bug 5 (add patch format docs to the native tool-calling prompt path),
then re-run the stress test. With Bugs 1-4 fixed and Bug 5 addressed,
the agent should be able to read files, produce valid patches, run
tests, and complete the plan tasks.