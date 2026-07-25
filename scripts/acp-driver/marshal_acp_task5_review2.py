#!/usr/bin/env python3
"""Re-run the fresh-context review for Task 5 with a concise prompt."""
import sys
sys.path.insert(0, "/tmp/kilo")
from marshal_acp_driver import ACPClient, initialize_and_create_session, run_single_turn, close_session, log
from pathlib import Path

LOG_PATH = Path("/tmp/kilo/marshal_acp_task5_review2.log")
LOG_PATH.write_text("", encoding="utf-8")

client = ACPClient()
client.start()
try:
    sid = initialize_and_create_session(client)
    review_prompt = """Review Task 5 of the Token Metric Tracking plan. Verify by reading files and running tests. Ignore any pre-existing failing test whose name contains "TestUsability".

Requirements for Task 5:
1. internal/agent/metrics.go: TurnMetrics has ReasoningTokens, CacheReadTokens, CacheWriteTokens, EstimatedCostCents; emitMetrics computes cost via pricing.EstimateCostCents.
2. internal/agent/runner.go: Runner has Pricing pricing.ModelPricing; UsageObserver signature is func(usage schema.TokenUsage); CopyFrom copies Pricing.
3. internal/agent/chat.go: records all usage fields in turnStats and passes full schema.TokenUsage to UsageObserver.
4. internal/agent/metrics_test.go: new test TestTurnMetricsRecordsAllUsageFields passes.
5. internal/agent/swarm/meter.go and orchestrator.go updated for widened signatures.
6. internal/app/app.go wires runner.Pricing from pricing.Lookup(route.Preset) and updates UsageObserver closure.
7. Narrow tests pass: go test ./internal/agent/ ./internal/agent/swarm/ ./internal/app/ -count=1

End with exactly:
VERDICT: APPROVED
or
VERDICT: REJECTED - reason
"""
    reply, resp = run_single_turn(client, sid, review_prompt, timeout=600.0)
    print("STOP:", resp)
    print("REPLY:\n", reply)
    close_session(client, sid)
finally:
    client.close()
