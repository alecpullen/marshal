package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func usageRow(agent string, id int64, at time.Time, cost float64) UsageRow {
	return UsageRow{
		ID: id, StartedAt: at.UnixMilli(), DurationMs: 3_600_000, Role: "implementer",
		Provider: "p", Model: "m1", PromptTokens: 100, CompletionTokens: 50, CostUSD: cost,
		AgentID: agent, Project: "/proj/a", Origin: "ui",
	}
}

func TestUsageLogAppendDedupes(t *testing.T) {
	u := NewUsageLog(t.TempDir())
	now := time.Now().UTC()
	rows := []UsageRow{usageRow("a1", 1, now, 0.5), usageRow("a1", 2, now, 0.25)}
	fresh, err := u.Append(rows)
	if err != nil || len(fresh) != 2 {
		t.Fatalf("first append = %d rows, %v", len(fresh), err)
	}
	fresh, err = u.Append(append(rows, usageRow("a2", 1, now, 1)))
	if err != nil || len(fresh) != 1 || fresh[0].AgentID != "a2" {
		t.Fatalf("a resent row was not deduped: %+v, %v", fresh, err)
	}
	got, _ := u.Range(now.Add(-time.Hour), now.Add(time.Hour))
	if len(got) != 3 {
		t.Fatalf("ledger holds %d rows, want 3", len(got))
	}
}

func TestUsageLogDedupeSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	if _, err := NewUsageLog(dir).Append([]UsageRow{usageRow("a1", 1, now, 1)}); err != nil {
		t.Fatal(err)
	}
	// A restarted agent resends rows the bridge already ledgered.
	fresh, err := NewUsageLog(dir).Append([]UsageRow{usageRow("a1", 1, now, 1)})
	if err != nil || len(fresh) != 0 {
		t.Fatalf("a row from before the restart was counted again: %+v, %v", fresh, err)
	}
}

func TestUsageLogRollsOverByStartMonth(t *testing.T) {
	dir := t.TempDir()
	u := NewUsageLog(dir)
	endOfAug := time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC)
	startOfSep := time.Date(2026, 9, 1, 0, 1, 0, 0, time.UTC)
	if _, err := u.Append([]UsageRow{usageRow("a", 1, endOfAug, 1), usageRow("a", 2, startOfSep, 2)}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-08.jsonl", "2026-09.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, "usage", name))
		if err != nil || strings.Count(string(data), "\n") != 1 {
			t.Fatalf("%s = %q, %v; want one row", name, data, err)
		}
	}
	got, _ := u.Range(endOfAug.Add(-time.Minute), startOfSep.Add(time.Minute))
	if len(got) != 2 {
		t.Fatalf("a range over the boundary returned %d rows, want 2", len(got))
	}
	got, _ = u.Range(startOfSep, startOfSep.Add(time.Hour))
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("a September-only range = %+v", got)
	}
}

