package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func loopItem() FactoryItem {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	req := factoryReq()
	req.Config.MaxRounds = 5
	req.Config.TargetBranch = "main"
	return FactoryItem{ID: "item", Title: req.Title, State: "queued", Draft: req, RepoID: req.Config.RepoID, Branch: "marshal/factory/item", BaseSHA: "base", Approvals: []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}, Grants: []FactoryGrant{{ID: "initial", AdditionalRounds: 5, At: now}}, ApprovedAt: now}
}
func TestFactoryLoopHappyAndRepair(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	steps := []string{"spawn", "seed", "publish", "create_pr", "implement", "publish", "park", "spawn", "review", "park", "resume", "implement", "publish", "park", "spawn", "review", "park", "set_ready"}
	for _, kind := range steps {
		obs := FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, HeadSHA: "head", TreeSHA: "tree", RemoteHead: "head", BaseSHA: "base", CI: &FactoryCI{HeadSHA: "head", Complete: true}, ObservedAt: now}
		if len(i.PRs) > 0 {
			p := i.PRs[len(i.PRs)-1]
			obs.PR = &p
		}
		n, e, err := AdvanceFactory(i, obs, now)
		if err != nil {
			t.Fatal(err)
		}
		if e == nil || e.Kind != kind {
			t.Fatalf("wanted %s, got %+v state=%s stage=%s", kind, e, n.State, n.Stage)
		}
		i = n
		outcome := FactoryOutcome{EffectID: e.ID, Confirmed: true, HeadSHA: "head", TreeSHA: "tree", BaseSHA: "base"}
		if kind == "spawn" {
			w := FactoryWorker{AgentID: e.ID, SessionID: e.ID, Role: e.WorkerRole, SpawnEffectID: e.ID}
			outcome.Worker = &w
		}
		if kind == "create_pr" {
			p := FactoryPR{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}
			outcome.PR = &p
		}
		if kind == "implement" {
			outcome.Report = &FactoryReport{OperationID: e.ID, Outcome: "complete", Summary: "done"}
		}
		if kind == "review" {
			findings := []ReviewFinding{}
			if len(i.Rounds) == 1 {
				findings = []ReviewFinding{{Title: "bug", Severity: "blocking"}}
			}
			outcome.Report = &FactoryReport{OperationID: e.ID, Outcome: "complete", Summary: "findings", InputsAccessible: true, BaseSHA: "base", HeadSHA: "head", Findings: findings}
		}
		if kind == "set_ready" {
			p := i.PRs[len(i.PRs)-1]
			p.Draft = false
			outcome.PR = &p
		}
		i, err = CompleteFactoryEffect(i, e.ID, outcome, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(i.Rounds) != 2 || i.State != "ready" {
		t.Fatalf("final state=%s rounds=%d", i.State, len(i.Rounds))
	}
}
func TestFactoryLoopLimitsAndNoProgress(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "repairing"
	i.Stage = "resume"
	i.Effects = []FactoryEffect{{ID: "resume", Kind: "resume", Status: "completed"}}
	i.Rounds = []FactoryRound{{Number: 1, TreeSHA: "same", Review: &FactoryReport{Findings: []ReviewFinding{{Title: "bug", Severity: "blocking"}}}}}
	for n := 2; n <= 5; n++ {
		i.Rounds = append(i.Rounds, FactoryRound{Number: n, TreeSHA: "changed"})
	}
	got, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e != nil || got.Decision == nil || got.Decision.Kind != "exhausted" {
		t.Fatalf("exhaustion: %+v %+v %v", got.Decision, e, err)
	}
	i.Grants = append(i.Grants, FactoryGrant{ID: "extra", AdditionalRounds: 1})
	_, e, err = AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "implement" || e.Round != 6 {
		t.Fatalf("grant: %+v %v", e, err)
	}
	i = loopItem()
	i.State = "repairing"
	i.Stage = "resume"
	i.Effects = []FactoryEffect{{ID: "resume", Kind: "resume", Status: "completed"}}
	i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, TreeSHA: "same"}, {Number: 2, ApprovalRevision: 1, TreeSHA: "same", Review: &FactoryReport{Findings: []ReviewFinding{{Title: "bug", Severity: "blocking"}}}}}
	got, e, err = AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e != nil || got.Decision == nil || got.Decision.Kind != "no_progress" {
		t.Fatalf("no progress: %+v %+v %v", got.Decision, e, err)
	}
}
func TestFactoryLoopEvidenceAndRevision(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "reviewing"
	i.Stage = "review"
	i.Rounds = []FactoryRound{{Number: 1, BaseSHA: "base", HeadSHA: "head", CI: &FactoryCI{HeadSHA: "head", Complete: false}, Review: &FactoryReport{Outcome: "complete", InputsAccessible: true, BaseSHA: "base", HeadSHA: "head", Findings: []ReviewFinding{}}}}
	i.PRs = []FactoryPR{{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
	n, e, err := AdvanceFactory(i, FactoryObservation{HeadSHA: "head", BaseSHA: "base", PR: &i.PRs[0], CI: i.Rounds[0].CI}, now)
	if err != nil || e != nil || n.State == "ready" {
		t.Fatalf("incomplete CI passed: %s %+v %v", n.State, e, err)
	}
	ci := FactoryCI{HeadSHA: "head", Complete: true}
	i.Rounds[0].CI = &ci
	n, e, err = AdvanceFactory(i, FactoryObservation{HeadSHA: "other", BaseSHA: "base", PR: &i.PRs[0], CI: &ci}, now)
	if err != nil || e != nil || n.State == "ready" {
		t.Fatalf("head drift passed: %s %+v %v", n.State, e, err)
	}
}
func TestFactoryLoopCommands(t *testing.T) {
	i := loopItem()
	e := FactoryEffect{ID: "effect", Kind: "implement", Status: "prepared"}
	i.Effects = []FactoryEffect{e}
	if _, err := CompleteFactoryEffect(i, "other", FactoryOutcome{Confirmed: true}, time.Now()); err == nil {
		t.Fatal("stale callback accepted")
	}
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "stop"}, time.Now()); err != nil || i.Effects[0].Status != "completed" || i.State != "stopping" {
		t.Fatalf("stop did not cancel prepared intent: %+v %v", i, err)
	}
}

