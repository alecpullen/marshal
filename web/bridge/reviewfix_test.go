package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// microSpend reports raw usage rows as a telemetry update would.
func (h *budgetHarness) microSpend(agent string, rows ...UsageRow) {
	raw, _ := json.Marshal(rows)
	h.f.recordUsage(agent, raw)
}

func TestBudgetSumsSubCentMicroDollarRows(t *testing.T) {
	// $0.001 cap: three rows of $0.0004 are whole-cent zero, so only the
	// micro field can trip it.
	h := newBudgetHarness(t, Budgets{DailyUSD: 0.001, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	at := h.now
	var rows []UsageRow
	for i := int64(1); i <= 3; i++ {
		r := usageRow("", i, at, 0)
		r.CostMicroUSD = 400
		rows = append(rows, r)
	}
	h.microSpend(id, rows...)
	if code, body := h.spawnStatus(t); code != http.StatusTooManyRequests {
		t.Fatalf("spawn = %d %s, want 429 once 1200 micro-dollars are spent", code, body)
	}
	rep := h.f.budgetReport()
	if rep.Daily.SpentUSD != 0.0012 {
		t.Fatalf("daily spend = %v, want 0.0012", rep.Daily.SpentUSD)
	}
}

func TestUsageFallsBackToCostUSDWithoutMicro(t *testing.T) {
	f, now := usageFleet(t)
	old := usageRow("a1", 1, now.Add(-time.Hour), 0.25) // CostMicroUSD unset
	fine := usageRow("a1", 2, now.Add(-time.Hour), 0)
	fine.CostMicroUSD = 123
	if _, err := f.usage.Append([]UsageRow{old, fine}); err != nil {
		t.Fatal(err)
	}
	rep, err := f.usageReport(24*time.Hour, "24h", "day", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := 0.250123; rep.Totals.CostUSD != want {
		t.Fatalf("cost = %v, want %v", rep.Totals.CostUSD, want)
	}
}

func TestBudgetGateAppliesToSpawnAndRunStart(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 2)

	var eb ErrBudget
	if _, err := h.f.Spawn(t.Context(), t.TempDir(), SpawnOptions{Origin: OriginIssue}); !errors.As(err, &eb) {
		t.Fatalf("Spawn on a blocked day = %v, want ErrBudget", err)
	}
	if err := h.f.StartRun(t.Context(), id, RunRequest{Kind: RunSwarm, Goal: "x"}); !errors.As(err, &eb) {
		t.Fatalf("StartRun on a blocked day = %v, want ErrBudget", err)
	}
}

func TestBudgetsReportExposesLoaded(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 5, OnDailyCap: "block"})
	var rep map[string]json.RawMessage
	decodeBody(t, doReq(t, h.s, http.MethodGet, "/api/budgets", nil, nil), &rep)
	if string(rep["loaded"]) != "true" {
		t.Fatalf("loaded = %s, want true", rep["loaded"])
	}

	f := testFleet(t) // no control agent
	f.usage = NewUsageLog(t.TempDir())
	var rep2 map[string]json.RawMessage
	decodeBody(t, doReq(t, NewServer(f, ""), http.MethodGet, "/api/budgets", nil, nil), &rep2)
	if string(rep2["loaded"]) != "false" {
		t.Fatalf("loaded = %s, want false without a control agent", rep2["loaded"])
	}
}

func TestBudgetAndRerouteDeltasAreFlat(t *testing.T) {
	h := newBudgetHarness(t, Budgets{DailyUSD: 1, OnDailyCap: "block", OnAgentCap: "warn"})
	id := h.spawn(t)
	h.spend(id, 1, 1.5)
	var found bool
	for _, e := range h.f.fleetLog.Tail(fleetStreamKey) {
		var m map[string]any
		if json.Unmarshal(e.Data, &m) != nil || m["kind"] != "budget" {
			continue
		}
		found = true
		for _, k := range []string{"sessionId", "scope", "spentUsd", "capUsd", "action"} {
			if _, ok := m[k]; !ok {
				t.Errorf("budget delta lacks top-level %q: %v", k, m)
			}
		}
	}
	if !found {
		t.Fatal("no budget delta")
	}

	r := newRoutingHarness(t)
	r.startRerouteWatch()
	r.fire("fired")
	waitFor(t, 5*time.Second, "reroute delta", func() bool { return len(rerouteDeltas(r.f)) == 1 })
	for _, e := range r.f.fleetLog.Tail(fleetStreamKey) {
		var m map[string]any
		if json.Unmarshal(e.Data, &m) != nil || m["kind"] != "reroute" {
			continue
		}
		for _, k := range []string{"id", "watchId", "watch", "role", "from", "to", "at"} {
			if _, ok := m[k]; !ok {
				t.Errorf("reroute delta lacks top-level %q: %v", k, m)
			}
		}
		if _, ok := m["from"].(string); !ok {
			t.Errorf("from = %v, want a display string", m["from"])
		}
	}
}