func TestUsageLogSkipsATornLine(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	u := NewUsageLog(dir)
	if _, err := u.Append([]UsageRow{usageRow("a", 1, now, 1)}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(dir, "usage", usageFileName(now)), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"id":2,"startedAt`)
	f.Close()
	got, _ := u.Range(now.Add(-time.Hour), now.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
}

func usageFleet(t *testing.T) (*Fleet, time.Time) {
	t.Helper()
	f := testFleet(t)
	f.usage = NewUsageLog(t.TempDir())
	f.audit = NewAuditLog(t.TempDir())
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	f.clock = func() time.Time { return now }
	return f, now
}

func TestUsageReportAggregatesEachWay(t *testing.T) {
	f, now := usageFleet(t)
	rows := []UsageRow{
		usageRow("a1", 1, now.Add(-time.Hour), 1.0),
		usageRow("a1", 2, now.Add(-2*time.Hour), 0.5),
		usageRow("a2", 1, now.Add(-30*time.Hour), 2.0),
	}
	rows[2].Project, rows[2].Role, rows[2].Model = "/proj/b", "reviewer", "m2"
	rows = append(rows, usageRow("a3", 1, now.Add(-10*24*time.Hour), 9)) // outside 7d
	if _, err := f.usage.Append(rows); err != nil {
		t.Fatal(err)
	}

	get := func(by string) usageReport {
		rep, err := f.usageReport(7*24*time.Hour, "7d", by, now)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	rep := get("day")
	if rep.Totals.CostUSD != 3.5 || rep.Totals.PromptTokens != 300 || rep.Totals.CompletionTokens != 150 {
		t.Fatalf("totals = %+v", rep.Totals)
	}
	if rep.Totals.AgentHours != 3 {
		t.Fatalf("agentHours = %v, want 3 (three one-hour turns)", rep.Totals.AgentHours)
	}
	// Every day in the window appears, oldest first, spend or not.
	if len(rep.Series) != 8 || rep.Series[0].Key >= rep.Series[7].Key || rep.Series[7].Key != "2026-10-04" {
		t.Fatalf("day series = %+v", rep.Series)
	}
	byDay := map[string]float64{}
	for _, p := range rep.Series {
		byDay[p.Key] = p.CostUSD
	}
	if byDay["2026-10-04"] != 1.5 || byDay["2026-10-03"] != 2.0 {
		t.Fatalf("day costs = %v", byDay)
	}

	for by, want := range map[string][]usagePoint{
		"project": {{"/proj/b", 2.0, 150}, {"/proj/a", 1.5, 300}},
		"role":    {{"reviewer", 2.0, 150}, {"implementer", 1.5, 300}},
		"model":   {{"m2", 2.0, 150}, {"m1", 1.5, 300}},
	} {
		got := get(by).Series
		if len(got) != len(want) {
			t.Fatalf("by %s: series = %+v", by, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("by %s [%d] = %+v, want %+v", by, i, got[i], want[i])
			}
		}
	}
}

func TestUsagePRsShippedFromAuditAndAgents(t *testing.T) {
	f, _ := usageFleet(t)
	now := time.Now().UTC()
	put := func(id, pr string, pushed time.Time) {
		if err := f.ws.PutAgent(Agent{ID: id, Project: "/p", OwnerID: DefaultOwnerID, PRUrl: pr, PushedAt: pushed}); err != nil {
			t.Fatal(err)
		}
	}
	put("pr-agent", "https://github.com/o/r/pull/1", now)
	put("push-only", "", now) // pushed a branch, no PR
	put("old-pr", "https://github.com/o/r/pull/2", now.Add(-30*24*time.Hour))
	f.auditf(AuditEvent{Event: AuditPush, AgentID: "pr-agent", Detail: "marshal/x"})
	f.auditf(AuditEvent{Event: AuditPush, AgentID: "push-only", Detail: "marshal/y"})
	// An agent since removed whose push event itself named a PR.
	f.auditf(AuditEvent{Event: AuditPush, AgentID: "gone", Detail: "https://github.com/o/r/pull/3"})

	got := f.prsShipped(now.Add(-time.Hour), now.Add(time.Hour))
	if got != 2 {
		t.Fatalf("prsShipped = %d, want 2 (pr-agent counted once, gone via its event)", got)
	}
}

func TestUsageRouteValidatesParams(t *testing.T) {
	f, _ := usageFleet(t)
	s := NewServer(f, "")
	for path, want := range map[string]int{
		"/api/usage":                    http.StatusOK,
		"/api/usage?range=30d&by=model": http.StatusOK,
		"/api/usage?range=1y":           http.StatusBadRequest,
		"/api/usage?range=7d&by=planet": http.StatusBadRequest,
	} {
		if rec := doReq(t, s, http.MethodGet, path, nil, nil); rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", path, rec.Code, want, rec.Body.String())
		}
	}
}

func TestTelemetryUsageReachesTheLedger(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	f.usage = NewUsageLog(t.TempDir())
	root := t.TempDir()
	id, err := f.Spawn(t.Context(), root, SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	update := map[string]any{"sessionId": "s-1", "update": map[string]any{
		"kind": "session_telemetry", "usage": []UsageRow{usageRow("", 7, now, 0.42)},
	}}
	agentOf(id).notify("session/update", update)
	agentOf(id).notify("session/update", update) // a resend
	waitFor(t, 5*time.Second, "usage row ledgered", func() bool {
		rows, _ := f.usage.Range(now.Add(-time.Hour), now.Add(time.Hour))
		return len(rows) == 1
	})
	time.Sleep(50 * time.Millisecond)
	rows, _ := f.usage.Range(now.Add(-time.Hour), now.Add(time.Hour))
	if len(rows) != 1 || rows[0].AgentID != id || rows[0].Project != root || rows[0].Origin != OriginUI || rows[0].CostUSD != 0.42 {
		t.Fatalf("rows = %+v", rows)
	}
	// The telemetry delta that reaches the fleet stream must not carry
	// the usage rows.
	events := f.fleetLog.Tail(fleetStreamKey)
	for _, e := range events {
		if strings.Contains(string(e.Data), `"usage"`) {
			t.Fatalf("usage leaked into the fleet stream: %s", e.Data)
		}
	}
}

// The full engine telemetry payload (changedFiles as objects, a sub-cent
// usage row with costMicroUsd) must reach the ledger and the budget, and
// the delta that reaches the fleet stream must carry the telemetry digest.
func TestRealTelemetryPayloadReachesLedgerAndBudget(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 0.001, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	at := h.now.UnixMilli()
	h.agentOf(id).notify("session/update", map[string]any{"sessionId": "s-1", "update": map[string]any{
		"kind":          "session_telemetry",
		"context":       map[string]any{"packTokens": 500, "packMaxTokens": 1000},
		"changedFiles":  []map[string]any{{"path": "a.go", "added": 2, "removed": 0}},
		"toolStats":     []map[string]any{{"name": "file.read", "calls": 1, "errors": 0, "slowestMs": 5}},
		"rules":         []string{},
		"sessionFooter": map[string]any{"turns": 1},
		"usage": []map[string]any{{
			"id": 1, "startedAt": at, "durationMs": 1000, "role": "implementer", "provider": "p", "model": "m",
			"promptTokens": 10, "completionTokens": 5, "costUsd": 0, "costMicroUsd": 1500,
		}},
	}})
	waitFor(t, 5*time.Second, "sub-cent spend counted", func() bool {
		return h.f.budgetReport().Daily.SpentUSD == 0.0015
	})
	rows, _ := h.f.usage.Range(h.now.Add(-time.Hour), h.now.Add(time.Hour))
	if len(rows) != 1 || rows[0].CostMicroUSD != 1500 || rows[0].AgentID != id {
		t.Fatalf("ledger rows = %+v", rows)
	}
	if code, _ := h.spawnStatus(t); code != http.StatusTooManyRequests {
		t.Fatalf("spawn = %d, want 429 once the sub-cent spend crossed the cap", code)
	}
	waitFor(t, 5*time.Second, "telemetry delta streamed", func() bool {
		for _, e := range h.f.fleetLog.Tail(fleetStreamKey) {
			var m map[string]any
			if json.Unmarshal(e.Data, &m) == nil && m["kind"] == "telemetry" {
				return m["changedFiles"] == float64(1) && m["contextPct"] == float64(50) && m["usage"] == nil
			}
		}
		return false
	})
}
