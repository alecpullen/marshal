package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFactoryRecoveryLostOutcomes(t *testing.T) {
	i := loopItem()
	i.State = "implementing"
	i.Effects = []FactoryEffect{{ID: "prompt-1", Kind: "implement", Status: "dispatched"}}
	c := NewFactoryController(nil, nil, nil)
	if _, ok := c.recoveryOutcome(i, i.Effects[0], FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}, factoryContinuationEvidence{}); ok {
		t.Fatal("idle prompt must not be accepted as completed")
	}
	spawn := FactoryEffect{ID: "spawn-id", Kind: "spawn", Status: "dispatched", WorkerRole: "implementer"}
	duplicate := FactoryWorker{AgentID: "one", SessionID: "s1", Role: "implementer", SpawnEffectID: spawn.ID}
	second := duplicate
	second.AgentID, second.SessionID = "two", "s2"
	if _, ok := c.recoveryOutcome(i, spawn, FactoryObservation{Workers: []FactoryWorker{duplicate, second}}, factoryContinuationEvidence{}); ok {
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
			out, ok := c.recoveryOutcome(i, e, tc.observation, factoryContinuationEvidence{})
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
			out, ok := c.recoveryOutcome(i, e, FactoryObservation{PR: &tc.pr}, factoryContinuationEvidence{})
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

func TestFactoryRecoverySeedEvidence(t *testing.T) {
	i := loopItem() // BaseSHA is "base"
	c := NewFactoryController(nil, nil, nil)
	e := FactoryEffect{ID: "seed", Kind: "seed", Status: "dispatched"}
	for _, tc := range []struct {
		name        string
		observation FactoryObservation
		want        bool
	}{
		// Seed never pushes, so a remote head is absent by design.
		{"local seed commit", FactoryObservation{HeadSHA: "seed-head", TreeSHA: "tree"}, true},
		{"seed did not run", FactoryObservation{HeadSHA: "base", TreeSHA: "tree"}, false},
		{"dirty checkout", FactoryObservation{HeadSHA: "seed-head", TreeSHA: "tree", Dirty: true}, false},
		{"missing tree", FactoryObservation{HeadSHA: "seed-head"}, false},
		{"observation error", FactoryObservation{HeadSHA: "seed-head", TreeSHA: "tree", Error: "boom"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := c.recoveryOutcome(i, e, tc.observation, factoryContinuationEvidence{})
			if ok != tc.want {
				t.Fatalf("seed accepted=%v, want %v", ok, tc.want)
			}
			if ok && out.HeadSHA != "seed-head" {
				t.Fatalf("seed evidence lost: %+v", out)
			}
		})
	}
}

func TestFactoryRecoverySeedThatNeverRanBecomesRetryable(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "prepare-seed", i.Version, json.RawMessage(`{"kind":"prepare"}`), func(dst *FactoryItem) error {
		dst.State = "preparing"
		dst.BaseSHA = "base"
		dst.Branch = "marshal/factory/" + dst.ID
		dst.Workers = []FactoryWorker{{AgentID: "worker", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn"}}
		dst.Effects = []FactoryEffect{{ID: "seed-id", Kind: "seed", Status: "uncertain"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fx := &recoveryEffects{observation: FactoryObservation{HeadSHA: "base", TreeSHA: "tree"}}
	c := NewFactoryController(s, fx, time.Now)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(i.ID)
	if got.Effects[0].Status != "completed" || got.Effects[0].Outcome == nil || !strings.Contains(got.Effects[0].Outcome.Error, "seed did not run") {
		t.Fatalf("seed that never ran stayed uncertain: %+v", got.Effects[0])
	}
	if got.Decision == nil || got.Decision.Kind != "worker_failed" {
		t.Fatalf("seed failure is not actionable: %+v", got.Decision)
	}
}

func TestFactoryAdvanceRetriesFailedSeed(t *testing.T) {
	now := time.Now().UTC()
	obs := FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, HeadSHA: "base", TreeSHA: "tree", BaseSHA: "base", ObservedAt: now}
	for _, tc := range []struct {
		name    string
		outcome *FactoryOutcome
		want    string
	}{
		{"failed seed retries the seed", &FactoryOutcome{EffectID: "seed-1", Error: "seed did not run", Confirmed: true}, "seed"},
		{"successful seed publishes", &FactoryOutcome{EffectID: "seed-1", HeadSHA: "seed-head", TreeSHA: "tree", Confirmed: true}, "publish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := loopItem()
			i.State = "repairing"
			i.Workers = []FactoryWorker{{AgentID: "worker", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn"}}
			i.Effects = []FactoryEffect{{ID: "seed-1", Kind: "seed", Status: "completed", WorkerRole: "implementer", Outcome: tc.outcome}}
			_, e, err := AdvanceFactory(i, obs, now)
			if err != nil {
				t.Fatal(err)
			}
			if e == nil || e.Kind != tc.want {
				t.Fatalf("wanted %s, got %+v", tc.want, e)
			}
			if e.Kind == "seed" && e.Worker == nil {
				t.Fatal("retried seed lost its worker")
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

// continuationReport is the fresh report a continuation turn writes. It names
// the iteration it answers, so it can never be read as the earlier final
// message the turn wrote before its children settled.
func continuationReport(id, question string) string {
	return "```json\n" + fmt.Sprintf(`{"continuationId":%q,"operationId":"op1","outcome":"needs_input","question":%q,"reason":"ambiguous"}`, id, question) + "\n```"
}

// continuationPromptID reads the continuation identity out of the prompt the
// bridge sent. The params are JSON, so a prompt line ends at an escaped
// newline.
func continuationPromptID(params string) string {
	const marker = "Continuation ID: "
	n := strings.Index(params, marker)
	if n < 0 {
		return ""
	}
	rest := params[n+len(marker):]
	if j := strings.IndexAny(rest, "\\"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// continuationFixture is a factory item in flight: a dispatched worker effect
// that handed back a waiting delegated turn, one live worker session per name,
// and a controller that reads and prompts through the real adapter.
type continuationFixture struct {
	t     *testing.T
	store *FactoryStore
	dw    *delegatedWorkers
	item  FactoryItem
	c     *FactoryController
	mu    sync.Mutex
	// atPrompt is each journalled delegated turn as the scripted session saw it
	// when a prompt arrived, which is how a test proves the dispatched record
	// was written before anything was sent.
	atPrompt []FactoryDelegatedTurn
}

func newContinuationFixture(t *testing.T, sessions ...string) *continuationFixture {
	t.Helper()
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	created, err := store.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dw := newDelegatedWorkers(t, created.ID, sessions...)
	f := &continuationFixture{t: t, store: store, dw: dw}
	f.item, err = store.Update(created.ID, "continuation-item", created.Version, json.RawMessage(`{"kind":"dispatch"}`), func(dst *FactoryItem) error {
		req := factoryReq()
		req.Config.TargetBranch = "main"
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: time.Now()}}
		dst.State = "implementing"
		dst.Branch = "marshal/factory/" + dst.ID
		dst.BaseSHA = "base"
		dst.Workers = append([]FactoryWorker(nil), dw.workers...)
		dst.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{
			ID: "op1", Kind: "implement", Status: "dispatched", WorkerRole: "implementer", Round: 1, ApprovalRevision: 1,
			Worker: &dst.Workers[0], ExpectedBase: "base",
			Delegated: &FactoryDelegatedTurn{
				EffectID: "op1", WorkerID: dst.Workers[0].AgentID, SessionID: dst.Workers[0].SessionID,
				ParentTurnID: "turn:1", ChildIDs: []int64{7}, State: factoryDelegatedWaiting,
			},
		}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.c = NewFactoryController(store, &factoryEffects{fleet: dw.fleet, workers: newFactoryWorkers(dw.fleet)}, time.Now)
	f.c.admitAfterRecovery()
	return f
}

// transcript states what the worker session's transcript holds and what its own
// child projection reports.
func (f *continuationFixture) transcript(n int, stack, status json.RawMessage) {
	tr := f.dw.transports[n]
	tr.stackResult = stack
	tr.results = map[string]json.RawMessage{
		"session/end_work":        json.RawMessage(`{"items":[]}`),
		"session/status":          status,
		"session/subagent_cancel": json.RawMessage(`{"cancelled":true}`),
	}
}

// children states the child projection the session reports from now on, as a
// child finishing between two ticks does.
func (f *continuationFixture) children(n int, status json.RawMessage) {
	f.dw.transports[n].results["session/status"] = status
}

// answer scripts the session's answer to a continuation prompt: it records the
// journalled delegation at that moment and then writes the turn and final
// message the prompt asked for.
func (f *continuationFixture) answer(n int, turn func(prompt int, id string) json.RawMessage) {
	tr := f.dw.transports[n]
	tr.results["session/prompt"] = json.RawMessage(`{"stopReason":"end_turn"}`)
	prompts := 0
	tr.onRequest = func(method string) error {
		if method != "session/prompt" {
			return nil
		}
		tr.mu.Lock()
		params := tr.seen[len(tr.seen)-1].params
		tr.mu.Unlock()
		if current, getErr := f.store.Get(f.item.ID); getErr == nil {
			if d := current.Effects[len(current.Effects)-1].Delegated; d != nil {
				f.mu.Lock()
				f.atPrompt = append(f.atPrompt, *d)
				f.mu.Unlock()
			}
		}
		tr.stackResult = turn(prompts, continuationPromptID(params))
		prompts++
		return nil
	}
}

// frames is every request one session received under method.
func (f *continuationFixture) frames(n int, method string) []string {
	tr := f.dw.transports[n]
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, frame := range tr.seen {
		if frame.method == method {
			out = append(out, frame.params)
		}
	}
	return out
}

// prompted is what the scripted session saw at each prompt.
func (f *continuationFixture) prompted() []FactoryDelegatedTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FactoryDelegatedTurn(nil), f.atPrompt...)
}

func (f *continuationFixture) current() FactoryItem {
	f.t.Helper()
	item, err := f.store.Get(f.item.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return item
}

// update journals one mutation and returns the item.
func (f *continuationFixture) update(tag string, mutate func(*FactoryItem) error) FactoryItem {
	f.t.Helper()
	current := f.current()
	next, err := f.store.Update(current.ID, "fixture-"+tag, current.Version, json.RawMessage(`{"kind":"fixture"}`), mutate, time.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	f.item = next
	return next
}

// stop journals a stop request for the item.
func (f *continuationFixture) stop() FactoryItem {
	return f.update("stop", func(dst *FactoryItem) error { dst.State = "stopping"; return nil })
}

// tick runs one controller pass and waits for every continuation it started.
func (f *continuationFixture) tick() {
	f.t.Helper()
	if err := f.c.tick(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	f.c.active.Wait()
}

// TestFactoryContinuationAfterChildSettles proves the waiting turn resolves: a
// settled child yields a fresh continuation, journalled as prepared and then
// dispatched before the prompt is sent, and the fresh report it answers with is
// the effect's result.
func TestFactoryContinuationAfterChildSettles(t *testing.T) {
	f := newContinuationFixture(t, "s1")
	// The turn that delegated is on the transcript with the early report it
	// wrote, and the child it launched has finished while the parent idled.
	f.transcript(0,
		delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
		delegatedStatus(t, true, delegatedChild(7, "done")))
	f.answer(0, func(_ int, id string) json.RawMessage {
		return delegatedTurnStack(t, "turn:2", nil, continuationReport(id, "Which approach, now the child finished?"))
	})
	f.tick()

	prompts := f.prompted()
	if len(prompts) != 1 {
		t.Fatalf("continuation prompts = %d, want 1", len(prompts))
	}
	if prompts[0].State != factoryDelegatedDispatched || prompts[0].ContinuationID == "" {
		t.Fatalf("the prompt was sent before the dispatched record existed: %+v", prompts[0])
	}
	if params := f.frames(0, "session/prompt"); len(params) != 1 || !strings.Contains(params[0], prompts[0].ContinuationID) {
		t.Fatalf("the prompt did not name the continuation: %v", params)
	}
	if params := f.frames(0, "session/prompt"); !strings.Contains(params[0], "Child 7") || !strings.Contains(params[0], "has finished") {
		t.Fatalf("the prompt did not ask for the settled children: %s", params[0])
	}
	saved := f.current()
	last := saved.Effects[len(saved.Effects)-1]
	if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil {
		t.Fatalf("the fresh report was not accepted: %+v", last)
	}
	if last.Outcome.Report.Question != "Which approach, now the child finished?" {
		t.Fatalf("the effect took the earlier final message as the result: %+v", last.Outcome.Report)
	}
	if last.Worker == nil || last.Worker.SessionID != "s1" || last.ExpectedBase != "base" {
		t.Fatalf("the continuation changed the effect's worker or candidate: %+v", last)
	}
	// The journal records the same order the prompt did.
	prepared, dispatched := false, false
	for _, event := range saved.History {
		switch event.Kind {
		case "continuation_prepared":
			prepared = true
			if dispatched {
				t.Fatal("the continuation was dispatched before it was prepared")
			}
		case "continuation_dispatched":
			dispatched = true
		}
	}
	if !prepared || !dispatched {
		t.Fatalf("continuation events = %+v", saved.History)
	}
	if saved.Decision == nil || saved.Decision.Kind != "question" {
		t.Fatalf("the accepted report did not surface its question: %+v", saved.Decision)
	}
}

// TestFactoryContinuationChildFailure pins what a failed or running child means:
// a failure is a terminal outcome the continuation must assess, and a running
// child is work the turn still waits on.
func TestFactoryContinuationChildFailure(t *testing.T) {
	t.Run("a failed child settles the turn and is named to the parent", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "failed")))
		f.answer(0, func(_ int, id string) json.RawMessage {
			return delegatedTurnStack(t, "turn:2", nil, continuationReport(id, "Child 7 failed; which fallback?"))
		})
		f.tick()
		params := f.frames(0, "session/prompt")
		if len(params) != 1 {
			t.Fatalf("a failed child did not settle the turn: %v", params)
		}
		if !strings.Contains(params[0], "failed") || !strings.Contains(params[0], "Assess every child that failed") {
			t.Fatalf("the prompt did not ask the parent to assess the failure: %s", params[0])
		}
		saved := f.current()
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil || last.Outcome.Report.Question != "Child 7 failed; which fallback?" {
			t.Fatalf("the continuation report was not accepted: %+v", last)
		}
	})

	t.Run("a running child holds the turn and prompts nothing", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "running")))
		f.answer(0, func(_ int, id string) json.RawMessage {
			return delegatedTurnStack(t, "turn:2", nil, continuationReport(id, "ignored"))
		})
		f.tick()
		if params := f.frames(0, "session/prompt"); len(params) != 0 {
			t.Fatalf("a running child did not hold the turn: %v", params)
		}
		saved := f.current()
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "dispatched" || last.Outcome != nil {
			t.Fatalf("a running child let the effect complete: %+v", last)
		}
		if last.Delegated == nil || last.Delegated.State != factoryDelegatedWaiting || last.Delegated.ContinuationID != "" {
			t.Fatalf("the waiting turn was disturbed: %+v", last.Delegated)
		}
	})

	t.Run("an unread child projection is unknown work, not a settled set", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, false, delegatedChild(7, "done")))
		f.tick()
		if params := f.frames(0, "session/prompt"); len(params) != 0 {
			t.Fatalf("an unread projection was read as settled: %v", params)
		}
	})
}

// TestFactoryContinuationResumesTurnHoldingItsIdentity pins the recovery of a
// turn that was prepared and then went back to waiting before anything was
// sent: Continue re-reads the child projection and returns the turn to waiting
// with its identity intact. That identity must resume and dispatch. Minting a
// second one is not merely a duplicate — factoryContinuationID hashes the
// identity it derives from, so a fresh derivation cannot reproduce the held ID,
// and the turn would wait forever on work nothing re-dispatches.
func TestFactoryContinuationResumesTurnHoldingItsIdentity(t *testing.T) {
	f := newContinuationFixture(t, "s1")
	f.transcript(0,
		delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
		delegatedStatus(t, true, delegatedChild(7, "done")))
	// The journal holds the turn as waiting under an identity: exactly the state
	// Continue leaves behind when it cannot prove the children settled.
	f.update("unsent", func(dst *FactoryItem) error {
		last := &dst.Effects[len(dst.Effects)-1]
		if last.Delegated == nil {
			return ErrFactoryConflict
		}
		next := *last.Delegated
		next.ContinuationID = "cont-unsent"
		next.State = factoryDelegatedWaiting
		last.Delegated = &next
		return nil
	})
	f.answer(0, func(_ int, id string) json.RawMessage {
		return delegatedTurnStack(t, "turn:2", nil, continuationReport(id, "Which approach?"))
	})
	f.tick()

	prompts := f.prompted()
	if len(prompts) != 1 {
		t.Fatalf("continuation prompts = %d, want 1: a waiting turn holding its identity was never dispatched", len(prompts))
	}
	if prompts[0].ContinuationID != "cont-unsent" {
		t.Fatalf("the continuation minted %q, want the identity the turn already held", prompts[0].ContinuationID)
	}
	params := f.frames(0, "session/prompt")
	if len(params) != 1 || continuationPromptID(params[0]) != "cont-unsent" {
		t.Fatalf("the prompt did not name the resumed continuation: %v", params)
	}
	saved := f.current()
	last := saved.Effects[len(saved.Effects)-1]
	if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil {
		t.Fatalf("the resumed continuation did not complete the effect: %+v", last)
	}
}

// TestFactoryQuiesceKeepsPreparedContinuation proves a shutdown between the
// journal write and the prompt does not hand back an ambiguity: a prepared
// continuation was never sent, so a restart re-dispatches it instead of
// demanding an operator decision.
func TestFactoryQuiesceKeepsPreparedContinuation(t *testing.T) {
	f := newContinuationFixture(t, "s1")
	f.update("prepared", func(dst *FactoryItem) error {
		last := &dst.Effects[len(dst.Effects)-1]
		if last.Delegated == nil {
			return ErrFactoryConflict
		}
		next := *last.Delegated
		next.ContinuationID = "cont-prepared"
		next.State = factoryDelegatedPrepared
		last.Delegated = &next
		return nil
	})
	if err := f.c.Quiesce(); err != nil {
		t.Fatal(err)
	}
	saved := f.current()
	last := saved.Effects[len(saved.Effects)-1]
	if last.Status != "dispatched" || last.Delegated == nil || last.Delegated.State != factoryDelegatedPrepared {
		t.Fatalf("a never-sent continuation was made uncertain: %+v", last)
	}
	if saved.Decision != nil {
		t.Fatalf("a never-sent continuation demanded a decision: %+v", saved.Decision)
	}
}

// TestFactoryContinuationRepeatsForNewChild proves the cycle repeats: a
// continuation that launches new child work is waiting again, under a fresh
// identity, and no report is accepted until that child settles.
func TestFactoryContinuationRepeatsForNewChild(t *testing.T) {
	f := newContinuationFixture(t, "s1")
	f.transcript(0,
		delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
		delegatedStatus(t, true, delegatedChild(7, "done")))
	f.answer(0, func(prompt int, id string) json.RawMessage {
		if prompt == 0 {
			// The first continuation launches child 8 and writes an early
			// report of its own.
			return delegatedTurnStack(t, "turn:2", []int64{8}, delegatedReport("op1"))
		}
		return delegatedTurnStack(t, "turn:3", nil, continuationReport(id, "Both children finished"))
	})
	f.tick()
	first := f.prompted()
	if len(first) != 1 || first[0].ContinuationID == "" {
		t.Fatalf("no first continuation: %+v", first)
	}
	saved := f.current()
	last := saved.Effects[len(saved.Effects)-1]
	if last.Status != "dispatched" || last.Outcome != nil {
		t.Fatalf("the continuation's early report completed the effect: %+v", last)
	}
	d := last.Delegated
	if d == nil || d.State != factoryDelegatedWaiting || d.ParentTurnID != "turn:2" || len(d.ChildIDs) != 1 || d.ChildIDs[0] != 8 {
		t.Fatalf("the new child work was not journalled as waiting: %+v", d)
	}
	if d.ContinuationID != "" {
		t.Fatalf("the next iteration inherited the spent continuation identity: %+v", d)
	}
	// The new child is still running, so nothing further is sent.
	f.tick()
	if params := f.frames(0, "session/prompt"); len(params) != 1 {
		t.Fatalf("a running child was continued anyway: %v", params)
	}
	// It finishes, and the cycle continues under a second, distinct identity.
	f.children(0, delegatedStatus(t, true, delegatedChild(7, "done"), delegatedChild(8, "done")))
	f.tick()
	second := f.prompted()
	if len(second) != 2 {
		t.Fatalf("the repeated cycle did not prompt: %+v", second)
	}
	if second[1].ContinuationID == "" || second[1].ContinuationID == first[0].ContinuationID {
		t.Fatalf("the repeated cycle reused continuation %q", second[1].ContinuationID)
	}
	if second[1].ParentTurnID != "turn:2" {
		t.Fatalf("the repeated cycle continued %q", second[1].ParentTurnID)
	}
	saved = f.current()
	last = saved.Effects[len(saved.Effects)-1]
	if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil || last.Outcome.Report.Question != "Both children finished" {
		t.Fatalf("the repeated continuation was not accepted: %+v", last)
	}
}

// continuationRecoveryEffects states the transcript proof a reconcile reads and
// records the continuation it was asked to send, so a crash boundary can be
// tested without a live session.
type continuationRecoveryEffects struct {
	observation FactoryObservation
	proof       factoryContinuationEvidence
	proofErr    error
	outcome     *FactoryOutcome
	store       *FactoryStore
	mu          sync.Mutex
	atCall      []FactoryDelegatedTurn
	cancels     int
}

func (f *continuationRecoveryEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return f.observation, nil
}

func (f *continuationRecoveryEffects) Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error) {
	return FactoryOutcome{}, fmt.Errorf("unexpected execute")
}

func (f *continuationRecoveryEffects) ContinuationEvidence(context.Context, FactoryItem, FactoryEffect) (factoryContinuationEvidence, error) {
	return f.proof, f.proofErr
}

func (f *continuationRecoveryEffects) Continue(_ context.Context, item FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	f.mu.Lock()
	if f.store != nil {
		if current, err := f.store.Get(item.ID); err == nil {
			if d := current.Effects[len(current.Effects)-1].Delegated; d != nil {
				f.atCall = append(f.atCall, *d)
			}
		}
	}
	f.mu.Unlock()
	if f.outcome == nil {
		return FactoryOutcome{EffectID: e.ID}, nil
	}
	out := *f.outcome
	out.EffectID = e.ID
	out.Continuation = e.Delegated.ContinuationID
	if e.Worker != nil {
		// The real turn binds its result to the worker it prompted.
		out.Worker = e.Worker
	}
	return out, nil
}

func (f *continuationRecoveryEffects) CancelChildren(context.Context, FactoryItem) error {
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	return nil
}

// continuationRecoveryItem journals a delegated item in one continuation state.
func continuationRecoveryItem(t *testing.T, store *FactoryStore, state string) FactoryItem {
	t.Helper()
	created, err := store.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := factoryReq()
	req.Config.TargetBranch = "main"
	item, err := store.Update(created.ID, "prepare", created.Version, json.RawMessage(`{"kind":"prepare"}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: time.Now()}}
		dst.State = "implementing"
		dst.BaseSHA = "base"
		dst.Workers = []FactoryWorker{{AgentID: "agent-s1", SessionID: "s1", Role: "implementer", SpawnEffectID: "spawn"}}
		dst.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{
			ID: "op1", Kind: "implement", Status: "dispatched", WorkerRole: "implementer", Round: 1, ApprovalRevision: 1,
			Worker: &dst.Workers[0], ExpectedBase: "base",
			Delegated: &FactoryDelegatedTurn{
				EffectID: "op1", WorkerID: "agent-s1", SessionID: "s1", ParentTurnID: "turn:1",
				ChildIDs: []int64{7}, ContinuationID: "cont-1", State: state,
			},
		}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// continuationObservation is a settled observation of the delegated worker.
func continuationObservation() FactoryObservation {
	return FactoryObservation{
		Workers: []FactoryWorker{{AgentID: "agent-s1", SessionID: "s1", Role: "implementer", SpawnEffectID: "spawn"}},
		Children: []FactoryChildSession{{SessionID: "s1", Known: true,
			Children: []FactoryChild{{SessionID: "s1", ID: 7, Status: "done", Terminal: true}}}},
	}
}

// TestFactoryContinuationRecoveryBoundaries covers every crash boundary: the
// prepared record without a dispatch, a dispatch without fresh proof, and the
// fresh proof that accepts. None of them is resolved by guessing.
func TestFactoryContinuationRecoveryBoundaries(t *testing.T) {
	t.Run("prepared with no dispatch is dispatched once", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedPrepared)
		report := continuationReport("cont-1", "the fresh report")
		fx := &continuationRecoveryEffects{
			observation: continuationObservation(),
			proof:       factoryContinuationEvidence{Known: true},
			store:       store,
			outcome: &FactoryOutcome{Confirmed: true, Report: &FactoryReport{
				OperationID: "op1", Outcome: "needs_input", Question: "the fresh report", Reason: "ambiguous",
			}},
		}
		c := NewFactoryController(store, fx, time.Now)
		if _, err := c.reconcile(context.Background(), item, false, ""); err != nil {
			t.Fatal(err)
		}
		c.active.Wait()
		fx.mu.Lock()
		atCall := append([]FactoryDelegatedTurn(nil), fx.atCall...)
		fx.mu.Unlock()
		if len(atCall) != 1 {
			t.Fatalf("continuations sent = %d, want 1", len(atCall))
		}
		if atCall[0].State != factoryDelegatedDispatched || atCall[0].ContinuationID != "cont-1" {
			t.Fatalf("the prompt ran before the dispatched record: %+v", atCall[0])
		}
		saved, err := store.Get(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil || last.Outcome.Report.Question != reportQuestion(report) {
			t.Fatalf("the recovered continuation was not accepted: %+v", last)
		}
	})

	t.Run("prepared with a dispatch in the transcript is never resent", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedPrepared)
		fx := &continuationRecoveryEffects{observation: continuationObservation(), proof: factoryContinuationEvidence{Known: true, Dispatched: true}, store: store}
		c := NewFactoryController(store, fx, time.Now)
		got, err := c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		fx.mu.Lock()
		sent := len(fx.atCall)
		fx.mu.Unlock()
		if sent != 0 {
			t.Fatalf("%d continuations were sent on an unproven absence", sent)
		}
		last := got.Effects[len(got.Effects)-1]
		if last.Status != "uncertain" || last.Delegated == nil || last.Delegated.State != factoryDelegatedUncertain {
			t.Fatalf("the ambiguous continuation was not marked uncertain: %+v", last)
		}
		if got.Decision == nil || got.Decision.Kind != "recovery" {
			t.Fatalf("the existing recovery decision was not surfaced: %+v", got.Decision)
		}
	})

	t.Run("an unreadable transcript never proves absence", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedPrepared)
		fx := &continuationRecoveryEffects{observation: continuationObservation(), proofErr: fmt.Errorf("unavailable"), store: store}
		c := NewFactoryController(store, fx, time.Now)
		got, err := c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		fx.mu.Lock()
		sent := len(fx.atCall)
		fx.mu.Unlock()
		if sent != 0 {
			t.Fatalf("%d continuations were sent with no evidence at all", sent)
		}
		if got.Effects[0].Status != "uncertain" || got.Decision == nil || got.Decision.Kind != "recovery" {
			t.Fatalf("an unreadable transcript was not made actionable: %+v", got)
		}
	})

	t.Run("dispatched with matching fresh proof is accepted", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedDispatched)
		fx := &continuationRecoveryEffects{
			observation: continuationObservation(),
			proof: factoryContinuationEvidence{Known: true, Dispatched: true, Accepted: true,
				Report: continuationReport("cont-1", "fresh after restart"), Turn: "turn:2"},
			store: store,
		}
		c := NewFactoryController(store, fx, time.Now)
		got, err := c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		last := got.Effects[len(got.Effects)-1]
		if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil || last.Outcome.Report.Question != "fresh after restart" {
			t.Fatalf("the fresh proof was not accepted: %+v", last)
		}
		if got.Decision != nil {
			t.Fatalf("an accepted continuation kept a decision: %+v", got.Decision)
		}
	})

	t.Run("dispatched without fresh proof is not retried", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedDispatched)
		fx := &continuationRecoveryEffects{observation: continuationObservation(), proof: factoryContinuationEvidence{Known: true, Dispatched: true}, store: store}
		c := NewFactoryController(store, fx, time.Now)
		got, err := c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		last := got.Effects[len(got.Effects)-1]
		if last.Status != "uncertain" || last.Delegated == nil || last.Delegated.State != factoryDelegatedUncertain {
			t.Fatalf("an unproven continuation was not made uncertain: %+v", last)
		}
		// A second pass changes nothing and sends nothing.
		again, err := c.reconcile(context.Background(), got, false, "")
		if err != nil {
			t.Fatal(err)
		}
		fx.mu.Lock()
		sent := len(fx.atCall)
		fx.mu.Unlock()
		if sent != 0 || again.Effects[0].Status != "uncertain" {
			t.Fatalf("an uncertain continuation was retried: %+v sent=%d", again.Effects[0], sent)
		}
	})

	t.Run("a report is not accepted while new children run", func(t *testing.T) {
		store, err := OpenFactoryStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		item := continuationRecoveryItem(t, store, factoryDelegatedDispatched)
		obs := continuationObservation()
		obs.Children = []FactoryChildSession{{SessionID: "s1", Known: true, Children: []FactoryChild{
			{SessionID: "s1", ID: 7, Status: "done", Terminal: true},
			{SessionID: "s1", ID: 8, Status: "running"},
		}}}
		// The turn itself is recorded as waiting on the new child.
		item.Effects[0].Delegated.ChildIDs = []int64{7, 8}
		fx := &continuationRecoveryEffects{
			observation: obs,
			proof: factoryContinuationEvidence{Known: true, Dispatched: true, Accepted: true,
				Report: continuationReport("cont-1", "should not be accepted"), Turn: "turn:2"},
			store: store,
		}
		c := NewFactoryController(store, fx, time.Now)
		got, err := c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		last := got.Effects[len(got.Effects)-1]
		if last.Status == "completed" || last.Outcome != nil {
			t.Fatalf("a report was accepted with child 8 still running: %+v", last)
		}
	})
}

// reportQuestion reads the question a scripted needs_input report carries.
func reportQuestion(report string) string {
	block, ok := lastJSONBlock(report)
	if !ok {
		return ""
	}
	var wire struct {
		Question string `json:"question"`
	}
	if json.Unmarshal([]byte(block), &wire) != nil {
		return ""
	}
	return wire.Question
}

// TestFactoryContinuationUncertain pins what is never accepted: a report that
// answers a different iteration, and a report written while the continuation's
// own children are still running.
func TestFactoryContinuationUncertain(t *testing.T) {
	t.Run("a report naming another iteration is rejected", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "done")))
		f.answer(0, func(_ int, _ string) json.RawMessage {
			// The turn writes a report for an iteration nobody dispatched.
			return delegatedTurnStack(t, "turn:2", nil, continuationReport("cont-someone-else", "stale"))
		})
		f.tick()
		saved := f.current()
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "uncertain" || last.Outcome != nil {
			t.Fatalf("a report for another iteration was accepted: %+v", last)
		}
		if last.Delegated == nil || last.Delegated.State != factoryDelegatedUncertain {
			t.Fatalf("the continuation was not made uncertain: %+v", last.Delegated)
		}
		if saved.Decision == nil || saved.Decision.Kind != "recovery" {
			t.Fatalf("the existing recovery decision was not surfaced: %+v", saved.Decision)
		}
		// Nothing is sent again: the uncertain continuation is spent.
		f.tick()
		if params := f.frames(0, "session/prompt"); len(params) != 1 {
			t.Fatalf("an uncertain continuation was repeated: %v", params)
		}
	})

	t.Run("a continuation that launches children is not finished by its early report", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "done")))
		f.answer(0, func(_ int, id string) json.RawMessage {
			// The continuation writes a report naming itself, but it also
			// launched child 9, which is still running.
			return delegatedTurnStack(t, "turn:2", []int64{9}, continuationReport(id, "too early"))
		})
		f.children(0, delegatedStatus(t, true, delegatedChild(7, "done"), delegatedChild(9, "running")))
		f.tick()
		saved := f.current()
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "dispatched" || last.Outcome != nil {
			t.Fatalf("a report was accepted with a child still running: %+v", last)
		}
		if last.Delegated == nil || last.Delegated.State != factoryDelegatedWaiting || len(last.Delegated.ChildIDs) != 1 || last.Delegated.ChildIDs[0] != 9 {
			t.Fatalf("the new child work was not journalled as waiting: %+v", last.Delegated)
		}
	})
}

// TestFactoryStopIncludesOwnedChildren pins the stop: the children a turn owns
// are cancelled through the existing session-scoped control, and the item is
// reported stopped only once their terminal outcomes are observed.
func TestFactoryStopIncludesOwnedChildren(t *testing.T) {
	t.Run("the cancellation names the session-scoped pair and nothing else", func(t *testing.T) {
		f := newContinuationFixture(t, "s1", "s2")
		f.stop()
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "running")))
		f.transcript(1, json.RawMessage(`{"roots":["turn:9"],"nodes":[]}`), delegatedStatus(t, true, delegatedChild(1, "running")))
		f.tick()

		cancels := f.frames(0, "session/subagent_cancel")
		if len(cancels) != 1 {
			t.Fatalf("session s1 cancels = %v", cancels)
		}
		if !strings.Contains(cancels[0], `"sessionId":"s1"`) || !strings.Contains(cancels[0], `"subagentId":7`) {
			t.Fatalf("the cancel did not name the session-scoped pair: %s", cancels[0])
		}
		// An unrelated session is untouched, and no prompt is ever sent.
		if got := f.frames(1, "session/subagent_cancel"); len(got) != 0 {
			t.Fatalf("session s2 was cancelled: %v", got)
		}
		for n := range f.dw.transports {
			if got := f.frames(n, "session/prompt"); len(got) != 0 {
				t.Fatalf("session %d was prompted during a stop: %v", n, got)
			}
		}
		saved := f.current()
		if saved.State != "stopping" {
			t.Fatalf("a stop was reported with a child still running: %q", saved.State)
		}
		last := saved.Effects[len(saved.Effects)-1]
		if last.Status != "dispatched" || last.Outcome != nil || last.Delegated == nil || last.Delegated.State != factoryDelegatedWaiting {
			t.Fatalf("the stopped turn's journal changed: %+v", last)
		}
		if len(saved.Workers) != 2 || saved.Workers[1].AgentID == "" || saved.Workers[1].Parked {
			t.Fatalf("an unrelated worker was disturbed: %+v", saved.Workers)
		}
	})

	t.Run("a terminal child outcome confirms the stop, an acknowledgement does not", func(t *testing.T) {
		f := newContinuationFixture(t, "s1")
		item := f.stop()
		f.transcript(0,
			delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
			delegatedStatus(t, true, delegatedChild(7, "running")))
		waiting, err := f.c.reconcile(context.Background(), item, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if waiting.State != "stopping" || waiting.Effects[0].Status != "dispatched" {
			t.Fatalf("a cancellation acknowledgement was read as a stopped child: %+v", waiting)
		}
		if cancels := f.frames(0, "session/subagent_cancel"); len(cancels) != 1 {
			t.Fatalf("the stop did not ask the owned child to stop: %v", cancels)
		}
		if waiting.Decision == nil || waiting.Decision.Kind != "recovery" {
			t.Fatalf("the waiting stop surfaced no decision: %+v", waiting.Decision)
		}
		// The child finishes, and only then is the item stopped.
		f.children(0, delegatedStatus(t, true, delegatedChild(7, "done")))
		stopped, err := f.c.reconcile(context.Background(), waiting, false, "")
		if err != nil {
			t.Fatal(err)
		}
		last := stopped.Effects[len(stopped.Effects)-1]
		if last.Status != "completed" || last.Outcome == nil || last.Outcome.Confirmed || !strings.Contains(last.Outcome.Error, "observed") {
			t.Fatalf("the stop was not completed on a terminal child: %+v", last)
		}
		if stopped.State != "stopping" || stopped.Decision != nil {
			t.Fatalf("the stopped item kept its stop decision: %+v", stopped)
		}
	})
}
