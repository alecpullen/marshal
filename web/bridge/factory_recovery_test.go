package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFactoryRecoveryLostOutcomes(t *testing.T) {
	i := loopItem()
	i.State = "implementing"
	i.Effects = []FactoryEffect{{ID: "prompt-1", Kind: "implement", Status: "dispatched"}}
	c := NewFactoryController(nil, nil, nil)
	if _, ok := c.recoveryOutcome(i, i.Effects[0], FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}); ok {
		t.Fatal("idle prompt must not be accepted as completed")
	}
	spawn := FactoryEffect{ID: "spawn-id", Kind: "spawn", Status: "dispatched", WorkerRole: "implementer"}
	duplicate := FactoryWorker{AgentID: "one", SessionID: "s1", Role: "implementer", SpawnEffectID: spawn.ID}
	second := duplicate
	second.AgentID, second.SessionID = "two", "s2"
	if _, ok := c.recoveryOutcome(i, spawn, FactoryObservation{Workers: []FactoryWorker{duplicate, second}}); ok {
		t.Fatal("duplicate live workers accepted")
	}
	for _, tc := range []struct {
		name        string
		observation FactoryObservation
		want        bool
	}{
		{"exact pushed head", FactoryObservation{HeadSHA: "new", TreeSHA: "tree", RemoteHead: "new"}, true},
		{"remote still old", FactoryObservation{HeadSHA: "new", TreeSHA: "tree", RemoteHead: "old"}, false},
		{"remote diverged", FactoryObservation{HeadSHA: "new", TreeSHA: "tree", RemoteHead: "foreign"}, false},
		{"dirty checkout", FactoryObservation{HeadSHA: "new", TreeSHA: "tree", RemoteHead: "new", Dirty: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := FactoryEffect{ID: "push", Kind: "publish", Status: "dispatched", ExpectedHead: "old"}
			out, ok := c.recoveryOutcome(i, e, tc.observation)
			if ok != tc.want {
				t.Fatalf("publish accepted=%v, want %v", ok, tc.want)
			}
			if ok {
				item := factoryCopy(i)
				item.Effects = []FactoryEffect{e}
				got, err := CompleteFactoryEffect(item, e.ID, out, time.Now())
				if err != nil || got.Effects[0].Status != "completed" || got.Effects[0].Outcome.HeadSHA != "new" {
					t.Fatalf("reconciled publish: %+v %v", got, err)
				}
			}
		})
	}
	i.Approvals[0].Config.TargetBranch = "main"
	p := FactoryPR{Number: 7, URL: "https://example.test/pr/7", HeadRef: i.Branch, BaseRef: "main", HeadSHA: "new", BaseSHA: "base", State: "open", Draft: true}
	for _, tc := range []struct {
		name string
		pr   FactoryPR
		want bool
	}{
		{"exact draft PR", p, true},
		{"wrong head branch", func() FactoryPR { q := p; q.HeadRef = "foreign"; return q }(), false},
		{"wrong base branch", func() FactoryPR { q := p; q.BaseRef = "other"; return q }(), false},
		{"ready PR", func() FactoryPR { q := p; q.Draft = false; return q }(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := FactoryEffect{ID: "create", Kind: "create_pr", Status: "dispatched"}
			out, ok := c.recoveryOutcome(i, e, FactoryObservation{PR: &tc.pr})
			if ok != tc.want {
				t.Fatalf("PR accepted=%v, want %v", ok, tc.want)
			}
			if ok {
				item := factoryCopy(i)
				item.Effects = []FactoryEffect{e}
				got, err := CompleteFactoryEffect(item, e.ID, out, time.Now())
				if err != nil || len(got.PRs) != 1 || got.PRs[0].Number != 7 {
					t.Fatalf("reconciled PR: %+v %v", got, err)
				}
			}
		})
	}
}