func TestRunDeltaAndListCarryAtInMillis(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	before := time.Now().UnixMilli()
	agentOf(id).notify("session/update", map[string]any{
		"sessionId": "s-1",
		"update":    map[string]any{"kind": "run_progress", "run": json.RawMessage(testRunDigest)},
	})
	waitFor(t, 5*time.Second, "run listed", func() bool { return len(f.listRuns()) == 1 })
	if at := f.listRuns()[0].At; at < before || at > time.Now().UnixMilli() {
		t.Fatalf("list at = %d, want Unix ms near %d", at, before)
	}
	waitFor(t, 5*time.Second, "run delta", func() bool {
		for _, e := range f.fleetLog.Tail(fleetStreamKey) {
			var m map[string]any
			if json.Unmarshal(e.Data, &m) == nil && m["kind"] == "run" {
				at, _ := m["at"].(float64)
				return int64(at) >= before && m["agentId"] == id
			}
		}
		return false
	})
}

func TestRerouteFailureRestoresTheRuleAndAudits(t *testing.T) {
	h := newRoutingHarness(t)
	prev := h.ctl.handler
	var failSet sync.Mutex
	failing := true
	h.ctl.handler = func(m string, p json.RawMessage) (any, *rpcError, bool) {
		if m == "config/set_routing" {
			failSet.Lock()
			defer failSet.Unlock()
			if failing {
				return nil, &rpcError{Code: -32000, Message: "disk full"}, true
			}
		}
		return prev(m, p)
	}
	h.startRerouteWatch()
	h.fire("fired")
	waitFor(t, 5*time.Second, "failure audited", func() bool {
		return findEvent(auditTail(t, h.f), AuditRerouteFailed) != nil
	})
	if _, ok := h.f.ws.WatchRule("w1"); !ok {
		t.Fatal("a failed reroute dropped the rule for good")
	}
	if h.binding("implementer") != `{"preset":"slow"}` {
		t.Fatal("binding changed despite the failure")
	}
	// The next firing succeeds now that the rule was restored.
	failSet.Lock()
	failing = false
	failSet.Unlock()
	h.fire("fired")
	waitFor(t, 5*time.Second, "reroute applied", func() bool { return h.binding("implementer") == `{"preset":"fast"}` })
}

func TestRerouteUndoIsAtomic(t *testing.T) {
	h := newRoutingHarness(t)
	h.startRerouteWatch()
	h.fire("fired")
	waitFor(t, 5*time.Second, "reroute applied", func() bool { return len(rerouteDeltas(h.f)) == 1 })
	id := rerouteDeltas(h.f)[0].ID

	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = doReq(t, h.s, http.MethodPost, "/api/reroutes/"+id+"/undo", nil, nil).Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		} else if c != http.StatusConflict {
			t.Fatalf("code = %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d undos succeeded, want exactly 1 (%v)", ok, codes)
	}
	if n := len(h.ctl.calls("config/set_routing")); n != 2 {
		t.Fatalf("set_routing sent %d times, want 2 (reroute + one undo)", n)
	}
}

func TestRunsStartDiscardsAFreshAgentWhenTheRunFails(t *testing.T) {
	f, _, _ := agentFleet(t)
	f.newRuntime = func(a Agent) (*Child, error) {
		fa := newFakeAgent()
		fa.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
			if m == "session/swarm_start" {
				return nil, &rpcError{Code: -32000, Message: "refused"}, true
			}
			return nil, nil, false
		}
		return &Child{Transport: fa}, nil
	}
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs",
		map[string]any{"project": t.TempDir(), "kind": "swarm", "goal": "go"}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(f.ws.Agents()); n != 0 {
		t.Fatalf("%d agents left behind by a failed run start", n)
	}
}

func TestRunAnswerClearsThePreviousError(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	f.live.setRunErr(id, f.live.beginRun(id), "old failure")
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs/"+id+"/answer", map[string]any{"answer": "go"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if got := f.live.get(id).runErr; got != "" {
		t.Fatalf("runErr = %q after a new run began", got)
	}
	_ = agentOf
}

func TestLateErrorFromAnEarlierRunIsDropped(t *testing.T) {
	f, _, _ := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	first := f.live.beginRun(id)
	second := f.live.beginRun(id)
	f.live.setRunErr(id, first, "stale")
	if got := f.live.get(id).runErr; got != "" {
		t.Fatalf("a superseded run wrote %q", got)
	}
	f.live.setRunErr(id, second, "current")
	if got := f.live.get(id).runErr; !strings.Contains(got, "current") {
		t.Fatalf("runErr = %q", got)
	}
}

func TestLibraryStagedTokensEvictOldestFirst(t *testing.T) {
	var l libraryState
	for i := 0; i < maxStagedTokens; i++ {
		l.remember("t"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "s")
	}
	l.remember("newest", "s2")
	if _, ok := l.lookup("newest"); !ok {
		t.Fatal("newest token missing")
	}
	if _, ok := l.lookup("ta"); ok {
		t.Fatal("oldest token should have been evicted")
	}
	if _, ok := l.lookup("tb"); !ok {
		t.Fatal("a token other than the oldest was evicted")
	}
}
