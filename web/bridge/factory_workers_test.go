package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
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

// factoryObservationIdle is the idle conjunction the safe boundary reads, stated
// as one value so a test asserts the whole of it rather than one flag.
func factoryObservationIdle(o FactoryObservation) bool {
	return o.RuntimeKnown && o.TurnIdle && o.ActiveWorkKnown && o.WorkIdle
}

// observerFixture is the smallest workspace Observe can read: a saved workspace
// file and the fleet over it, with no runtime started. It is enough for every
// shape whose workers are all parked, because a parked worker has no runtime.
func observerFixture(t *testing.T) (*Fleet, *Workspace) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	f := NewFleet(ws, "", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(f.Close)
	return f, ws
}

// observerWorker puts the agent a worker row names into the registry, which is
// the only thing Observe matches a row against.
func observerWorker(t *testing.T, ws *Workspace, w FactoryWorker, itemID string, parked bool) {
	t.Helper()
	err := ws.PutAgent(Agent{
		ID: w.AgentID, Project: t.TempDir(), SessionID: w.SessionID,
		OwnerID: DefaultOwnerID, Origin: OriginUI,
		FactoryItemID: itemID, FactoryRole: w.Role, FactorySpawnEffectID: w.SpawnEffectID,
		FactoryParked: parked,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFactoryWorkersObserveParkedShortcut drives the production Observe over
// real workspace agents and pins what its parked shortcut may claim.
//
// The shortcut exists because a parked worker has no runtime to contribute a
// turn or active-work reading, so when every worker the registry can account for
// is parked there is nothing to be busy. It may only speak for rows it can
// account for: a row with no agent behind it, which the item's own record does
// not already record as parked, may be running, and the shortcut must not call
// that item known-idle.
func TestFactoryWorkersObserveParkedShortcut(t *testing.T) {
	implementer := FactoryWorker{AgentID: "a-impl", SessionID: "s-impl", Role: "implementer", SpawnEffectID: "spawn-1"}

	t.Run("every matched worker parked is known idle", func(t *testing.T) {
		f, ws := observerFixture(t)
		observerWorker(t, ws, implementer, "i-1", true)
		obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i-1", Workers: []FactoryWorker{implementer}})
		if err != nil {
			t.Fatal(err)
		}
		if !factoryObservationIdle(obs) {
			t.Fatalf("a fully parked item is not known idle: %+v", obs)
		}
	})

	t.Run("live worker with active work is not idle", func(t *testing.T) {
		// A real runtime makes the flags evidence rather than a default: the
		// turn reads idle while the worker's running work does not, so only the
		// work half of the conjunction keeps the item off a safe boundary.
		f, w, _, _ := parkedFixture(t, json.RawMessage(`{"items":[{"id":"job"}]}`), nil)
		obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{ID: "i1", Workers: []FactoryWorker{w}})
		if err != nil {
			t.Fatal(err)
		}
		if !obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || obs.WorkIdle {
			t.Fatalf("the live worker's evidence is not what was stated: %+v", obs)
		}
		if factoryObservationIdle(obs) {
			t.Fatalf("an item running active work was reported idle: %+v", obs)
		}
	})

	for _, tc := range []struct {
		name      string
		rowParked bool
		wantIdle  bool
	}{
		// The regression guard: the shortcut must not over-claim for a row the
		// registry cannot match and the journal does not record as parked.
		{"unaccounted row not recorded as parked is not idle", false, false},
		// The row survives its agent being pruned; without this the shortcut
		// would never fire and a ready item could not reach a safe boundary.
		{"unaccounted row recorded as parked is idle", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ws := observerFixture(t)
			observerWorker(t, ws, implementer, "i-1", true)
			// No agent exists for this row at all: it was pruned after the item
			// recorded it.
			gone := FactoryWorker{
				AgentID: "a-gone", SessionID: "s-gone", Role: "reviewer",
				SpawnEffectID: "spawn-2", Parked: tc.rowParked,
			}
			obs, err := newFactoryWorkers(f).Observe(context.Background(), FactoryItem{
				ID: "i-1", Workers: []FactoryWorker{implementer, gone},
			})
			if err != nil {
				t.Fatal(err)
			}
			if idle := factoryObservationIdle(obs); idle != tc.wantIdle {
				t.Fatalf("known idle = %v, want %v: %+v", idle, tc.wantIdle, obs)
			}
			if !tc.wantIdle && (obs.RuntimeKnown || obs.TurnIdle || obs.ActiveWorkKnown || obs.WorkIdle) {
				t.Fatalf("the shortcut claimed a flag for a row it cannot account for: %+v", obs)
			}
		})
	}
}

// TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting proves the routing
// source the factory relies on: a worker reads the latest approval's route and
// it reaches the agent verbatim in the session/new frame.
//
// The spawn is driven through the real Fleet.spawn, so the fleet is extended
// with what that path resolves before session/new — a registered repository,
// pointed at a local bare repository so the clone is offline, and a runtime
// whose transport records the frames the bridge sends.
func TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting(t *testing.T) {
	bare := newBareRepoFixture(t)
	base := gitText(t, bare, "rev-parse", "main")
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutRepo(Repo{ID: "repo-1", URL: bare, Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "", nil, t.TempDir(), Limits{}, "", nil, "")
	tr := &captureTransport{}
	f.newRuntime = func(Agent) (*Child, error) { return &Child{Transport: tr}, nil }
	t.Cleanup(f.Close)
	reviewerRoute := json.RawMessage(`{"profile":"cheap-review"}`)
	item := FactoryItem{
		ID:      "item-1",
		RepoID:  "repo-1",
		BaseSHA: base,
		Approvals: []FactoryApproval{{Revision: 1, Plan: "plan", Spec: "spec", PlanHash: FactoryPlanHash("plan", "spec"), Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              t.TempDir(),
			TargetBranch:             "main",
			Native:                   true,
			ImplementerRouting:       json.RawMessage(`{"profile":"frontier"}`),
			ReviewerRouting:          reviewerRoute,
			NoRequiredCIAcknowledged: true,
			MaxRounds:                5,
		}}},
		Rounds: []FactoryRound{{Number: 1, ApprovalRevision: 1, HeadSHA: base}},
	}
	if _, err := f.SpawnFactoryWorker(context.Background(), item, "reviewer", "spawn-1"); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	frames := append([]capturedFrame(nil), tr.seen...)
	tr.mu.Unlock()
	var params map[string]any
	for _, frame := range frames {
		if frame.method != "session/new" {
			continue
		}
		if err := json.Unmarshal([]byte(frame.params), &params); err != nil {
			t.Fatalf("decode session/new params: %v", err)
		}
	}
	if params == nil {
		t.Fatalf("no session/new frame was sent: %+v", frames)
	}
	routing, _ := params["routing"].(map[string]any)
	if routing["profile"] != "cheap-review" {
		t.Fatalf("session/new routing = %v, want the reviewer route from the latest approval", params["routing"])
	}
}

// factorySubagentWorkerFixture is the smallest world a factory worker spawn
// needs: a registered repository it can clone offline, one runtime per agent
// whose session/new frames a test can read, and a control agent that reports
// whether the runtime binds per-session child settings.
type factorySubagentWorkerFixture struct {
	f   *Fleet
	ctl *fakeAgent
	// mu guards runtimes, which the spawn path records as it starts each agent.
	mu       sync.Mutex
	runtimes map[string]*captureTransport
	base     string
	root     string
}

func newFactorySubagentWorkerFixture(t *testing.T) *factorySubagentWorkerFixture {
	t.Helper()
	bare := newBareRepoFixture(t)
	fx := &factorySubagentWorkerFixture{
		runtimes: map[string]*captureTransport{},
		base:     gitText(t, bare, "rev-parse", "main"),
		root:     t.TempDir(),
	}
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutRepo(Repo{ID: "repo-1", URL: bare, Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	fx.f = NewFleet(ws, "", nil, t.TempDir(), Limits{}, "", nil, "")
	fx.ctl = newFakeAgent()
	fx.f.newControl = func() (*Child, error) { return &Child{Transport: fx.ctl}, nil }
	fx.f.newRuntime = func(a Agent) (*Child, error) {
		tr := &captureTransport{results: map[string]json.RawMessage{
			"session/new": json.RawMessage(`{"sessionId":"session-` + a.ID + `"}`),
		}}
		fx.mu.Lock()
		fx.runtimes[a.ID] = tr
		fx.mu.Unlock()
		return &Child{Transport: tr}, nil
	}
	t.Cleanup(fx.f.Close)
	return fx
}

// reportSettings is the control session's session/status answer: whether the
// runtime binds per-session child settings, and the ordinary defaults it would
// apply to a session that bound none.
func (fx *factorySubagentWorkerFixture) reportSettings(t *testing.T, settings bool, defaults FactorySubagentSettings) {
	t.Helper()
	fx.ctl.results["session/status"] = map[string]any{
		"subagentControls": map[string]any{"settings": settings},
		"subagentDefaults": map[string]any{
			"settings": map[string]any{"enabled": defaults.Enabled, "model": defaults.Model, "concurrency": defaults.Concurrency},
			"digest":   "digest",
		},
	}
}

// silenceSettings is a runtime that does not report the capability at all,
// which is what an agent that does not know session/status answers.
func (fx *factorySubagentWorkerFixture) silenceSettings() { delete(fx.ctl.results, "session/status") }

// approval is one approved revision carrying the two role snapshots a test
// states, with the configuration a worker spawn needs.
func (fx *factorySubagentWorkerFixture) approval(impl, rev FactorySubagentSettings) FactoryApproval {
	return FactoryApproval{
		Plan: "plan", Spec: "spec", PlanHash: FactoryPlanHash("plan", "spec"),
		Config: FactoryConfig{
			RepoID: "repo-1", ProjectRoot: fx.root, TargetBranch: "main", Native: true,
			ImplementerRouting:       json.RawMessage(`{"profile":"frontier"}`),
			ReviewerRouting:          json.RawMessage(`{"profile":"cheap-review"}`),
			NoRequiredCIAcknowledged: true,
			MaxRounds:                5,
		},
		ImplementerSubagents: impl,
		ReviewerSubagents:    rev,
	}
}

// item is one approved handoff whose approvals are numbered in the order given,
// which is the order an approval is appended in.
func (fx *factorySubagentWorkerFixture) item(id string, approvals ...FactoryApproval) FactoryItem {
	for n := range approvals {
		approvals[n].Revision = uint64(n + 1)
	}
	return FactoryItem{
		ID: id, RepoID: "repo-1", BaseSHA: fx.base, Approvals: approvals,
		Branch: "marshal/factory/" + id,
		Rounds: []FactoryRound{{Number: 1, ApprovalRevision: uint64(len(approvals)), HeadSHA: fx.base}},
	}
}

// recoverableWorker saves the agent one worker row names over a real checkout on
// the item's branch, which is the state the fresh-session path verifies before
// it recreates a session. bound carries the identity the worker had before it
// was lost.
func (fx *factorySubagentWorkerFixture) recoverableWorker(t *testing.T, item FactoryItem, role string, bound FactoryWorker) FactoryWorker {
	t.Helper()
	project := t.TempDir()
	mustGit(t, project, "init", "--initial-branch="+item.Branch)
	mustGit(t, project, "config", "user.email", "test@example.com")
	mustGit(t, project, "config", "user.name", "Test")
	mustGit(t, project, "commit", "--allow-empty", "-m", "seed")
	worker := bound
	worker.AgentID, worker.SessionID = "a-"+role, "session-lost-"+role
	worker.Role, worker.SpawnEffectID = role, "spawn-1"
	if err := fx.f.ws.PutAgent(Agent{
		ID: worker.AgentID, Project: project, SessionID: worker.SessionID,
		OwnerID: DefaultOwnerID, Origin: OriginUI,
		FactoryItemID: item.ID, FactoryRole: role, FactorySpawnEffectID: worker.SpawnEffectID,
	}); err != nil {
		t.Fatal(err)
	}
	return worker
}

// sentSettings returns the subagentSettings one worker's session/new carried,
// and whether the frame carried the key at all.
func (fx *factorySubagentWorkerFixture) sentSettings(t *testing.T, agentID string) (map[string]any, bool) {
	t.Helper()
	fx.mu.Lock()
	tr := fx.runtimes[agentID]
	fx.mu.Unlock()
	if tr == nil {
		t.Fatalf("no runtime was started for agent %s", agentID)
	}
	tr.mu.Lock()
	frames := append([]capturedFrame(nil), tr.seen...)
	tr.mu.Unlock()
	var params map[string]any
	for _, frame := range frames {
		if frame.method != "session/new" {
			continue
		}
		if err := json.Unmarshal([]byte(frame.params), &params); err != nil {
			t.Fatalf("decode session/new params %s: %v", frame.params, err)
		}
	}
	if params == nil {
		t.Fatalf("agent %s was sent no session/new: %+v", agentID, frames)
	}
	settings, ok := params["subagentSettings"].(map[string]any)
	return settings, ok
}

// wantSubagentSettings pins a frame's subagentSettings to the snapshot a role's
// approval froze, and to the shape the session accepts.
func wantSubagentSettings(t *testing.T, got map[string]any, want FactorySubagentSettings) {
	t.Helper()
	if got == nil {
		t.Fatalf("session/new carried no subagentSettings, want %+v", want)
	}
	for key := range got {
		switch key {
		case "enabled", "model", "concurrency":
		default:
			t.Fatalf("subagentSettings carried %q, which the session does not accept", key)
		}
	}
	if enabled, _ := got["enabled"].(bool); enabled != want.Enabled {
		t.Fatalf("subagentSettings enabled = %v, want %v", got["enabled"], want.Enabled)
	}
	model, _ := got["model"].(string)
	if model != want.Model {
		t.Fatalf("subagentSettings model = %v, want %q", got["model"], want.Model)
	}
	concurrency, _ := got["concurrency"].(float64)
	if int(concurrency) != want.Concurrency {
		t.Fatalf("subagentSettings concurrency = %v, want %d", got["concurrency"], want.Concurrency)
	}
}

// TestFactoryWorkerAppliesApprovedSubagents proves a spawned worker binds the
// snapshot its own role's approval froze, rather than the runtime's global
// defaults, and that an approval that froze nothing still sends nothing.
func TestFactoryWorkerAppliesApprovedSubagents(t *testing.T) {
	t.Run("a spawn applies the settings its approval froze", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		// The runtime's own defaults are not what was approved, so a fallback
		// to them is visible in the frame.
		fx.reportSettings(t, true, FactorySubagentSettings{Enabled: true, Model: "test/default", Concurrency: 8})
		approved := FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
		item := fx.item("i-1", fx.approval(approved, FactorySubagentSettings{Enabled: false, Concurrency: 1}))
		worker, err := fx.f.SpawnFactoryWorker(context.Background(), item, "implementer", "spawn-1")
		if err != nil {
			t.Fatal(err)
		}
		settings, ok := fx.sentSettings(t, worker.AgentID)
		if !ok {
			t.Fatal("session/new carried no subagentSettings")
		}
		wantSubagentSettings(t, settings, approved)
		if worker.Subagents != approved {
			t.Fatalf("worker bound %+v, want the approved %+v", worker.Subagents, approved)
		}
		if worker.ApprovalRevision != 1 {
			t.Fatalf("worker bound approval revision %d, want 1", worker.ApprovalRevision)
		}
	})

	t.Run("a later approval replaces the settings a worker binds", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		fx.reportSettings(t, true, FactorySubagentSettings{Enabled: true, Concurrency: 4})
		first := FactorySubagentSettings{Enabled: true, Model: "test/first", Concurrency: 2}
		second := FactorySubagentSettings{Enabled: false, Concurrency: 3}
		item := fx.item("i-1",
			fx.approval(first, FactorySubagentSettings{}),
			fx.approval(second, FactorySubagentSettings{}))
		worker, err := fx.f.SpawnFactoryWorker(context.Background(), item, "implementer", "spawn-1")
		if err != nil {
			t.Fatal(err)
		}
		settings, ok := fx.sentSettings(t, worker.AgentID)
		if !ok {
			t.Fatal("session/new carried no subagentSettings")
		}
		wantSubagentSettings(t, settings, second)
		if worker.ApprovalRevision != 2 || worker.Subagents != second {
			t.Fatalf("worker bound revision %d settings %+v, want revision 2 settings %+v",
				worker.ApprovalRevision, worker.Subagents, second)
		}
	})

	t.Run("an approval that froze nothing sends nothing", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		// A legacy approval never asks for the capability, so a runtime that
		// reports none is still enough to spawn on.
		fx.silenceSettings()
		item := fx.item("i-1", fx.approval(FactorySubagentSettings{}, FactorySubagentSettings{}))
		worker, err := fx.f.SpawnFactoryWorker(context.Background(), item, "implementer", "spawn-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := fx.sentSettings(t, worker.AgentID); ok {
			t.Fatal("a legacy approval sent subagentSettings")
		}
		if worker.Subagents != (FactorySubagentSettings{}) {
			t.Fatalf("a legacy worker bound %+v", worker.Subagents)
		}
	})

	t.Run("a runtime that cannot bind the settings refuses before any worker starts", func(t *testing.T) {
		approved := FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
		for _, tc := range []struct {
			name  string
			setup func(*factorySubagentWorkerFixture, *testing.T)
		}{
			{"no session status at all", func(fx *factorySubagentWorkerFixture, _ *testing.T) { fx.silenceSettings() }},
			{"status without subagent controls", func(fx *factorySubagentWorkerFixture, t *testing.T) {
				fx.reportSettings(t, true, FactorySubagentSettings{Enabled: true, Concurrency: 4})
				delete(fx.ctl.results["session/status"].(map[string]any), "subagentControls")
			}},
			{"settings reported false", func(fx *factorySubagentWorkerFixture, t *testing.T) {
				fx.reportSettings(t, false, FactorySubagentSettings{Enabled: true, Concurrency: 4})
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fx := newFactorySubagentWorkerFixture(t)
				tc.setup(fx, t)
				item := fx.item("i-1", fx.approval(approved, FactorySubagentSettings{}))
				if _, err := fx.f.SpawnFactoryWorker(context.Background(), item, "implementer", "spawn-1"); !errors.Is(err, ErrFactoryUnsupported) {
					t.Fatalf("spawn err = %v, want ErrFactoryUnsupported", err)
				}
				// Nothing started: no runtime, no slot.
				fx.mu.Lock()
				started := len(fx.runtimes)
				fx.mu.Unlock()
				if started != 0 {
					t.Fatalf("refused spawn started %d runtimes", started)
				}
				if n, _ := fx.f.slots.stats(); n != 0 {
					t.Fatalf("refused spawn took %d slots", n)
				}
			})
		}
	})
}

