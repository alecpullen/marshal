package bridge

import (
	"context"
	"encoding/json"
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
}

func TestFactoryRecoveryParkedAndStopped(t *testing.T) {
	i := loopItem()
	i.State = "stopping"
	i.Workers = []FactoryWorker{{AgentID: "worker", Role: "implementer"}}
	if recoveryStopConfirmed(i, FactoryObservation{RuntimeKnown: true, TurnIdle: true}) {
		t.Fatal("detached work is unknown")
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
