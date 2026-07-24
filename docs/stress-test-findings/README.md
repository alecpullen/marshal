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
