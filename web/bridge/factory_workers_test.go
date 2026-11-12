package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func parkedFixture(t *testing.T, work json.RawMessage, signalErr error) (*Fleet, FactoryWorker, *captureTransport, string) {
	t.Helper()
	root := t.TempDir()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	w := FactoryWorker{AgentID: "a1", SessionID: "s1", Role: "implementer", SpawnEffectID: "e1"}
	a := Agent{ID: w.AgentID, Project: root, SessionID: w.SessionID, FactoryItemID: "i1", FactoryRole: w.Role, FactorySpawnEffectID: w.SpawnEffectID}
	if err := ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "", nil, t.TempDir(), Limits{MaxConcurrent: 1}, "", nil, "")
	tr := &captureTransport{results: map[string]json.RawMessage{"session/end_work": work}, signalErr: signalErr}
	c := &Child{Transport: tr}
	r := NewRegistry(c)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	r.track(w.SessionID, AgentPath(root))
	f.runtimes[w.AgentID] = &agentRuntime{id: w.AgentID, root: root, sessionID: w.SessionID, child: c, reg: r}
	f.sessionAgent[w.SessionID] = w.AgentID
	if err := f.slots.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f, w, tr, root
}

func TestFactoryParkRetainsWorkspace(t *testing.T) {
	t.Run("confirmed idle retains workspace and restores session", func(t *testing.T) {
		f, w, _, root := parkedFixture(t, json.RawMessage(`{}`), nil)
		obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i1", Workers: []FactoryWorker{w}})
		if err != nil || !obs.ActiveWorkKnown || !obs.WorkIdle {
			t.Fatalf("omitempty empty work must be known idle: %+v, %v", obs, err)
		}
		if err := f.ParkFactoryWorker(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		a, ok := f.ws.Agent(w.AgentID)
		if !ok || !a.FactoryParked || a.Project != root {
			t.Fatalf("saved agent lost: %+v", a)
		}
		if n, _ := f.slots.stats(); n != 0 {
			t.Fatalf("slot retained: %d", n)
		}
		f.newRuntime = func(Agent) (*Child, error) {
			return &Child{Transport: &captureTransport{results: map[string]json.RawMessage{"session/load": json.RawMessage(`{}`)}}}, nil
		}
		if err := f.Resume(context.Background(), w.AgentID); err != nil {
			t.Fatal(err)
		}
		a, _ = f.ws.Agent(w.AgentID)
		if a.FactoryParked {
			t.Fatal("resume did not clear parked flag")
		}
		if n, _ := f.slots.stats(); n != 1 {
			t.Fatalf("resumed slot count: %d", n)
		}
	})
	t.Run("active or unknown work refuses", func(t *testing.T) {
		for _, work := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`{"items":null}`)} {
			f, w, _, _ := parkedFixture(t, work, nil)
			if err := f.ParkFactoryWorker(context.Background(), w); err == nil {
				t.Fatalf("parked with work %s", work)
			}
			obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i1", Workers: []FactoryWorker{w}})
			if err != nil || obs.ActiveWorkKnown || obs.WorkIdle {
				t.Fatalf("observation claims known idle work %s: %+v, %v", work, obs, err)
			}
			if n, _ := f.slots.stats(); n != 1 {
				t.Fatalf("freed slot with work %s", work)
			}
		}
		f, w, _, _ := parkedFixture(t, json.RawMessage(`{"items":[{"id":"job"}]}`), nil)
		if err := f.ParkFactoryWorker(context.Background(), w); err == nil {
			t.Fatal("parked active work")
		}
		obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i1", Workers: []FactoryWorker{w}})
		if err != nil || !obs.ActiveWorkKnown || obs.WorkIdle {
			t.Fatalf("active work observation: %+v, %v", obs, err)
		}
	})
	t.Run("unavailable work snapshot refuses", func(t *testing.T) {
		f, w, tr, _ := parkedFixture(t, json.RawMessage(`{}`), nil)
		tr.errs = map[string]*rpcError{"session/end_work": {Code: -32000, Message: "unavailable"}}
		if err := f.ParkFactoryWorker(context.Background(), w); err == nil {
			t.Fatal("parked without work evidence")
		}
		obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i1", Workers: []FactoryWorker{w}})
		if err != nil || obs.ActiveWorkKnown || obs.WorkIdle {
			t.Fatalf("unavailable work observation: %+v, %v", obs, err)
		}
	})
	t.Run("stop failure retains capacity", func(t *testing.T) {
		f, w, _, _ := parkedFixture(t, json.RawMessage(`{"items":[]}`), errors.New("stop failed"))
		if err := f.ParkFactoryWorker(context.Background(), w); err == nil {
			t.Fatal("stop failure ignored")
		}
		if n, _ := f.slots.stats(); n != 1 {
			t.Fatalf("freed slot after stop failure: %d", n)
		}
	})
}

