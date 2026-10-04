package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// budgetHarness is a fleet with a fake control agent that serves budgets,
// a fake project agent per spawn, a ledger, and a controllable clock.
type budgetHarness struct {
	f       *Fleet
	s       *Server
	ctl     *fakeAgent
	agentOf func(string) *fakeAgent
	now     time.Time
	mu      sync.Mutex
	cfg     Budgets
}

func newBudgetHarness(t *testing.T, cfg Budgets) *budgetHarness {
	t.Helper()
	f, ctl, agentOf := agentFleet(t)
	h := &budgetHarness{f: f, ctl: ctl, agentOf: agentOf, cfg: cfg,
		now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	f.usage = NewUsageLog(t.TempDir())
	f.clock = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}
	// config/get and config/set_budgets share the harness's stored caps,
	// as the engine's user config would.
	ctl.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch method {
		case "config/get":
			return map[string]any{"budgets": h.cfg}, nil, true
		case "config/set_budgets":
			var p struct {
				Budgets Budgets `json:"budgets"`
			}
			_ = json.Unmarshal(params, &p)
			h.cfg = p.Budgets
			return map[string]any{}, nil, true
		}
		return nil, nil, false
	}
	h.s = NewServer(f, "")
	return h
}

func (h *budgetHarness) setNow(t time.Time) {
	h.mu.Lock()
	h.now = t
	h.mu.Unlock()
}

func (h *budgetHarness) spawn(t *testing.T) string {
	t.Helper()
	id, err := h.f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// spend reports one usage row for an agent through the same path as a
// telemetry update.
func (h *budgetHarness) spend(agent string, id int64, cost float64) {
	h.mu.Lock()
	at := h.now
	h.mu.Unlock()
	raw, _ := json.Marshal([]UsageRow{usageRow("", id, at, cost)})
	h.f.recordUsage(agent, raw)
}

func (h *budgetHarness) spawnStatus(t *testing.T) (int, string) {
	t.Helper()
	rec := doReq(t, h.s, http.MethodPost, "/api/agents", map[string]any{"project": t.TempDir()}, nil)
	return rec.Code, rec.Body.String()
}

func budgetDeltas(f *Fleet) []budgetDelta {
	events := f.fleetLog.Tail(fleetStreamKey)
	var out []budgetDelta
	for _, e := range events {
		var d fleetDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "budget" && d.Budget != nil {
			out = append(out, *d.Budget)
		}
	}
	return out
}

func TestBudgetDailyBlockReturns429OnSpawnAndPrompt(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 1.5)

	code, body := h.spawnStatus(t)
	if code != http.StatusTooManyRequests ||
		!strings.Contains(body, `"error":"budget_exceeded"`) || !strings.Contains(body, `"scope":"daily"`) {
		t.Fatalf("spawn = %d %s", code, body)
	}
	rec := doReq(t, h.s, http.MethodPost, "/api/sessions/"+id+"/prompt", map[string]any{"text": "go"}, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), `"scope":"daily"`) {
		t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, h.s, http.MethodPost, "/api/runs", map[string]any{"agentId": id, "kind": "swarm", "goal": "x"}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("run = %d %s", rec.Code, rec.Body.String())
	}
	deltas := budgetDeltas(h.f)
	if len(deltas) != 1 || deltas[0].Scope != "daily" || deltas[0].Action != "block" ||
		deltas[0].CapUSD != 1 || deltas[0].SpentUSD != 1.5 {
		t.Fatalf("deltas = %+v", deltas)
	}
}