// TestFactoryWorkerSubagentSettingsAreRoleScoped proves each role's session
// binds that role's own snapshot: an implementer never runs under the reviewer's
// settings, and neither spawn writes the other role's approval.
func TestFactoryWorkerSubagentSettingsAreRoleScoped(t *testing.T) {
	fx := newFactorySubagentWorkerFixture(t)
	fx.reportSettings(t, true, FactorySubagentSettings{Enabled: true, Concurrency: 4})
	implementerSettings := FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
	reviewerSettings := FactorySubagentSettings{Enabled: false, Model: "test/cheap", Concurrency: 6}
	item := fx.item("i-1", fx.approval(implementerSettings, reviewerSettings))
	ctx := context.Background()
	implementer, err := fx.f.SpawnFactoryWorker(ctx, item, "implementer", "spawn-1")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := fx.f.SpawnFactoryWorker(ctx, item, "reviewer", "spawn-2")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fx.sentSettings(t, implementer.AgentID)
	if !ok {
		t.Fatal("the implementer session carried no subagentSettings")
	}
	wantSubagentSettings(t, got, implementerSettings)
	got, ok = fx.sentSettings(t, reviewer.AgentID)
	if !ok {
		t.Fatal("the reviewer session carried no subagentSettings")
	}
	wantSubagentSettings(t, got, reviewerSettings)
	if implementer.Subagents != implementerSettings || reviewer.Subagents != reviewerSettings {
		t.Fatalf("bound settings implementer=%+v reviewer=%+v", implementer.Subagents, reviewer.Subagents)
	}
	// Neither spawn rewrote the approval it read: the frozen pair is what each
	// worker was measured against.
	if item.Approvals[0].ImplementerSubagents != implementerSettings || item.Approvals[0].ReviewerSubagents != reviewerSettings {
		t.Fatalf("the approval was rewritten: %+v", item.Approvals[0])
	}
}