func TestFactoryResumeLoadFailureRetainsWorkspaceAndCapacity(t *testing.T) {
	f, w, _, root := parkedFixture(t, json.RawMessage(`{"items":[]}`), nil)
	if err := f.ParkFactoryWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	f.newRuntime = func(Agent) (*Child, error) {
		return &Child{Transport: &captureTransport{errs: map[string]*rpcError{"session/load": {Code: -32000, Message: "cannot load"}}}}, nil
	}
	if err := f.Resume(context.Background(), w.AgentID); err == nil {
		t.Fatal("saved session load failure ignored")
	}
	if n, _ := f.slots.stats(); n != 0 {
		t.Fatalf("failed load retained slot: %d", n)
	}
	if _, err := f.runtimeForAgent(w.AgentID); err == nil {
		t.Fatal("failed load left live runtime addressable")
	}
	a, ok := f.ws.Agent(w.AgentID)
	if !ok || !a.FactoryParked || a.Project != root {
		t.Fatalf("retained workspace lost: %+v", a)
	}
	f.newRuntime = func(Agent) (*Child, error) {
		return &Child{Transport: &captureTransport{results: map[string]json.RawMessage{"session/load": json.RawMessage(`{}`)}}}, nil
	}
	if err := f.Resume(context.Background(), w.AgentID); err != nil {
		t.Fatalf("retry load: %v", err)
	}
}

func TestFactoryResumeLoadFailureStopUnconfirmed(t *testing.T) {
	f, w, _, _ := parkedFixture(t, json.RawMessage(`{"items":[]}`), nil)
	if err := f.ParkFactoryWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	f.newRuntime = func(Agent) (*Child, error) {
		return &Child{Transport: &captureTransport{errs: map[string]*rpcError{"session/load": {Code: -32000, Message: "cannot load"}}, signalErr: errors.New("stop failed")}}, nil
	}
	if err := f.Resume(context.Background(), w.AgentID); err == nil {
		t.Fatal("failed load and stop reported success")
	}
	if n, _ := f.slots.stats(); n != 1 {
		t.Fatalf("unconfirmed stop freed slot: %d", n)
	}
	if err := f.Resume(context.Background(), w.AgentID); err == nil {
		t.Fatal("uncertain runtime reported as successfully resumed")
	}
	a, _ := f.ws.Agent(w.AgentID)
	if !a.FactoryParked {
		t.Fatal("uncertain resumed worker marked active")
	}
}

func TestFactoryPromptFreshFinalAndStopReason(t *testing.T) {
	for _, tc := range []struct {
		name, reason                string
		fresh, emptyFinal, accepted bool
	}{
		{"new end turn", "end_turn", true, false, true},
		{"previous turn report", "end_turn", false, false, false},
		{"new empty final after old valid report", "end_turn", true, true, false},
		{"cancelled", "cancelled", true, false, false},
		{"max tokens", "max_tokens", true, false, false},
		{"unknown reason", "provider_limit", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w, tr, _ := parkedFixture(t, json.RawMessage(`{}`), nil)
			message := "```json\n{\"operationId\":\"op1\",\"outcome\":\"needs_input\",\"question\":\"Need input\",\"reason\":\"infrastructure_suspected\"}\n```"
			old, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"id": "final:old", "kind": "final", "message": map[string]any{"content": message}}}})
			fresh, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"id": "final:old", "kind": "final", "message": map[string]any{"content": message}}, map[string]any{"id": "final:new", "kind": "final", "message": map[string]any{"content": message}}}})
			if tc.emptyFinal {
				fresh, _ = json.Marshal(map[string]any{"nodes": []any{map[string]any{"id": "final:old", "kind": "final", "message": map[string]any{"content": message}}, map[string]any{"id": "final:new", "kind": "final"}}})
			}
			tr.stackResult = old
			tr.results = map[string]json.RawMessage{"session/prompt": json.RawMessage(`{"stopReason":"` + tc.reason + `"}`)}
			tr.onRequest = func(method string) error {
				if method == "session/prompt" && tc.fresh {
					tr.stackResult = fresh
				}
				return nil
			}
			item := FactoryItem{ID: "i1", Approvals: []FactoryApproval{{Plan: "plan", Spec: "spec"}}}
			effect := FactoryEffect{ID: "op1", Kind: "implement", Worker: &w}
			result, err := newFactoryWorkers(f).Execute(context.Background(), item, effect)
			if (err == nil) != tc.accepted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.accepted && (result.Report == nil || result.Report.Outcome != "needs_input" || result.StopReason != "end_turn") {
				t.Fatalf("wrong accepted result: %+v", result)
			}
		})
	}
}
