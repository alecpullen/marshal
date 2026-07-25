#!/usr/bin/env python3
"""ACP-driven Task 6: DB turn_metrics widening + AggregateTurnMetrics."""
import sys
sys.path.insert(0, "/tmp/kilo")
from marshal_acp_driver import ACPClient, initialize_and_create_session, run_single_turn, close_session, log
from pathlib import Path

LOG_PATH = Path("/tmp/kilo/marshal_acp_task6.log")
LOG_PATH.write_text("", encoding="utf-8")

client = ACPClient()
client.start()
try:
    sid = initialize_and_create_session(client)
    prompt = """Implement Task 6 of the Token Metric Tracking plan.

Goal: persist the new token fields and estimated cost in SQLite, and add a project-scope aggregate query.

Files to modify:
- internal/db/migrations.go
- internal/db/turnmetrics.go
- internal/db/turnmetrics_test.go

Required changes:

1. In internal/db/migrations.go:
   - Add "turn_metrics" to allowedTableInfo.
   - Add four migrationColumns for turn_metrics:
       {"turn_metrics", "reasoning_tokens", "INTEGER NOT NULL DEFAULT 0"},
       {"turn_metrics", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
       {"turn_metrics", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
       {"turn_metrics", "estimated_cost_cents", "INTEGER NOT NULL DEFAULT 0"},

2. In internal/db/turnmetrics.go:
   - Define these types at package level:
       type UsageTotals struct {
           PromptTokens int64; CompletionTokens int64; ReasoningTokens int64; CacheReadTokens int64; CacheWriteTokens int64; EstimatedCostCents int64; Turns int
       }
       type ModelBreakdown struct { UsageTotals; Provider string; Model string; Reported TokenCategorySet }
       type TokenCategorySet uint8
       const ( CatPrompt TokenCategorySet = 1 << iota; CatCompletion; CatReasoning; CatCacheRead; CatCacheWrite )
   - Add to TurnMetricsRow: ReasoningTokens int, CacheReadTokens int, CacheWriteTokens int, EstimatedCostCents int64.
   - Update InsertTurnMetrics to write the four new columns.
   - Update RecentTurnMetrics SELECT/Scan to read the four new columns.
   - Add AggregateTurnMetrics(projectID int64) (UsageTotals, []ModelBreakdown, error) that does:
       SELECT provider, model, SUM(prompt_tokens), SUM(completion_tokens), SUM(reasoning_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(estimated_cost_cents), COUNT(*) FROM turn_metrics WHERE project_id = ? GROUP BY provider, model ORDER BY SUM(estimated_cost_cents) DESC
     and returns grand totals plus per-model breakdowns.

3. In internal/db/turnmetrics_test.go, add:
   - TestInsertAndRecentTurnMetricsWithNewFields — sets the four new fields, inserts, reads back, asserts values.
   - TestAggregateTurnMetrics — inserts three rows (two gpt-4o, one local), calls AggregateTurnMetrics, asserts totals and breakdowns sorted by cost descending.

TDD workflow:
- Write the two new tests first and run them (they should fail due to missing fields/functions).
- Implement the production changes.
- Run go test ./internal/db/ -v until all pass.
- Run go build ./... to check for breakage.
- Run gofmt -w on the three files.
- Stage and commit with message: "feat(db): widen turn_metrics with reasoning/cache/cost columns and add AggregateTurnMetrics".

Ignore any pre-existing failing test whose name contains "TestUsability". Report TASK 6 COMPLETE or TASK 6 FAILED with reason.
"""
    reply, resp = run_single_turn(client, sid, prompt, timeout=900.0)
    print("STOP:", resp)
    print("REPLY:\n", reply)
    close_session(client, sid)
finally:
    client.close()