// TestFactoryWorkerBoundSnapshotKeepsLegacyRecordsByteIdentical proves the two
// fields a worker now binds do not change the record a worker that bound
// nothing writes. The journal replays by re-marshalling each record and
// comparing it with the stored bytes, so this is what keeps an older journal
// loadable.
func TestFactoryWorkerBoundSnapshotKeepsLegacyRecordsByteIdentical(t *testing.T) {
	legacy := func() json.RawMessage {
		raw, err := json.Marshal(struct {
			AgentID       string `json:"agentId"`
			SessionID     string `json:"sessionId"`
			Role          string `json:"role"`
			SpawnEffectID string `json:"spawnEffectId"`
			Parked        bool   `json:"parked"`
		}{"a1", "s1", "implementer", "spawn-1", false})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	unbound := FactoryWorker{AgentID: "a1", SessionID: "s1", Role: "implementer", SpawnEffectID: "spawn-1"}
	raw, err := json.Marshal(unbound)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(legacy) {
		t.Fatalf("unbound worker record = %s, want %s", raw, legacy)
	}
	// A bound worker states the snapshot it runs under, and reads back as one.
	bound := unbound
	bound.ApprovalRevision, bound.Subagents = 2, FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
	raw, err = json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	var back FactoryWorker
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back != bound {
		t.Fatalf("bound worker round-tripped as %+v, want %+v", back, bound)
	}
}