func TestFactoryRecoveryParkedAndStopped(t *testing.T) {
	i := loopItem()
	i.State = "stopping"
	i.Workers = []FactoryWorker{{AgentID: "worker", Role: "implementer"}}
	if recoveryStopConfirmed(i, FactoryObservation{RuntimeKnown: true, TurnIdle: true}) {
		t.Fatal("detached work is unknown")
	}
	if !recoveryStopConfirmed(i, FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}) {
		t.Fatal("confirmed idle stop did not settle")
	}
	f := testFleetWithVersion(t, "dev")
	factoryAgent := Agent{ID: "parked-history", Project: t.TempDir(), FactoryItemID: "factory-item", FactoryParked: true, SessionID: "session"}
	if err := f.ws.PutAgent(factoryAgent); err != nil {
		t.Fatal(err)
	}
	starts := 0
	f.newRuntime = func(Agent) (*Child, error) { starts++; return nil, errors.New("parked worker restarted") }
	if errs := f.ReattachAll(context.Background()); len(errs) != 0 || starts != 0 {
		t.Fatalf("parked startup: starts=%d errors=%v", starts, errs)
	}
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	created, err = s.Update(created.ID, "parked", created.Version, json.RawMessage(`{"kind":"parked"}`), func(dst *FactoryItem) error {
		dst.State = "checking"
		dst.Workers = []FactoryWorker{{AgentID: "parked-history", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn", Parked: true}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fx := &recoveryEffects{observation: FactoryObservation{Workers: created.Workers}}
	c := NewFactoryController(s, fx, time.Now)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(created.ID)
	if got.State != "checking" || fx.calls != 0 || starts != 0 {
		t.Fatalf("parked worker restarted or prompted: %+v calls=%d starts=%d", got, fx.calls, starts)
	}
	tr := &captureTransport{results: map[string]json.RawMessage{"session/load": json.RawMessage(`{}`)}}
	prompts := 0
	tr.onRequest = func(method string) error {
		if method == "session/prompt" {
			prompts++
		}
		return nil
	}
	f.newRuntime = func(Agent) (*Child, error) { return &Child{Transport: tr}, nil }
	if err := f.Resume(context.Background(), factoryAgent.ID); err != nil {
		t.Fatalf("reconnect saved session: %v", err)
	}
	reconnected, _ := f.ws.Agent(factoryAgent.ID)
	if reconnected.SessionID != "session" || reconnected.FactoryParked || prompts != 0 {
		t.Fatalf("reconnect replayed prompt or changed session: %+v prompts=%d", reconnected, prompts)
	}
	lockDir := t.TempDir()
	first, err := OpenFactoryStore(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenFactoryStore(lockDir)
	if !errors.Is(err, ErrFactoryLocked) || !strings.Contains(err.Error(), "confirm previous bridge stopped") {
		t.Fatalf("stale lock procedure missing: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenFactoryStore(lockDir)
	if err != nil {
		t.Fatalf("lock not released by owner: %v", err)
	}
	second.Close()
}

func TestFactoryRecoveryReportStopReasonEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, stopReason, operationID string
		accepted                      bool
	}{
		{"matching end turn", "end_turn", "effect-1", true},
		{"cancelled turn", "cancelled", "effect-1", false},
		{"stale report", "end_turn", "other-effect", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, worker, tr, _ := parkedFixture(t, json.RawMessage(`{}`), nil)
			message := "```json\n" + `{"operationId":"` + tc.operationID + `","outcome":"needs_input","question":"Need input","reason":"infrastructure_suspected"}` + "\n```"
			tr.stackResult = json.RawMessage(`{"nodes":[]}`)
			tr.results = map[string]json.RawMessage{"session/prompt": json.RawMessage(`{"stopReason":"` + tc.stopReason + `"}`)}
			tr.onRequest = func(method string) error {
				if method == "session/prompt" {
					raw, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"id": "new-final", "kind": "final", "message": map[string]any{"content": message}}}})
					tr.stackResult = raw
				}
				return nil
			}
			item := FactoryItem{ID: "i1", Approvals: []FactoryApproval{{Plan: "plan", Spec: "spec"}}}
			effect := FactoryEffect{ID: "effect-1", Kind: "implement", Worker: &worker}
			out, err := newFactoryWorkers(f).Execute(context.Background(), item, effect)
			if (err == nil) != tc.accepted || (tc.accepted && (out.StopReason != "end_turn" || out.Report == nil || out.Report.OperationID != effect.ID)) {
				t.Fatalf("accepted=%v output=%+v err=%v", tc.accepted, out, err)
			}
		})
	}
}

