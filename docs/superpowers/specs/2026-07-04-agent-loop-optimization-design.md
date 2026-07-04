# Agent Loop Optimization Design

## Background

Live integration tests against a local `qwen3.6-35b-a3b` model (LM Studio) showed three related pain points:

1. **Slow wall-clock responses** — simple tool calls routinely take 10–30 seconds.
2. **Thinking in circles / iteration burn** — the model occasionally emits plain-text plans instead of the required JSON action, produces invalid patch formats, or re-reads files it has already seen, burning through `max_tool_iterations`.
3. **Growing context cost** — every tool result is appended to the transcript verbatim, so later iterations see larger prompts and slower generation.

This spec proposes a phased set of changes to reduce latency, cut wasted iterations, and make the agent loop more robust on local hardware.

## Goals

- Reduce average time-to-answer for simple tasks (read, search, shell) on local models.
- Reduce the rate of `ErrMaxIterationsExceeded` failures caused by looped or malformed tool calls.
- Keep changes backward-compatible with the existing JSON action protocol and provider interface.
- Lay the groundwork for native tool-calling as a future provider capability.

## Non-goals

- Changing the local inference server configuration (quantization, KV cache, context size) — those are operator concerns outside Marshal.
- Removing the existing JSON action protocol.
- Adding full multi-agent parallelism in this milestone.

## Phase 1 — Prompt & Protocol Hardening (quick wins)

### 1.1 Few-shot action examples in the system prompt

Extend the system prompt with one concise example for each action type:

- `answer`
- `tool_call`
- `patch` (showing the exact `<<<<<<< SEARCH / ======= / >>>>>>> REPLACE` format)
- `final`

This directly addresses the patch-format confusion observed in live tests.

### 1.2 Optional structured-output mode

Add a `ResponseFormat *schema.ResponseFormat` field to `schema.ChatRequest` and wire it through `OpenAICompatible`. When a model preset declares `tool_calling = "json"` (or a new `response_format = "json_object"`), send `response_format: {"type":"json_object"}` to the provider if the provider capability flag `StructuredOutput` is true.

If the provider/server rejects it or the capability is false, fall back to the existing parser and markdown-fence tolerance.

### 1.3 Per-request timeout in `chatWithRetry`

Currently `chatOnce` uses the caller’s context with no inner deadline. A hung local model call can block the whole turn. Add a configurable `RequestTimeout` to the provider/request and wrap each `chatOnce` call in `context.WithTimeout`. The runner should expose this via `Runner.RequestTimeout` and default to something reasonable (e.g., 60s for local models).

### 1.4 Skip planning for obvious single-tool tasks

If `Classify(goal)` returns `ClassQuestion` or the goal can be matched against a small allow-list of single-tool patterns (e.g., "read ...", "run ..."), skip the planning phase and go straight to the execution loop. This saves one full model generation per simple request.

## Phase 2 — Smarter Agent Loop

### 2.1 Tool-result cache in `session.State`

Cache the result of read-only tools keyed by `(tool_name, normalized_args)` for the duration of a turn. If the model asks for the same file/search again, return the cached result instantly and append a short note to the transcript instead of re-running the tool.

Scope: in-memory, per-turn only. Do not persist across runs.

### 2.2 Parallel read-only tool calls

Allow the model to request multiple read-only tools in one response. Extend the action protocol to accept an array of actions:

```json
{
  "rationale": "read two files",
  "actions": [
    {"type": "tool_call", "tool": "file.read", "args": {"path": "a.go"}},
    {"type": "tool_call", "tool": "file.read", "args": {"path": "b.go"}}
  ]
}
```

For backward compatibility, still accept the single `"action"` object. When `actions` is present and every entry is read-only, execute them concurrently and feed all results back in one turn.

### 2.3 Loop detection

Track the last N tool calls in the runner. If the same `(tool, args)` pair appears three times without a final answer, append a system message like:

> “You appear to be repeating the same step. Either produce a final answer or ask the user for clarification.”

Count this guidance as a regular iteration so the existing limit still applies, but give the model a nudge instead of silently dying.

### 2.4 Tool-output summarization / truncation

Add a `Summarize` helper for large tool results (long file reads, big `repo.search` outputs, `git diff`). Before appending to the transcript:

- Truncate to a configurable `MaxToolResultTokens` (default e.g., 2000 tokens / ~8000 chars).
- For `repo.search`, return only the first N matching lines rather than all matches.
- For `git diff`, return only changed file names plus a small diff sample unless the model asks for the full diff.

## Phase 3 — Native Tool Calling (future milestone)

Local servers increasingly support OpenAI-compatible `tools`/`functions` in chat completions. Add a provider capability `ToolCalling = native` and, when active, convert Marshal’s registry of tools into native function schemas.

In this mode:

- The provider handles tool selection and argument generation.
- The runner invokes the matching Marshal tool and returns the result in the standard `tool` message role.
- Fallback to the JSON action protocol remains available for providers without the capability.

This is kept as a future milestone because server support is inconsistent and it requires a second message-handling path in the runner.

## Success Criteria

- Average live-test response time for `file.read`/`shell.run` drops by at least 20% after Phase 1.
- `ErrMaxIterationsExceeded` rate in live integration tests drops to <10% after Phase 2.
- All existing unit and integration tests continue to pass.
- No breaking changes to the public `Runner`/`Provider` interfaces beyond new optional fields.

## Testing

- Extend `internal/agent/prompts_test.go` to assert the few-shot examples are present.
- Add unit tests for the tool-result cache in `internal/app/session`.
- Add runner-level tests for parallel read-only actions and loop detection using the existing `scriptedProvider`.
- Keep the live integration suite in `internal/app/live_agent_test.go` as the end-to-end validation; use it to measure before/after latency and failure rates.

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| JSON mode not supported by local server | Capability-gated fallback to current parser. |
| Parallel reads create noisy transcripts | Feed results in deterministic order. |
| Loop detection false-positives | Only trigger on exact `(tool, args)` repeats ≥3 times. |
| Native tool calling fragmentation | Abstract behind provider capability flag; keep JSON path forever. |