func TestBudgetRaisingTheCapLiftsTheBlock(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 2)
	if code, _ := h.spawnStatus(t); code != http.StatusTooManyRequests {
		t.Fatalf("not blocked: %d", code)
	}
	rec := doReq(t, h.s, http.MethodPut, "/api/budgets", map[string]any{"budgets": Budgets{
		DailyUSD: 10, OnDailyCap: "block", OnAgentCap: "warn"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	if code, body := h.spawnStatus(t); code != http.StatusCreated {
		t.Fatalf("block outlived a raised cap: %d %s", code, body)
	}
	if got := h.ctl.calls("config/set_budgets"); len(got) != 1 || !strings.Contains(got[0], `"dailyUsd":10`) {
		t.Fatalf("set_budgets = %v", got)
	}
	if findEvent(auditTail(t, h.f), AuditBudgetsChanged) == nil {
		t.Fatal("budgets change not audited")
	}
}

func TestBudgetWarnOnlyEmitsOneDelta(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, PerAgentUSD: 1, OnDailyCap: "warn", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 2)
	h.spend(id, 2, 2)
	if code, body := h.spawnStatus(t); code != http.StatusCreated {
		t.Fatalf("warn blocked a spawn: %d %s", code, body)
	}
	deltas := budgetDeltas(h.f)
	if len(deltas) != 2 {
		t.Fatalf("deltas = %+v, want one daily and one agent, each once", deltas)
	}
	scopes := map[string]string{}
	for _, d := range deltas {
		scopes[d.Scope] = d.Action
	}
	if scopes["daily"] != "warn" || scopes["agent"] != "warn" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestBudgetAgentPauseCancelsItsTurnAndOverrideLiftsIt(t *testing.T) {
	h := newBudgetHarness(t, Budgets{PerAgentUSD: 1, OnDailyCap: "warn", OnAgentCap: "pause"})
	id := h.spawn(t)
	other := h.spawn(t)
	h.spend(id, 1, 1.25)

	waitFor(t, 5*time.Second, "session/cancel to reach the paused agent", func() bool {
		return len(h.agentOf(id).calls("session/cancel")) == 1
	})
	if n := len(h.agentOf(other).calls("session/cancel")); n != 0 {
		t.Fatalf("an unrelated agent was cancelled")
	}
	rec := doReq(t, h.s, http.MethodPost, "/api/sessions/"+id+"/prompt", map[string]any{"text": "go"}, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), `"scope":"agent"`) {
		t.Fatalf("paused prompt = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, h.s, http.MethodPost, "/api/sessions/"+other+"/prompt", map[string]any{"text": "go"}, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("an unpaused agent was refused: %d %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h.s, http.MethodPost, "/api/agents/"+id+"/budget/override", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("override = %d %s", rec.Code, rec.Body.String())
	}
	if !hasEvent(auditTail(t, h.f), AuditBudgetOverride, id) {
		t.Fatal("override not audited")
	}
	if rec := doReq(t, h.s, http.MethodPost, "/api/sessions/"+id+"/prompt", map[string]any{"text": "go"}, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("override did not lift the pause: %d %s", rec.Code, rec.Body.String())
	}
	// An overridden agent that keeps spending is not paused again.
	h.spend(id, 2, 5)
	time.Sleep(50 * time.Millisecond)
	if n := len(h.agentOf(id).calls("session/cancel")); n != 1 {
		t.Fatalf("an overridden agent was cancelled again (%d cancels)", n)
	}
	if rec := doReq(t, h.s, http.MethodPost, "/api/agents/nope/budget/override", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("override of an unknown agent = %d", rec.Code)
	}
}

func TestBudgetDayChangesAtUTCMidnight(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, OnDailyCap: "block", OnAgentCap: "warn"})
	h.setNow(time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC))
	id := h.spawn(t)
	h.spend(id, 1, 2)
	if code, _ := h.spawnStatus(t); code != http.StatusTooManyRequests {
		t.Fatalf("not blocked before midnight: %d", code)
	}
	h.setNow(time.Date(2026, 10, 5, 0, 1, 0, 0, time.UTC))
	if code, body := h.spawnStatus(t); code != http.StatusCreated {
		t.Fatalf("still blocked after midnight: %d %s", code, body)
	}
	rep := h.f.budgetReport()
	if rep.Daily.Day != "2026-10-05" || rep.Daily.SpentUSD != 0 || rep.Daily.Blocked {
		t.Fatalf("report after midnight = %+v", rep.Daily)
	}
	// The new day spends against its own tally.
	h.spend(id, 2, 0.5)
	if h.f.budgetReport().Daily.SpentUSD != 0.5 {
		t.Fatalf("new day tally = %+v", h.f.budgetReport().Daily)
	}
}

func TestBudgetSeedsSpendFromTheLedger(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, PerAgentUSD: 1, OnDailyCap: "block", OnAgentCap: "pause"})
	// Spend recorded before this bridge process started.
	if _, err := h.f.usage.Append([]UsageRow{
		usageRow("old-agent", 1, h.now.Add(-time.Hour), 0.7),
		usageRow("old-agent", 2, h.now.Add(-2*time.Hour), 0.7),
		usageRow("last-week", 1, h.now.Add(-3*24*time.Hour), 4),
	}); err != nil {
		t.Fatal(err)
	}
	rep := h.f.budgetReport()
	if rep.Daily.SpentUSD != 1.4 {
		t.Fatalf("seeded daily spend = %v, want 1.4", rep.Daily.SpentUSD)
	}
	byAgent := map[string]float64{}
	for _, a := range rep.Agents {
		byAgent[a.AgentID] = a.SpentUSD
	}
	if byAgent["old-agent"] != 1.4 || byAgent["last-week"] != 4 {
		t.Fatalf("seeded agent spend = %v", byAgent)
	}
}

func TestBudgetDuplicateRowsCountOnce(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 100, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 3)
	h.spend(id, 1, 3)
	if got := h.f.budgetReport().Daily.SpentUSD; got != 3 {
		t.Fatalf("daily spend = %v after a resent row, want 3", got)
	}
}

func TestBudgetWithoutAControlAgentEnforcesNothing(t *testing.T) {
	h := newBudgetHarness(t, Budgets{})
	h.ctl.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
		if method == "config/get" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	id := h.spawn(t)
	h.spend(id, 1, 1000)
	if code, body := h.spawnStatus(t); code != http.StatusCreated {
		t.Fatalf("an old engine with no config methods blocked a spawn: %d %s", code, body)
	}
}

func TestBudgetsRoutes(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 25, PerAgentUSD: 5, OnDailyCap: "block", OnAgentCap: "pause"})
	id := h.spawn(t)
	h.spend(id, 1, 0.75)
	rec := doReq(t, h.s, http.MethodGet, "/api/budgets", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	var rep budgetReport
	decodeBody(t, rec, &rep)
	if rep.Budgets.DailyUSD != 25 || rep.Budgets.OnAgentCap != "pause" || rep.Daily.SpentUSD != 0.75 ||
		len(rep.Agents) != 1 || rep.Agents[0].AgentID != id {
		t.Fatalf("report = %+v", rep)
	}
	if rec := doReq(t, h.s, http.MethodPut, "/api/budgets", map[string]any{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("put without budgets = %d", rec.Code)
	}
}
