# Native Tool Calling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add provider-native function/tool-calling as an opt-in coexisting alternative to Marshal's text-based JSON action protocol, selected per preset via `tool_calling = "native"`.

**Architecture:** The provider gains the OpenAI-compatible `tools[]` request field and `tool_calls[]` response parsing (streaming fragments accumulated in the provider, surfaced complete on the `Done` event). The runner keeps its single `RunTask` loop and branches only at two seams — decode (text `ParseAction` vs. reading `event.ToolCalls`) and tool-result formatting (text message vs. `role:tool` message). All reliability machinery (planning, approval, write-gate, stall detection, compaction, salvage/finalize, telemetry) is reused unchanged. Native is strictly opt-in and capability-gated, with a fallback ladder `native → json_schema → json_object → nil`.

**Tech Stack:** Go stdlib only. No new dependencies.

## Locked Design Decisions

1. **Coexistence, opt-in.** New preset value `tool_calling = "native"`. The existing text/JSON protocol remains the default and stays fully functional.
2. **Non-tool action mapping in native mode:**
   - Final answer = assistant message with content and **zero** tool calls (`finish_reason: stop`). Runner treats it exactly like `ActionFinal` today.
   - `patch` = a normal `file.write_patch` tool call (already a registered tool with schema `{patch: string}`). No special action path in native mode.
   - `ask_user` = a synthetic tool (name `ask_user`, schema `{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`) injected into the tool list **only for the general role**. The runner intercepts a tool call named `ask_user` and routes it to the existing `requestAnswer` path. Swarm roles never receive the tool (cleaner than today's text correction).
3. **Streaming:** provider accumulates tool-call fragments by `index`, delivers a complete `[]ToolCall` on the `Done` event. Content/reasoning deltas keep streaming live.
4. **Runner:** one loop, branch at decode + result-formatting. `executeToolCall`/`executeActions`/policy/approval/audit/tracker reused unchanged. Tool-result builder chooses text vs `role:tool` by presence of a `tool_call_id`. `compactMessages` also recognizes `role:tool` messages.
5. **Parallel calls:** native multi-call batches execute **sequentially** in returned order for v1; every `tool_call_id` gets a `role:tool` reply. Write-gate still applies. (Parallel-read optimization deferred.)
6. **Activation/fallback:** `defaultCapabilities()` keeps `ToolCalling: false` — native activates only when a provider is explicitly configured with `ToolCalling: true`. `tool_calling = "native"` resolves to native tools if `caps.ToolCalling`, else falls back `json_schema` (if `StructuredOutput`) → `json_object` (if `JSONMode`) → nil.
7. **Prompts:** `BuildSystemPrompt` takes a mode flag; in native mode it drops the JSON-action-envelope scaffolding (`baseOutputFormat` + envelope examples) while keeping behavioral base rules (concise, plan-first, ask-when-ambiguous, etc.).
8. **Telemetry:** native mode never increments `TurnMetrics.ParseFailures`; all other metrics unchanged.
9. **ToolChoice:** omit it (default provider "auto") for v1 to maximize local-server compatibility; keep the field for future use but leave empty.

## Global Constraints

- Work on branch `native-tool-calling` (from `main`).
- Build/test with `CGO_ENABLED=1 go test ./...`; `gofmt` clean; `go vet` clean except the documented pre-existing `internal/app/app.go` mutex-copy warning.
- Back-compat: with no `tool_calling` change and `ToolCalling: false` (the default), every existing request must serialize byte-identically — no `tools` key, no behavior change. Existing tests must stay green untouched except where a new field is intentionally threaded.
- The runner must answer every `tool_call_id` in an assistant message with exactly one `role:tool` message before the next model turn.
- Never fail runner construction over a decoding/tool preference — always degrade down the ladder.

---

### Task 1: Schema wire types

**Files:**
- Modify: `internal/llm/schema/chat.go` (extend `ChatMessage`, `ChatRequest`; add `ToolCall`, `ToolDefinition`)
- Modify: `internal/llm/schema/event.go` (add `ToolCalls` to `ChatEvent`)
- Test: `internal/llm/schema/` (a small table test if the package has one; otherwise assertions live in Task 2's provider tests)

**Produces (later tasks depend on these):**
- `schema.ToolCall{ID string; Name string; Args json.RawMessage}`
- `schema.ToolDefinition{Name string; Description string; Parameters json.RawMessage}`
- `ChatMessage` gains `ToolCalls []ToolCall` (assistant requests) and `ToolCallID string` (tool-result messages).
- `ChatRequest` gains `Tools []ToolDefinition` and `ToolChoice string`.
- `ChatEvent` gains `ToolCalls []ToolCall` (populated only on `Done`).

- [ ] **Step 1:** Add the types and fields. Keep all new fields optional so zero-value serialization is unchanged (see Task 2 for wire `omitempty`).
- [ ] **Step 2:** `CGO_ENABLED=1 go test -count=1 ./internal/llm/...` stays green; `gofmt -w internal/llm/schema`; `go vet ./internal/llm/...`.
- [ ] **Step 3:** Commit: `feat(schema): tool-calling fields on chat message, request, and event`.

---

### Task 2: Provider — tools request + tool_calls parsing

**Files:**
- Modify: `internal/llm/provider/openai_compatible_wire.go` (wire structs for `tools`, `tool_calls`, `role:tool` messages)
- Modify: `internal/llm/provider/openai_compatible.go` (`buildChatRequestBody` maps `req.Tools`/`req.ToolChoice` and tool-bearing messages; `streamChatEvents`/`readChatResponse` accumulate and surface tool calls)
- Test: `internal/llm/provider/openai_compatible_test.go`

**Interfaces:**
- Consumes: `schema.ToolCall`, `schema.ToolDefinition`, extended `ChatMessage`/`ChatRequest`/`ChatEvent` (Task 1).

**Wire shapes (must match OpenAI):**
- Request `tools`: `[{"type":"function","function":{"name":...,"description":...,"parameters":{...}}}]`. Omit the key entirely when `len(req.Tools)==0`.
- Assistant message with calls: `{"role":"assistant","content":"...","tool_calls":[{"id":...,"type":"function","function":{"name":...,"arguments":"<json string>"}}]}`. `arguments` is a JSON **string**, not an object.
- Tool result message: `{"role":"tool","tool_call_id":...,"content":"..."}`.
- Streaming delta: `choices[0].delta.tool_calls[]` entries carry `index`; first fragment has `id` + `function.name`, later fragments append `function.arguments` string pieces. `finish_reason:"tool_calls"` on completion.

- [ ] **Step 1: Failing wire tests.** Add to `openai_compatible_test.go`:
  - Request with `Tools` set serializes the `tools` array in the exact shape above; request with no tools omits the key (byte-identical to today for a baseline request).
  - A `role:tool` message and an assistant-with-`tool_calls` message serialize correctly (`arguments` as a JSON string).
  - A streaming response whose deltas split `tool_calls[0].function.arguments` across chunks yields, on the `Done` event, one complete `ToolCall` with reassembled `Args` and correct `ID`/`Name`. Include a two-call batch (`index` 0 and 1) to prove index handling.
  - A non-streaming response with `message.tool_calls` yields the same complete `[]ToolCall` on `Done`.
- [ ] **Step 2:** Run to confirm red.
- [ ] **Step 3: Implement.** Add wire structs; map request tools/messages; accumulate streaming fragments in a per-index buffer inside `streamChatEvents`; attach `ToolCalls` to the terminal `Done` event in both stream and non-stream paths. Content/reasoning delta handling unchanged.
- [ ] **Step 4:** `CGO_ENABLED=1 go test -count=1 ./internal/llm/...`; `gofmt -w internal/llm/provider`; `go vet ./internal/llm/...`.
- [ ] **Step 5:** Commit: `feat(provider): send tools and parse streamed tool_calls on the OpenAI-compatible wire`.

---

### Task 3: Prompts — native mode scaffolding

**Files:**
- Modify: `internal/agent/prompts.go` (`BuildSystemPrompt` gains a mode flag; skip `baseOutputFormat`/envelope examples in native mode)
- Test: `internal/agent/prompts_test.go`

**Notes:**
- Every current `BuildSystemPrompt(...)` call site is in `runner.go`. Add the mode parameter (e.g. a `nativeTools bool` or small enum) and update call sites in Task 4.
- Native mode keeps behavioral base rules and role addenda; it removes only the JSON-action-envelope instructions and examples.

- [ ] **Step 1: Failing tests.** Assert: native-mode prompt does **not** contain the JSON-envelope markers (e.g. the `"action"`/`"rationale"` format block) but **does** contain the behavioral rules and the role addendum. Non-native prompt is unchanged (existing snapshot assertions stay green).
- [ ] **Step 2:** Run to confirm red.
- [ ] **Step 3: Implement** the branch.
- [ ] **Step 4:** `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`; `gofmt`; `vet`.
- [ ] **Step 5:** Commit: `feat(agent): native-mode system prompt without JSON-envelope scaffolding`.

---

### Task 4: Runner — native decode, dispatch, and tool-result formatting

**Files:**
- Modify: `internal/agent/runner.go` (`NativeTools` field; `chatOnce`/`chatWithRetry` return a result struct; tool-definition builder; dispatch branch; result formatting)
- Modify: `internal/agent/compact.go` (recognize `role:tool` messages as compaction candidates)
- Modify: `internal/agent/runner_test.go` (`scriptedProvider` emits tool calls)
- Test: `internal/agent/runner_test.go`, `internal/agent/compact_test.go`

**Design:**
- `chatOnce` returns `chatResult{Text string; ToolCalls []schema.ToolCall; FinishReason string}` instead of a bare string; `chatWithRetry` mirrors it. `finalize` reads `.Text` (it only wants prose; it must send **no** tools so the salvage turn always yields text).
- New `Runner.NativeTools bool`. When set, `chatOnce` populates `req.Tools` from a builder:
  - `buildToolDefinitions()` maps `Registry.List()` → `[]schema.ToolDefinition` (`Name`, `Description`, `Parameters = tool.Schema`; default `{"type":"object"}` when a tool's schema is empty).
  - Append the synthetic `ask_user` tool iff `r.role() == RoleGeneral`.
  - `ResponseFormat` is omitted in native mode.
- **Decode branch** after chat:
  - Native: if `len(res.ToolCalls) == 0` and `res.Text != ""` → terminal answer (set `task.Summary`, `AddMessageFinal`, return), exactly as `ActionFinal`. If both empty → append a bounded nudge ("call a tool or give a final answer") and continue. If tool calls present → append the assistant message (content + `ToolCalls`) to `messages`, then execute each call sequentially.
  - Non-native: unchanged `ParseAction` path (including parse-failure escalation).
- **Per native tool call:** if name == `ask_user` (general role) → route to `requestAnswer`, then feed a `role:tool` message (with that call's `tool_call_id`) carrying the user's answer / declined marker. Otherwise build a `ModelAction{Type: ActionToolCall, Tool: name, Args: args}` carrying the `tool_call_id` and reuse `executeToolCall`. Every call produces exactly one `role:tool` reply.
- **Result formatting:** the tool-result message builder emits a `role:tool` message with `ToolCallID` when the call has an ID (native), else the existing text message (JSON mode). Simplest: thread the `tool_call_id` through `executeToolCall` and choose the builder at the tail.
- **Telemetry:** do not touch `ParseFailures` on the native path; existing `countToolCall`/tracker/stall calls remain.
- **Compaction:** `compactMessages` treats `role:tool` messages the same way it treats text tool-result messages (candidates for shrinking, preserving `ToolCallID`).

- [ ] **Step 1: Failing tests.** Extend `scriptedProvider` to script tool calls per turn (e.g. a `toolCalls [][]schema.ToolCall` field surfaced on the `Done` event). Then:
  - Native turn: model returns a `file.read` tool call, then a text-only final answer → task completes, one `role:tool` message with the correct `tool_call_id` was fed back, `ParseFailures == 0`, `ToolCalls == 1`.
  - Native multi-call batch (two calls in one response) executes sequentially and feeds back two `role:tool` messages, one per id.
  - `ask_user` tool call from the general role routes to `requestAnswer` and feeds the answer back as a `role:tool` message; from a swarm role the tool is absent from the definitions (assert `buildToolDefinitions` for a non-general role omits `ask_user`).
  - Unknown tool name in a native call still yields a `role:tool` error message answering that id (turn continues).
  - `compact_test.go`: a `role:tool` message over budget is compacted like a text tool result.
- [ ] **Step 2:** Run to confirm red.
- [ ] **Step 3: Implement** the result struct, builder, dispatch branch, result formatting, and compaction change.
- [ ] **Step 4:** `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`; `gofmt`; `vet`.
- [ ] **Step 5:** Commit: `feat(agent): native tool-call decode, dispatch, and role:tool feedback in the runner`.

---

### Task 5: App wiring — decode-config resolver and preset opt-in

**Files:**
- Modify: `internal/app/app.go` (resolve `tool_calling` into `{Native bool; ResponseFormat *schema.ResponseFormat}`; set `runner.NativeTools` + `runner.ResponseFormat` in `buildAgentRunner` and the swarm factory)
- Modify: `docs/09-configuration-examples.md` (document `tool_calling = "native"` and the fallback ladder)
- Test: `internal/app/response_format_test.go` (extend) or a new `internal/app/decoding_test.go`

**Design:**
- Replace/extend the existing `actionResponseFormat(toolCalling, caps)` with a resolver returning both the native flag and the response format:
  - `"native"`: `caps.ToolCalling` → `{Native:true}`; else `caps.StructuredOutput` → `{ResponseFormat: json_schema}`; else `caps.JSONMode` → `{ResponseFormat: json_object}`; else `{}`.
  - `"json_schema"` / `"json"` / `""`: unchanged (Native false).
- Both runner constructors set `runner.NativeTools = cfg.Native` and `runner.ResponseFormat = cfg.ResponseFormat`.

- [ ] **Step 1: Failing test** covering the full ladder table, including `native` with full caps (Native true, no ResponseFormat), `native` degrading to json_schema, to json_object, and to nil; plus the unchanged `json`/`json_schema`/`""` rows.
- [ ] **Step 2:** Run to confirm red.
- [ ] **Step 3: Implement** the resolver and wire both constructors; document the preset value.
- [ ] **Step 4:** `CGO_ENABLED=1 go test -count=1 ./internal/app/... ./internal/agent/...`; `gofmt -w internal/app`; `go vet ./internal/app/... 2>&1 | grep -v "copies lock value" || true`.
- [ ] **Step 5:** Commit: `feat(app): native tool_calling preset with capability fallback ladder`.

---

### Task 6: Eval + end-to-end coverage

**Files:**
- Modify: `internal/agent/eval_scenarios_test.go` (native-mode rows, using the tool-call-capable `scriptedProvider`)
- Modify: `internal/agent/metrics.go` (comment: `ParseFailures` is always 0 in native mode)
- Test: as above

- [ ] **Step 1:** Add native-mode eval rows mirroring the existing behavioral scenarios (research turn answers after reads; tool error recovers to an answer) but driven by scripted tool calls. Assert `ParseFailures == 0` and outcome/tool-call counts.
- [ ] **Step 2:** `CGO_ENABLED=1 go test -count=1 ./...` fully green; `gofmt`; `vet`.
- [ ] **Step 3:** Commit: `test(agent): native-mode eval scenarios and telemetry`.

---

## Validation

- `CGO_ENABLED=1 go test -count=1 ./...` green; `gofmt -l .` empty; `go vet ./...` clean except the known `app.go` mutex-copy warning.
- Back-compat proof: a baseline chat request with no tools serializes byte-identically to pre-change output (asserted in Task 2).
- **Live check (the real proof):** configure a provider with `ToolCalling: true` and a preset with `tool_calling = "native"` against llama.cpp server or Ollama's OpenAI endpoint. Run several coding turns; confirm tool calls execute, results feed back, and the turn ends on a text answer. Compare `turn_metrics.parse_failures` and `iterations` between a `native` preset and a `json_schema` preset on the same tasks (the telemetry table exists to justify/measure this).

## Risks & Mitigations

- **Flaky local tool-calling support.** Mitigated: opt-in only (`ToolCalling: false` default), capability-gated, full fallback ladder.
- **Unanswered `tool_call_id` breaks the next turn.** Mitigated: sequential execution answers every id with exactly one `role:tool` message, including error/unknown-tool cases.
- **Streaming fragment quirks across servers** (id only on first fragment, argument splitting, index gaps). Mitigated: dedicated wire tests including multi-call/index cases; non-stream path covered too.
- **Compaction marker divergence** (text vs `role:tool`). Mitigated: `compactMessages` recognizes both; test added.
- **Prompt contradiction** (JSON-envelope instructions + native tools). Mitigated: `BuildSystemPrompt` drops envelope scaffolding in native mode; test added.

## Out of Scope (v1)

- Parallel execution of native read-only batches (sequential only for now).
- Provider-native tool-choice forcing / `tool_choice` targeting a specific function.
- Anthropic-style tool-use wire format (only OpenAI-compatible providers exist today).
- Per-`rationale` capture per tool call (native has no per-call rationale field; pre-call content serves as narration).
