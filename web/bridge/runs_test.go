package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const testRunDigest = `{"kind":"sdd","sdd":{"phase":"implement","currentTask":2}}`

func TestClassifyRunProgress(t *testing.T) {
	params := json.RawMessage(`{"sessionId":"s-1","update":{"kind":"run_progress","run":` + testRunDigest + `}}`)
	d, ok := classifyNotification("session/update", params)
	if !ok || d.Kind != "run" || string(d.Run) != testRunDigest {
		t.Fatalf("delta = %+v ok=%v", d, ok)
	}
	// A run_progress without a payload carries nothing to store.
	if _, ok := classifyNotification("session/update",
		json.RawMessage(`{"sessionId":"s-1","update":{"kind":"run_progress"}}`)); ok {
		t.Fatal("an empty run_progress was classified")
	}
}

func TestRunsListReflectsRunProgress(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	root := t.TempDir()
	id, err := f.Spawn(t.Context(), root, SpawnOptions{Name: "runner"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(f, "")

	var empty []runListEntry
	rec := doReq(t, s, http.MethodGet, "/api/runs", nil, nil)
	decodeBody(t, rec, &empty)
	if len(empty) != 0 {
		t.Fatalf("a run was listed before any progress: %+v", empty)
	}

	agentOf(id).notify("session/update", map[string]any{
		"sessionId": "s-1",
		"update":    map[string]any{"kind": "run_progress", "run": json.RawMessage(testRunDigest)},
	})
	waitFor(t, 5*time.Second, "run to be listed", func() bool {
		var got []runListEntry
		decodeBody(t, doReq(t, s, http.MethodGet, "/api/runs", nil, nil), &got)
		return len(got) == 1
	})
	var got []runListEntry
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/runs", nil, nil), &got)
	if got[0].AgentID != id || got[0].Name != "runner" || string(got[0].Run) != testRunDigest {
		t.Fatalf("entry = %+v", got[0])
	}
}

func TestRunsGetProxiesSessionRun(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agentOf(id).results["session/run"] = json.RawMessage(testRunDigest)
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodGet, "/api/runs/"+id, nil, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != testRunDigest {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if got := agentOf(id).calls("session/run"); len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-1"`) {
		t.Fatalf("session/run params = %v", got)
	}
}

func TestRunsGetUnsupported(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	agentOf(id).handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/run" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/runs/"+id, nil, nil)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "run_unsupported") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunsStartSDDWritesThePlanAndSendsSDDStart(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	root := t.TempDir()
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodPost, "/api/runs", map[string]any{
		"project": root, "kind": "sdd", "plan": "## Task 1: x\n",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		AgentID string `json:"agentId"`
	}
	decodeBody(t, rec, &out)
	calls := agentOf(out.AgentID).calls("session/sdd_start")
	if len(calls) != 1 {
		t.Fatalf("sdd_start sent %d times", len(calls))
	}
	var p struct {
		SessionID string `json:"sessionId"`
		PlanPath  string `json:"planPath"`
	}
	if err := json.Unmarshal([]byte(calls[0]), &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s-1" || !strings.HasPrefix(p.PlanPath, root) {
		t.Fatalf("params = %+v", p)
	}
	data, err := os.ReadFile(p.PlanPath)
	if err != nil || string(data) != "## Task 1: x\n" {
		t.Fatalf("plan file = %q, %v", data, err)
	}
	// The run was spawned isolated, so it cannot touch the checkout.
	if got := agentOf(out.AgentID).calls("session/new"); len(got) != 1 || !strings.Contains(got[0], `"isolation"`) {
		t.Fatalf("session/new = %v", got)
	}
}

func TestRunsStartSDDWithPlanPathOnExistingAgent(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs",
		map[string]any{"agentId": id, "kind": "sdd", "planPath": "/home/u/plan.md"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := agentOf(id).calls("session/sdd_start"); len(got) != 1 || !strings.Contains(got[0], `"planPath":"/home/u/plan.md"`) {
		t.Fatalf("sdd_start = %v", got)
	}
}

func TestRunsStartSwarmSendsSwarmStart(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs",
		map[string]any{"agentId": id, "kind": "swarm", "goal": "fix the flaky test"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := agentOf(id).calls("session/swarm_start")
	if len(got) != 1 || !strings.Contains(got[0], `"goal":"fix the flaky test"`) {
		t.Fatalf("swarm_start = %v", got)
	}
}

func TestRunsStartRejectsBadRequestsWithoutSpawning(t *testing.T) {
	f, _, _ := agentFleet(t)
	s := NewServer(f, "")
	root := t.TempDir()
	for name, body := range map[string]map[string]any{
		"no kind":       {"project": root},
		"sdd no plan":   {"project": root, "kind": "sdd"},
		"swarm no goal": {"project": root, "kind": "swarm", "goal": "  "},
		"bad kind":      {"project": root, "kind": "yolo", "goal": "x"},
	} {
		if rec := doReq(t, s, http.MethodPost, "/api/runs", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	if n := len(f.ws.Agents()); n != 0 {
		t.Fatalf("a rejected request spawned %d agents", n)
	}
}

func TestRunsStartSurfacesAnImmediateRefusal(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	agentOf(id).handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/swarm_start" {
			return nil, &rpcError{Code: -32000, Message: "a turn is already active"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs",
		map[string]any{"agentId": id, "kind": "swarm", "goal": "go"}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "already active") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if got := f.live.get(id).runErr; !strings.Contains(got, "already active") {
		t.Fatalf("runErr = %q", got)
	}
}

func TestRunsStartRecordsALateFailure(t *testing.T) {
	old := runDispatchGrace
	runDispatchGrace = 20 * time.Millisecond
	t.Cleanup(func() { runDispatchGrace = old })

	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	release := make(chan struct{})
	agentOf(id).handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/swarm_start" {
			<-release
			return nil, &rpcError{Code: -32000, Message: "run crashed"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs",
		map[string]any{"agentId": id, "kind": "swarm", "goal": "go"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a run still in flight should be accepted: %d %s", rec.Code, rec.Body.String())
	}
	close(release)
	waitFor(t, 5*time.Second, "late failure recorded", func() bool {
		return strings.Contains(f.live.get(id).runErr, "run crashed")
	})
}

func TestRunsAnswerProxiesSDDAnswer(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs/"+id+"/answer",
		map[string]any{"answer": "yes"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := agentOf(id).calls("session/sdd_answer")
	if len(got) != 1 || !strings.Contains(got[0], `"answer":"yes"`) || !strings.Contains(got[0], `"sessionId":"s-1"`) {
		t.Fatalf("sdd_answer = %v", got)
	}
	if rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/runs/nope/answer",
		map[string]any{"answer": "yes"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", rec.Code)
	}
}
