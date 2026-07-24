#!/usr/bin/env python3
"""ACP-driven Task 7: UsageAggregator for session-scope live tracking."""
import sys
sys.path.insert(0, "/tmp/kilo")
from marshal_acp_driver import ACPClient, initialize_and_create_session, run_single_turn, close_session, log
from pathlib import Path

LOG_PATH = Path("/tmp/kilo/marshal_acp_task7.log")
LOG_PATH.write_text("", encoding="utf-8")

client = ACPClient()
client.start()
try:
    sid = initialize_and_create_session(client)
    prompt = """Implement Task 7 of the Token Metric Tracking plan.

Goal: add an in-memory session-scope usage aggregator fed by TurnMetrics.

Files to create:
- internal/agent/usage.go
- internal/agent/usage_test.go

Required changes in internal/agent/usage.go:
- Define type UsageAggregator with sync.Mutex, totals db.UsageTotals, and byModel map[string]*db.ModelBreakdown.
- func NewUsageAggregator() *UsageAggregator returns a new aggregator with byModel initialized.
- func (a *UsageAggregator) Observe(m TurnMetrics):
    * Add all token counts and EstimatedCostCents from m to a.totals (int64 fields).
    * Increment a.totals.Turns.
    * Look up or create the per-model breakdown keyed by provider + "/" + model.
    * Add the same fields to the breakdown's UsageTotals and breakdown.Turns.
    * Update breakdown.Reported (db.TokenCategorySet bits): always set CatPrompt and CatCompletion. Set CatReasoning if m.ReasoningTokens > 0, CatCacheRead if m.CacheReadTokens > 0, CatCacheWrite if m.CacheWriteTokens > 0.
- func (a *UsageAggregator) Snapshot() (db.UsageTotals, []db.ModelBreakdown):
    * Lock, copy totals, collect breakdown pointers into a slice.
    * Sort breakdowns by EstimatedCostCents descending (and by Provider+Model as a stable tie-breaker).
    * Return the totals and sorted breakdowns.

Required changes in internal/agent/usage_test.go:
- TestUsageAggregatorAccumulates: observe three TurnMetrics (two gpt-4o, one ollama local) and assert totals and sorted breakdowns.
- TestUsageAggregatorCapabilityTracking: observe a turn with reasoning + cache read but no cache write, then assert Reported bits for CatReasoning and CatCacheRead are set and CatCacheWrite is not.
- TestUsageAggregatorLocalModelNoCapabilities: observe a local model turn and assert CatPrompt and CatCompletion are set, but CatReasoning is not.

TDD workflow:
- Write usage_test.go first; run go test ./internal/agent/ -run TestUsageAggregator -v and confirm failure (UsageAggregator undefined).
- Implement usage.go.
- Run go test ./internal/agent/ -v until all pass.
- Run go build ./... to check for breakage.
- Run gofmt -w on the two new files.
- Stage and commit with message: "feat(agent): add UsageAggregator for session-scope token usage and cost".

Ignore any pre-existing failing test whose name contains "TestUsability". Report TASK 7 COMPLETE or TASK 7 FAILED with reason.
"""
    reply, resp = run_single_turn(client, sid, prompt, timeout=900.0)
    print("STOP:", resp)
    print("REPLY:\n", reply)
    close_session(client, sid)
finally:
    client.close()