type loopEffects struct {
	store          *FactoryStore
	seen           []string
	observation    FactoryObservation
	observeErr     error
	stopOnDispatch bool
}

func (f *loopEffects) Observe(_ context.Context, _ FactoryItem) (FactoryObservation, error) {
	return f.observation, f.observeErr
}
func TestFactoryReadyMonitorPersistsDraftOnUnavailableCI(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "ready", i.Version, json.RawMessage(`{"ready":true}`), func(dst *FactoryItem) error {
		dst.State = "ready"
		dst.Branch = "marshal/factory/item"
		req := factoryReq()
		req.Config.TargetBranch = "main"
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: time.Now()}}
		dst.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: dst.Branch, BaseRef: "main", State: "open", HeadSHA: "head", BaseSHA: "base"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := i.PRs[0]
	f := &loopEffects{store: s, observation: FactoryObservation{PR: &p, Error: "CI unavailable"}, observeErr: errors.New("CI unavailable")}
	c := NewFactoryController(s, f, time.Now)
	c.admitAfterRecovery()
	c.monitorReadyOnce(context.Background())
	saved, err := s.Get(i.ID)
	if err != nil || saved.State == "ready" || len(saved.Effects) != 1 || saved.Effects[0].Kind != "set_draft" || saved.Effects[0].Status != "prepared" {
		t.Fatalf("durable revocation: %+v %v", saved, err)
	}
}
func TestFactoryActiveTickClosesPRDespiteCIError(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "checking", i.Version, json.RawMessage(`{"checking":true}`), func(dst *FactoryItem) error {
		req := factoryReq()
		req.Config.TargetBranch = "main"
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: time.Now()}}
		dst.State = "checking"
		dst.Branch = "marshal/factory/item"
		dst.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: dst.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: "park", Kind: "park", WorkerRole: "implementer", Status: "completed"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := i.PRs[0]
	p.State = "merged"
	f := &loopEffects{store: s, observation: FactoryObservation{PR: &p, Error: "CI fetch failed"}, observeErr: errors.New("CI fetch failed")}
	c := NewFactoryController(s, f, time.Now)
	c.admitAfterRecovery()
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get(i.ID)
	if err != nil || saved.State != "closed" || saved.Decision != nil {
		t.Fatalf("closed PR was stranded: %+v %v", saved, err)
	}
}
func (f *loopEffects) Execute(_ context.Context, i FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	saved, err := f.store.Get(i.ID)
	if err != nil {
		return FactoryOutcome{}, err
	}
	if len(saved.Effects) == 0 || saved.Effects[len(saved.Effects)-1].ID != e.ID || saved.Effects[len(saved.Effects)-1].Status != "dispatched" {
		return FactoryOutcome{}, fmt.Errorf("effect was not durable before dispatch")
	}
	f.seen = append(f.seen, e.Kind)
	if f.stopOnDispatch {
		raw := json.RawMessage(`{"kind":"stop"}`)
		_, err = f.store.Update(i.ID, "stop-inflight", saved.Version, raw, func(dst *FactoryItem) error {
			return factoryApplyCommand(dst, FactoryCommand{Kind: "stop"}, time.Now())
		}, time.Now())
		if err != nil {
			return FactoryOutcome{}, err
		}
	}
	o := FactoryOutcome{EffectID: e.ID, Confirmed: true}
	if e.Kind == "spawn" {
		o.Worker = &FactoryWorker{AgentID: "worker", SessionID: "session", Role: e.WorkerRole, SpawnEffectID: e.ID}
	}
	return o, nil
}
func TestFactoryLoopDurableIntentAndRecoveryGate(t *testing.T) {
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	req := factoryReq()
	i, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &loopEffects{store: store, observation: FactoryObservation{BaseSHA: "base"}}
	c := NewFactoryController(store, fake, time.Now)
	cmd := FactoryCommand{RequestID: "approve", ExpectedVersion: i.Version, Kind: "approve"}
	i, err = c.Command(context.Background(), i.ID, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.seen) != 0 {
		t.Fatal("dispatched before recovery gate")
	}
	c.admitAfterRecovery()
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.seen) != 1 || fake.seen[0] != "spawn" {
		t.Fatalf("effects=%v", fake.seen)
	}
	saved, _ := store.Get(i.ID)
	if saved.BaseSHA != "base" {
		t.Fatalf("base not persisted: %q", saved.BaseSHA)
	}
	if saved.Effects[0].Status != "completed" {
		t.Fatalf("saved effect=%+v", saved.Effects[0])
	}
}
func TestFactoryLoopRepairPromptEvidence(t *testing.T) {
	i := loopItem()
	i.Rounds = []FactoryRound{{Number: 1, HeadSHA: "head", CI: &FactoryCI{HeadSHA: "head", Complete: true, Checks: []FactoryCheck{{Key: FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 1}, SHA: "head", Status: "completed", Conclusion: "failure", LogTail: "compile failed"}}}, Review: &FactoryReport{Outcome: "complete", Findings: []ReviewFinding{{Title: "broken API", Severity: "blocking"}}}}}
	prompt, err := BuildFactoryPrompt(i, FactoryEffect{ID: "repair", Kind: "implement", Round: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "compile failed") || !strings.Contains(prompt, "broken API") || !strings.Contains(prompt, "infrastructure_suspected") {
		t.Fatalf("repair evidence missing: %s", prompt)
	}
}
func TestFactoryLoopNoProgressGrantContinues(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "repairing"
	i.Effects = []FactoryEffect{{Kind: "resume", Status: "completed"}}
	i.Rounds = []FactoryRound{{Number: 1, TreeSHA: "same"}, {Number: 2, TreeSHA: "same", FinishedAt: now.Add(-time.Hour), Review: &FactoryReport{Findings: []ReviewFinding{{Title: "bug", Severity: "blocking"}}}}}
	i.Grants = append(i.Grants, FactoryGrant{ID: "retry", AdditionalRounds: 1, At: now})
	_, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "implement" || e.Round != 3 {
		t.Fatalf("grant failed: %+v %v", e, err)
	}
}
func TestFactoryLoopFIFOAndIdleRelease(t *testing.T) {
	now := time.Now().UTC()
	a := loopItem()
	a.ID = "z"
	a.State = "queued"
	a.ApprovedAt = now.Add(-time.Minute)
	b := loopItem()
	b.ID = "a"
	b.State = "queued"
	b.ApprovedAt = now
	items := []FactoryItem{b, a}
	picked := factoryPick(items, func(FactoryItem) bool { return true })
	if picked == nil || picked.ID != "z" {
		t.Fatalf("FIFO chose %+v", picked)
	}
	a.State = "needs_input"
	a.Decision = &FactoryDecision{ID: "q"}
	items = []FactoryItem{a, b}
	picked = factoryPick(items, func(FactoryItem) bool { return true })
	if picked == nil || picked.ID != "a" {
		t.Fatalf("idle blocker held slot: %+v", picked)
	}
}
func TestFactoryLoopNeedsInputAnswerUsesNextRound(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "needs_input"
	i.Rounds = []FactoryRound{{Number: 1, ImplementEffectID: "first"}}
	i.Effects = []FactoryEffect{{ID: "first", Kind: "implement", Status: "completed", Outcome: &FactoryOutcome{EffectID: "first", Confirmed: true, Report: &FactoryReport{Outcome: "needs_input", Question: "which?", Reason: "ambiguous"}}}}
	i.Decision = &FactoryDecision{ID: "q", Kind: "question", EffectID: "first"}
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "answer", DecisionID: "old", Answer: "yes"}, now); err == nil {
		t.Fatal("stale decision accepted")
	}
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "answer", DecisionID: "q", Answer: "yes"}, now); err != nil {
		t.Fatal(err)
	}
	_, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "implement" || e.Round != 2 {
		t.Fatalf("answer did not use next round: %+v %v", e, err)
	}
}
func TestFactoryLoopStopDuringDispatchPreservesCommand(t *testing.T) {
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	i, err := store.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &loopEffects{store: store, observation: FactoryObservation{BaseSHA: "base"}, stopOnDispatch: true}
	c := NewFactoryController(store, fake, time.Now)
	_, err = c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "approve", ExpectedVersion: i.Version, Kind: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	c.admitAfterRecovery()
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Get(i.ID)
	if saved.State != "stopping" || saved.Effects[0].Status != "completed" {
		t.Fatalf("stop/completion lost: state=%s effect=%s", saved.State, saved.Effects[0].Status)
	}
}
func TestFactoryLoopReadyRevisionRevokesAndDrafts(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "ready"
	i.PRs = []FactoryPR{{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: false, HeadSHA: "old", BaseSHA: "base"}}
	i.Effects = []FactoryEffect{{ID: "ready", Kind: "set_ready", Status: "completed", Outcome: &FactoryOutcome{EffectID: "ready", Confirmed: true}}}
	moved := i.PRs[0]
	moved.HeadSHA = "new"
	next, e, err := AdvanceFactory(i, FactoryObservation{PR: &moved}, now)
	if err != nil || e == nil || e.Kind != "set_draft" || next.State == "ready" {
		t.Fatalf("ready not revoked: %s %+v %v", next.State, e, err)
	}
}
func TestFactoryReadyRerunRevokes(t *testing.T) {
	now := time.Now().UTC()
	for _, state := range []string{"failed", "blocked", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			i := loopItem()
			i.State = "ready"
			i.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", HeadSHA: "head", BaseSHA: "base"}}
			p := i.PRs[0]
			ci := FactoryCI{HeadSHA: "head", Complete: state != "unavailable"}
			if state != "unavailable" {
				ci.Checks = []FactoryCheck{{Key: FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 1}, SHA: "head", Status: "completed", Conclusion: "failure"}}
			}
			if state == "blocked" {
				ci.Checks[0].Conclusion = "cancelled"
			}
			n, e, err := AdvanceFactory(i, FactoryObservation{PR: &p, CI: &ci}, now)
			if err != nil || n.State == "ready" {
				t.Fatalf("stale readiness: state=%s effect=%+v err=%v", n.State, e, err)
			}
			if e == nil || e.Kind != "set_draft" {
				t.Fatalf("rerun did not draft: %+v", e)
			}
		})
	}
}
func TestFactoryActivePRMovement(t *testing.T) {
	now := time.Now().UTC()
	for _, stage := range []string{"checking", "reviewing"} {
		for _, movement := range []string{"closed", "head", "base", "remote", "identity"} {
			t.Run(stage+"/"+movement, func(t *testing.T) {
				i := loopItem()
				i.State = stage
				i.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
				i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, HeadSHA: "head", BaseSHA: "base", CI: &FactoryCI{HeadSHA: "head", Complete: true}, Review: &FactoryReport{Outcome: "complete", InputsAccessible: true, HeadSHA: "head", BaseSHA: "base", Findings: []ReviewFinding{}}}}
				role := "implementer"
				if stage == "reviewing" {
					role = "reviewer"
				}
				i.Effects = []FactoryEffect{{ID: "park", Kind: "park", WorkerRole: role, Status: "completed"}}
				p := i.PRs[0]
				obs := FactoryObservation{PR: &p, RemoteHead: "head", CI: &FactoryCI{HeadSHA: "head", Complete: true}}
				switch movement {
				case "closed":
					p.State = "merged"
				case "head":
					p.HeadSHA = "other"
				case "base":
					p.BaseSHA = "other"
				case "remote":
					obs.RemoteHead = "other"
				case "identity":
					p.NodeID = "other"
				}
				n, e, err := AdvanceFactory(i, obs, now)
				if err != nil || e != nil {
					t.Fatalf("advanced moved PR: %+v %v", e, err)
				}
				if movement == "closed" {
					if n.State != "closed" {
						t.Fatalf("state=%s", n.State)
					}
				} else if n.Decision == nil || n.Decision.Kind != "conflict" {
					t.Fatalf("decision=%+v", n.Decision)
				}
			})
		}
	}
}
func TestFactoryReadyRejectsSameSHAChangedRefs(t *testing.T) {
	i := loopItem()
	i.State = "reviewing"
	i.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
	i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, HeadSHA: "head", BaseSHA: "base", CI: &FactoryCI{HeadSHA: "head", Complete: true}, Review: &FactoryReport{Outcome: "complete", InputsAccessible: true, HeadSHA: "head", BaseSHA: "base", Findings: []ReviewFinding{}}}}
	i.Effects = []FactoryEffect{{Kind: "park", WorkerRole: "reviewer", Status: "completed"}}
	for _, ref := range []string{"head", "base", "identity"} {
		t.Run(ref, func(t *testing.T) {
			p := i.PRs[0]
			switch ref {
			case "head":
				p.HeadRef = "other"
			case "base":
				p.BaseRef = "other"
			case "identity":
				p.Number = 2
			}
			ci := FactoryCI{HeadSHA: "head", Complete: true}
			n, e, err := AdvanceFactory(i, FactoryObservation{PR: &p, CI: &ci}, time.Now())
			if err != nil || e != nil || n.Decision == nil || n.Decision.Kind != "conflict" {
				t.Fatalf("retarget accepted: %+v %+v %v", n.Decision, e, err)
			}
		})
	}
}
func TestFactoryLoopCommandReplayAndConflict(t *testing.T) {
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	i, err := store.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := NewFactoryController(store, &loopEffects{store: store}, time.Now)
	cmd := FactoryCommand{RequestID: "approve", Kind: "approve", ExpectedVersion: i.Version}
	first, err := c.Command(context.Background(), i.ID, cmd)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := c.Command(context.Background(), i.ID, cmd)
	if err != nil || replayed.Version != first.Version {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	cmd.Kind = "stop"
	if _, err = c.Command(context.Background(), i.ID, cmd); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	cmd.RequestID = "stale"
	if _, err = c.Command(context.Background(), i.ID, cmd); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("stale browser version: %v", err)
	}
}
func TestFactoryLoopExhaustedAnswerWaitsForGrant(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "needs_input"
	i.Rounds = []FactoryRound{{Number: 1, Implementation: &FactoryReport{Outcome: "needs_input"}}}
	i.Effects = []FactoryEffect{{ID: "first", Kind: "implement", Status: "completed", Outcome: &FactoryOutcome{EffectID: "first", Confirmed: true, Report: &FactoryReport{Outcome: "needs_input"}}}}
	i.Grants = []FactoryGrant{{ID: "initial", AdditionalRounds: 1}}
	i.Decision = &FactoryDecision{ID: "q", Kind: "question", EffectID: "first"}
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "answer", DecisionID: "q", Answer: "use A"}, now); err != nil {
		t.Fatal(err)
	}
	next, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e != nil || next.Decision == nil || next.Decision.Kind != "exhausted" {
		t.Fatalf("exhausted answer dispatched: %+v %+v %v", next.Decision, e, err)
	}
}
func TestFactoryLoopRetryRequiresIdle(t *testing.T) {
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	i, err := store.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = store.Update(i.ID, "blocked", i.Version, json.RawMessage(`{"blocked":true}`), func(dst *FactoryItem) error {
		dst.State = "needs_input"
		dst.Decision = &FactoryDecision{ID: "q"}
		dst.Workers = []FactoryWorker{{AgentID: "worker", Role: "implementer"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &loopEffects{store: store, observation: FactoryObservation{RuntimeKnown: true, TurnIdle: false, ActiveWorkKnown: true, WorkIdle: true}}
	c := NewFactoryController(store, fake, time.Now)
	cmd := FactoryCommand{RequestID: "retry", ExpectedVersion: i.Version, Kind: "retry", DecisionID: "q", Reason: "continue", AdditionalRounds: 1}
	if _, err = c.Command(context.Background(), i.ID, cmd); err == nil {
		t.Fatal("retry admitted while worker active")
	}
	fake.observation.TurnIdle = true
	if _, err = c.Command(context.Background(), i.ID, cmd); err != nil {
		t.Fatal(err)
	}
}
func TestFactoryLoopFreshEvidenceMarksReady(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "reviewing"
	i.PRs = []FactoryPR{{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
	i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, BaseSHA: "base", HeadSHA: "head", CI: &FactoryCI{HeadSHA: "head", Complete: true}, Review: &FactoryReport{Outcome: "complete", InputsAccessible: true, BaseSHA: "base", HeadSHA: "head", Findings: []ReviewFinding{}}}}
	i.Effects = []FactoryEffect{{ID: "park", Kind: "park", WorkerRole: "reviewer", Status: "completed"}}
	p := i.PRs[0]
	ci := FactoryCI{HeadSHA: "head", Complete: true}
	_, e, err := AdvanceFactory(i, FactoryObservation{PR: &p, CI: &ci}, now)
	if err != nil || e == nil || e.Kind != "set_ready" {
		t.Fatalf("fresh evidence: %+v %v", e, err)
	}
	p.HeadSHA = "moved"
	n, e, err := AdvanceFactory(i, FactoryObservation{PR: &p, CI: &ci}, now)
	if err != nil || e != nil || n.Decision == nil || n.Decision.Kind != "conflict" {
		t.Fatalf("moved head: %+v %+v %v", n.Decision, e, err)
	}
}

type blockedLoopEffects struct {
	started   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	readyPR   FactoryPR
}

func (f *blockedLoopEffects) Execute(_ context.Context, _ FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	if e.Kind == "implement" {
		select {
		case f.started <- struct{}{}:
		default:
		}
		<-f.release
		return FactoryOutcome{EffectID: e.ID, Confirmed: true, Report: &FactoryReport{OperationID: e.ID, Outcome: "complete", Summary: "done"}, HeadSHA: "head", TreeSHA: "tree"}, nil
	}
	return FactoryOutcome{EffectID: e.ID, Confirmed: true}, nil
}
func (f *blockedLoopEffects) Observe(_ context.Context, i FactoryItem) (FactoryObservation, error) {
	if i.State == "ready" {
		return FactoryObservation{PR: &f.readyPR}, nil
	}
	return FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}, nil
}
func (f *blockedLoopEffects) Cancel(_ context.Context, _ FactoryItem) error {
	select {
	case f.cancelled <- struct{}{}:
	default:
	}
	return nil
}
func blockedFactoryItem(t *testing.T, s *FactoryStore, request string) FactoryItem {
	t.Helper()
	req := factoryReq()
	req.RequestID = request
	i, err := s.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, request+"-work", i.Version, json.RawMessage(`{"work":true}`), func(dst *FactoryItem) error {
		a := FactoryApproval{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: time.Now()}
		dst.Approvals = []FactoryApproval{a}
		dst.Grants = []FactoryGrant{{ID: "initial", AdditionalRounds: 5}}
		dst.State = "implementing"
		dst.ApprovedAt = time.Now().Add(-time.Hour)
		dst.BaseSHA = "base"
		dst.Workers = []FactoryWorker{{AgentID: "w", SessionID: "s", Role: "implementer"}}
		dst.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, ImplementEffectID: "impl", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: "impl", Kind: "implement", Status: "prepared", WorkerRole: "implementer", Round: 1, Worker: &dst.Workers[0]}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func TestFactoryLoopStopCancelsActiveTurn(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := blockedFactoryItem(t, s, "blocked")
	f := &blockedLoopEffects{started: make(chan struct{}, 1), release: make(chan struct{}), cancelled: make(chan struct{}, 1)}
	c := NewFactoryController(s, f, time.Now)
	c.admitAfterRecovery()
	done := make(chan error, 1)
	go func() { done <- c.tick(context.Background()) }()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		close(f.release)
		t.Fatal("model did not start")
	}
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		close(f.release)
		t.Fatal("scheduler blocked on model turn")
	}
	cmd := FactoryCommand{RequestID: "stop", ExpectedVersion: i.Version + 1, Kind: "stop"}
	current, _ := s.Get(i.ID)
	cmd.ExpectedVersion = current.Version
	if _, err = c.Command(context.Background(), i.ID, cmd); err != nil {
		close(f.release)
		t.Fatal(err)
	}
	select {
	case <-f.cancelled:
	case <-time.After(time.Second):
		close(f.release)
		t.Fatal("ACP cancel not sent")
	}
	close(f.release)
	deadline := time.After(time.Second)
	for {
		saved, _ := s.Get(i.ID)
		if len(saved.Effects) > 0 && saved.Effects[len(saved.Effects)-1].Status == "completed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cancelled callback not persisted")
		case <-time.After(time.Millisecond * 5):
		}
	}
}
func TestFactoryLoopReadyMonitorDuringModelTurn(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = blockedFactoryItem(t, s, "blocked")
	req := factoryReq()
	req.RequestID = "ready-create"
	ready, err := s.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ready, err = s.Update(ready.ID, "ready-state", ready.Version, json.RawMessage(`{"ready":true}`), func(dst *FactoryItem) error {
		dst.State = "ready"
		dst.BaseSHA = "base"
		dst.PRs = []FactoryPR{{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: false, HeadSHA: "old", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: "ready", Kind: "set_ready", Status: "completed", Outcome: &FactoryOutcome{EffectID: "ready", Confirmed: true}}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &blockedLoopEffects{started: make(chan struct{}, 1), release: make(chan struct{}), cancelled: make(chan struct{}, 1), readyPR: FactoryPR{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: false, HeadSHA: "new", BaseSHA: "base"}}
	c := NewFactoryController(s, f, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(f.release)
	c.admitAfterRecovery()
	go c.Run(ctx)
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	deadline := time.After(time.Second)
	for {
		item, _ := s.Get(ready.ID)
		if item.State != "ready" && len(item.Effects) == 2 && item.Effects[1].Kind == "set_draft" && item.Effects[1].Status == "prepared" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("ready PR not monitored during model turn: %+v", item)
		case <-time.After(time.Millisecond * 10):
			c.Wake()
		}
	}
}
func TestFactoryLoopFailedCICleanReviewRepairs(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "reviewing"
	i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, BaseSHA: "base", HeadSHA: "head", CI: &FactoryCI{HeadSHA: "head", Complete: true, Checks: []FactoryCheck{{Key: FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 1}, SHA: "head", Status: "completed", Conclusion: "failure", LogTail: "build failed"}}}, Review: &FactoryReport{Outcome: "complete", InputsAccessible: true, BaseSHA: "base", HeadSHA: "head", Findings: []ReviewFinding{}}}}
	i.Effects = []FactoryEffect{{ID: "park", Kind: "park", WorkerRole: "reviewer", Status: "completed"}}
	n, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "resume" || n.State == "ready" {
		t.Fatalf("failed CI escaped repair: %+v state=%s err=%v", e, n.State, err)
	}
	n, err = CompleteFactoryEffect(n, e.ID, FactoryOutcome{EffectID: e.ID, Confirmed: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	n, e, err = AdvanceFactory(n, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "implement" || e.Round != 2 {
		t.Fatalf("CI repair not dispatched: %+v %v", e, err)
	}
	prompt, err := BuildFactoryPrompt(n, *e)
	if err != nil || !strings.Contains(prompt, "build failed") {
		t.Fatalf("CI repair prompt=%s err=%v", prompt, err)
	}
}
func TestFactoryLoopAnswerOnlyCurrentQuestion(t *testing.T) {
	now := time.Now().UTC()
	for _, kind := range []string{"recovery", "exhausted", "conflict", "question"} {
		i := loopItem()
		i.State = "needs_input"
		i.Effects = []FactoryEffect{{ID: "current", Kind: "implement", Status: "completed", Outcome: &FactoryOutcome{EffectID: "current", Confirmed: true, Report: &FactoryReport{Outcome: "needs_input"}}}}
		i.Decision = &FactoryDecision{ID: "q", Kind: kind, EffectID: "current"}
		err := factoryApplyCommand(&i, FactoryCommand{Kind: "answer", DecisionID: "q", Answer: "A"}, now)
		if kind == "question" && err != nil || kind != "question" && err == nil {
			t.Fatalf("kind=%s answer err=%v", kind, err)
		}
	}
	i := loopItem()
	i.State = "needs_input"
	i.Decision = &FactoryDecision{ID: "q", Kind: "question", EffectID: "old"}
	i.Effects = []FactoryEffect{{ID: "current", Kind: "implement", Status: "completed"}}
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "answer", DecisionID: "q", Answer: "A"}, now); err == nil {
		t.Fatal("stale effect question accepted")
	}
}
func TestFactoryLoopReplacementSupersedesReview(t *testing.T) {
	now := time.Now().UTC()
	i := loopItem()
	i.State = "needs_input"
	i.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, BaseSHA: "base", HeadSHA: "head", Review: &FactoryReport{Outcome: "complete", Findings: []ReviewFinding{}}}}
	i.Effects = []FactoryEffect{{ID: "park", Kind: "park", WorkerRole: "reviewer", Status: "completed"}}
	i.Decision = &FactoryDecision{ID: "block", Kind: "conflict"}
	r := factoryReq()
	r.RequestID = "replacement"
	r.Plan = "replacement plan"
	if err := factoryApplyCommand(&i, FactoryCommand{Kind: "retry", DecisionID: "block", RequestID: "retry", Reason: "new plan", AdditionalRounds: 1, Replacement: &r}, now); err != nil {
		t.Fatal(err)
	}
	n, e, err := AdvanceFactory(i, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind == "set_ready" || n.State == "checking" {
		t.Fatalf("replacement reused old evidence: %+v state=%s err=%v", e, n.State, err)
	}
	if len(n.Approvals) != 2 || n.Approvals[1].Revision != 2 {
		t.Fatalf("approval history: %+v", n.Approvals)
	}
	n, err = CompleteFactoryEffect(n, e.ID, FactoryOutcome{EffectID: e.ID, Confirmed: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	n, e, err = AdvanceFactory(n, FactoryObservation{}, now)
	if err != nil || e == nil || e.Kind != "implement" || e.ApprovalRevision != 2 || e.Round != 2 {
		t.Fatalf("new approval did not restart implementation: %+v %v", e, err)
	}
}

// Closure must retain ownership until every runtime has been confirmed idle
// (including detached work) and parked.
func TestFactoryExternalClosureCleansWorkers(t *testing.T) {
	for _, state := range []string{"closed", "merged"} {
		t.Run(state, func(t *testing.T) {
			s, err := OpenFactoryStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			i, err := s.Create(factoryReq(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			i, err = s.Update(i.ID, "fixture", i.Version, json.RawMessage(`{}`), func(d *FactoryItem) error {
				d.State = "reviewing"
				d.Workers = []FactoryWorker{{AgentID: "implementer", Role: "implementer"}, {AgentID: "reviewer", Role: "reviewer"}}
				d.PRs = []FactoryPR{{Number: 1, State: "open"}}
				d.Effects = []FactoryEffect{{ID: "review", Kind: "review", WorkerRole: "reviewer", Status: "completed"}}
				return nil
			}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f := &loopEffects{store: s, observation: FactoryObservation{PR: &FactoryPR{Number: 1, State: state}}}
			c := NewFactoryController(s, f, time.Now)
			c.admitAfterRecovery()
			if err := c.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Get(i.ID)
			if got.State != "stopping" || !c.OwnsBranch(got.RepoID, got.Branch) {
				t.Fatalf("closure released ownership: %+v", got)
			}
			for _, obs := range []FactoryObservation{
				{RuntimeKnown: true, TurnIdle: false, ActiveWorkKnown: true, WorkIdle: true},
				{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: false},
				{RuntimeKnown: true, TurnIdle: true},
			} {
				f.observation = obs
				if err := c.tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				got, _ = s.Get(i.ID)
				if got.State != "stopping" || len(f.seen) != 0 {
					t.Fatalf("unsafe cleanup: %+v %v", got, f.seen)
				}
			}
			f.observation = FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}
			for n := 0; n < 3; n++ {
				if err := c.tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			got, _ = s.Get(i.ID)
			if got.State != "closed" || len(f.seen) != 2 || !got.Workers[0].Parked || !got.Workers[1].Parked {
				t.Fatalf("cleanup incomplete: %+v %v", got, f.seen)
			}
			if _, owned := c.OwnsAgent("reviewer"); owned {
				t.Fatal("closed worker still owned")
			}
		})
	}
}

type closureActiveEffects struct {
	*blockedLoopEffects
	closed bool // accessed only by the scheduler test goroutine
}

func (f *closureActiveEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	p := FactoryPR{Number: 1, State: "open"}
	if f.closed {
		p.State = "closed"
	}
	return FactoryObservation{PR: &p, RuntimeKnown: true, TurnIdle: !f.closed, ActiveWorkKnown: true, WorkIdle: true}, nil
}
func TestFactoryExternalClosureCancelsDispatchedTurn(t *testing.T) {
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := blockedFactoryItem(t, s, "closure-active")
	i, err = s.Update(i.ID, "pr", i.Version, json.RawMessage(`{}`), func(d *FactoryItem) error { d.PRs = []FactoryPR{{Number: 1, State: "open"}}; return nil }, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &closureActiveEffects{blockedLoopEffects: &blockedLoopEffects{started: make(chan struct{}, 1), release: make(chan struct{}), cancelled: make(chan struct{}, 1)}}
	c := NewFactoryController(s, f, time.Now)
	c.admitAfterRecovery()
	defer c.Quiesce()
	defer close(f.release)
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	f.closed = true
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.cancelled:
	default:
		t.Fatal("external closure did not cancel active turn")
	}
	got, _ := s.Get(i.ID)
	if got.State != "stopping" || got.Effects[len(got.Effects)-1].Status != "dispatched" {
		t.Fatalf("active turn released: %+v", got)
	}
	if _, owned := c.OwnsAgent("w"); !owned {
		t.Fatal("active worker lost ownership")
	}
	if err = c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(i.ID)
	if got.State != "stopping" {
		t.Fatalf("active turn closed: %+v", got)
	}
}