func TestFactoryRecoveryStoppingWaitsForWork(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "stopping", i.Version, json.RawMessage(`{"kind":"stopping"}`), func(dst *FactoryItem) error {
		dst.State = "stopping"
		dst.Workers = []FactoryWorker{{AgentID: "known", SessionID: "s1", Role: "implementer", SpawnEffectID: "spawn"}}
		dst.Effects = []FactoryEffect{{ID: "prompt", Kind: "implement", Status: "dispatched"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fx := &recoveryEffects{observation: FactoryObservation{RuntimeKnown: true, TurnIdle: true}}
	c := NewFactoryController(s, fx, time.Now)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	unknown, _ := s.Get(i.ID)
	if unknown.State != "stopping" || unknown.Effects[0].Status != "dispatched" {
		t.Fatalf("stop settled unknown work: %+v", unknown)
	}
	fx.observation = FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}
	resolved, err := c.Command(context.Background(), i.ID, FactoryCommand{Kind: "reconcile", RequestID: "stop-reconcile", ExpectedVersion: unknown.Version})
	if err != nil || resolved.State != "stopping" || resolved.Effects[0].Status != "completed" || resolved.Effects[0].Outcome == nil || resolved.Effects[0].Outcome.Confirmed {
		t.Fatalf("stop did not resolve observed idle work: %+v %v", resolved, err)
	}
}

type recoveryEffects struct {
	observation FactoryObservation
	calls       int
}

func (f *recoveryEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return f.observation, nil
}
func (f *recoveryEffects) Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error) {
	f.calls++
	return FactoryOutcome{}, nil
}

func TestFactoryRecoveryDispatchGateAndExactSpawn(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "prepare-recovery", i.Version, json.RawMessage(`{"kind":"prepare"}`), func(dst *FactoryItem) error {
		dst.State = "preparing"
		dst.Approvals = loopItem().Approvals
		dst.Branch = "marshal/factory/" + dst.ID
		dst.Effects = []FactoryEffect{{ID: "spawn-id", Kind: "spawn", WorkerRole: "implementer", Status: "dispatched"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryEffects{observation: FactoryObservation{Workers: []FactoryWorker{{AgentID: "exact", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn-id"}}}}
	c := NewFactoryController(s, f, time.Now)
	if c.isReady() {
		t.Fatal("gate opened before recovery")
	}
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(i.ID)
	if !c.isReady() || len(got.Workers) != 1 || got.Workers[0].AgentID != "exact" || got.Effects[0].Status != "completed" || f.calls != 0 {
		t.Fatalf("recovery: ready=%v workers=%+v effects=%+v calls=%d", c.isReady(), got.Workers, got.Effects, f.calls)
	}
}

func TestFactoryRecoveryUnknownPromptRemainsBlocked(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "prepare-prompt", i.Version, json.RawMessage(`{"kind":"prepare"}`), func(dst *FactoryItem) error {
		dst.State = "implementing"
		dst.Effects = []FactoryEffect{{ID: "prompt-id", Kind: "implement", Status: "dispatched"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryEffects{observation: FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}}
	c := NewFactoryController(s, f, time.Now)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(i.ID)
	if got.State != "recovering" || got.Effects[0].Status != "dispatched" || got.Decision == nil || f.calls != 0 {
		t.Fatalf("unknown prompt replayed or lost: %+v", got)
	}
	_, err = c.Command(context.Background(), i.ID, FactoryCommand{Kind: "retry", RequestID: "retry-unknown", ExpectedVersion: got.Version, DecisionID: got.Decision.ID, AdditionalRounds: 1, Reason: "try again"})
	if err != ErrFactoryConflict {
		t.Fatalf("retry bypassed unknown prompt: %v", err)
	}
}

func TestFactoryRecoveryAttestationNamesWorker(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "prepare-attest", i.Version, json.RawMessage(`{"kind":"prepare"}`), func(dst *FactoryItem) error {
		dst.State = "recovering"
		dst.Approvals = loopItem().Approvals
		dst.Workers = []FactoryWorker{{AgentID: "known-worker", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn"}}
		dst.Effects = []FactoryEffect{{ID: "prompt-id", Kind: "implement", Status: "uncertain"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := NewFactoryController(s, &recoveryEffects{}, time.Now)
	cmd := FactoryCommand{Kind: "recover", RequestID: "attest", ExpectedVersion: i.Version, ConfirmPreviousWorkerStopped: true, Reason: "previous worker is stopped"}
	if _, err := c.Command(context.Background(), i.ID, cmd); err != ErrFactoryConflict {
		t.Fatalf("unnamed attestation: %v", err)
	}
	cmd.Reason = "known-worker was stopped by operator"
	got, err := c.Command(context.Background(), i.ID, cmd)
	if err != nil || got.Effects[0].Status != "completed" || got.Effects[0].Outcome == nil || got.Effects[0].Outcome.Confirmed || got.Decision == nil || got.Decision.Kind != "worker_failed" {
		t.Fatalf("attestation: item=%+v err=%v", got, err)
	}
	f := testFleetWithVersion(t, "dev")
	f.git = nil // the retained checkout is a fixture, not a remote repository
	work := t.TempDir()
	if err := f.ws.PutAgent(Agent{ID: "known-worker", Project: work, FactoryItemID: got.ID, FactoryRole: "implementer", FactorySpawnEffectID: "spawn", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	retry := FactoryCommand{Kind: "retry", RequestID: "retry-attested", ExpectedVersion: got.Version, DecisionID: got.Decision.ID, AdditionalRounds: 1, Reason: "continue retained work"}
	got, err = c.Command(context.Background(), i.ID, retry)
	if err != nil || len(got.Effects) != 2 || got.Effects[1].Kind != "resume" || got.Effects[1].Status != "prepared" || !factoryFreshResume(got, got.Effects[1]) {
		t.Fatalf("fresh resume intent: %+v %v", got.Effects, err)
	}
	worker, err := f.ResumeFactoryWorkerFresh(context.Background(), got, got.Workers[0])
	if err != nil || worker.AgentID != "known-worker" || worker.SessionID == "session" || worker.SessionID == "" {
		t.Fatalf("fresh worker: %+v %v", worker, err)
	}
	a, _ := f.ws.Agent(worker.AgentID)
	if a.Project != work || a.SessionID != worker.SessionID {
		t.Fatalf("retained checkout or session lost: %+v", a)
	}
	completed, err := CompleteFactoryEffect(got, got.Effects[1].ID, FactoryOutcome{EffectID: got.Effects[1].ID, Worker: &worker, Confirmed: true}, time.Now())
	if err != nil || completed.Workers[0].SessionID != worker.SessionID || completed.Effects[0].Outcome == nil || completed.Effects[0].Outcome.Confirmed {
		t.Fatalf("resume history: %+v %v", completed, err)
	}
}
