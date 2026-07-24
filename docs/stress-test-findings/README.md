# Marshal Stress-Test Findings

This document captures observations, root causes, and proposed improvements discovered while stress-testing Marshal's agent loop and ACP integration.

## 1. Parallel native tool use

**Observation**
Marshal can already execute multiple non-native read-only actions in parallel (`internal/agent/execute.go:executeActions`). The native tool-calling path (`executeNativeToolCalls`) runs tool calls sequentially, even when they are independent read-only calls.

**Why it matters**
Long-horizon autonomous coding tasks often issue several independent `file.read`, `repo.search`, or `symbols.find` calls in one turn. Running them serially increases wall-clock latency and token cost (the model waits for each round-trip before seeing results). Parallel execution would improve throughput without changing the agent's reasoning model.

**Proposed change**
Extend `executeNativeToolCalls` with the same two-phase pattern used by `executeActions`:
- Run `ask_user` / `question.ask` serially (they share `State.PendingQuestion`).
- Run read-only tools concurrently, bounded by `MaxParallelActions`.
- Keep write/destructive tools under policy/approval, but allow approved independent writes to run concurrently.
- Return one `tool` role message per tool call so the conversation transcript stays well-formed.

**Related code**
- `internal/agent/execute.go:executeNativeToolCalls` (sequential today)
- `internal/agent/execute.go:executeActions` (existing parallel non-native implementation)
- `internal/agent/runner.go:MaxParallelActions`

---

## 2. Native tool-call argument concatenation (minimax-m3 via Ollama Cloud)

**Observation**
When driving Marshal with `minimax-m3` through Ollama Cloud's native tool-calling adapter, the model sometimes emits a single tool call whose `function.arguments` string contains multiple concatenated JSON objects, e.g.:

```json
{"path":"internal/app/config/types.go"}{"path":"internal/app/config/defaults.go"}
```

Ollama Cloud returns HTTP 400 `{"error":{"message":"invalid tool call arguments"}}`, and Marshal surfaces the failure as a provider error.

**What we tried**
- Added `"additionalProperties": false` to every native tool schema.
- Added an explicit system-prompt rule that each tool call's arguments must be a single valid JSON object with no extra keys.

Neither prevented the bug under larger/complex prompts. The issue is in the model/provider adapter, not in Marshal's request formatting.

**Workaround used**
Switching the model preset from `tool_calling = "native"` to `tool_calling = "json"` avoided the provider-side tool-calling path entirely. Marshal then parses the tool calls from the JSON response itself.

**Fix implemented**
`internal/llm/provider/toolcall_repair.go` now post-processes every native tool-call response. If a single `function.arguments` string is not valid JSON but is a concatenation of valid JSON objects, the provider splits it into separate `schema.ToolCall` entries with synthetic `tool_call_id` suffixes before surfacing them to the runner. This lets native mode continue working against adapters that concatenate objects, without requiring a second provider request.

**Graceful degradation still recommended**
If repair fails repeatedly (or the adapter produces other unrecoverable garbage), the runner should be able to fall back to `tool_calling = "json"` for the rest of the session. That broader degradation path is not yet implemented.

**Related code**
- `internal/llm/provider/toolcall_repair.go` — heuristic repair
- `internal/llm/provider/openai_compatible.go` — wired into both streaming and non-streaming tool-call parsing
- `internal/agent/prompts.go` — native tool-call JSON rule
- `internal/tools/native/*.go` — tool schemas with `additionalProperties: false`

---

## 3. Import cycle from command-runner seam

**Observation**
Task 3 of the structured digest provider plan proposed `internal/rollover/filesstate.go` importing `internal/tools/native` for `CommandRunner`. This created a test-only import cycle:

```
internal/tools/native (tests) -> internal/agent
internal/agent/handoff.go -> internal/rollover
internal/rollover/filesstate.go -> internal/tools/native
```

`go test ./internal/tools/native` failed with `import cycle not allowed in test`.

**Fix**
Define a narrow, local `CommandRunner` interface inside `internal/rollover` using plain Go types (`CommandRequest`, `CommandResult`). In `internal/app/app.go`, wrap the real `native.CommandRunner` in a tiny adapter that maps the native request/result types to the rollover ones. This keeps the seam intact without coupling the package graph.

**Related code**
- `internal/rollover/filesstate.go` (`rollover.CommandRunner`)
- `internal/app/app.go` (`rolloverRunnerAdapter`)

---

## 4. Plan test-code defects surface during execution

**Observation**
The structured digest provider plan's `filesstate_test.go` included a test comment claiming duplicate writes would be "deduped by PK", but the SQL used plain `INSERT INTO file_writes(...)` rather than `INSERT OR IGNORE INTO ...`. Running the test as written raised a `UNIQUE constraint failed` error. A one-word fix (`OR IGNORE`) aligned the test with its stated intent.

**Why it matters**
Long, prescriptive plans can contain latent inconsistencies that only show up when executed. A supervising agent should run tests, trust failures, and patch the plan or test rather than assuming the plan is perfect.

**Related code**
- `internal/rollover/filesstate_test.go` (fixed `INSERT OR IGNORE`)

---

## 5. ACP prompt/task boundary strategy

**Observation**
Giving Marshal a single task at a time (one plan task, one fix, one verification command) with explicit "stop and report error/file/line on failure" instructions produced reliable results. Larger prompts that asked for multiple tasks in one turn were more likely to hit the native tool-call concatenation bug or produce incomplete work.

**Recommendation**
For ACP-driven plan execution, prefer sequential single-task prompts over batched multi-task prompts. This reduces model pressure, makes failures attributable to one step, and lets the supervisor retry or redirect quickly.
