package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge"
)

func TestFactoryStartupOrdering(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	owned, err := bridge.OpenFactoryStore(filepath.Join(state, "factory", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	err = run(context.Background(), []string{"--token", "test-token", "--workspace", filepath.Join(root, "workspace.json"), "--state-dir", state, "--cwd-root", root, "--egress", "off", "--preview-addr", "off"}, io.Discard, io.Discard)
	if !errors.Is(err, bridge.ErrFactoryLocked) {
		t.Fatalf("startup must refuse owned journal before starting work: %v", err)
	}
	if !strings.Contains(err.Error(), "open factory") {
		t.Fatalf("missing startup phase: %v", err)
	}
}

type startupEffects struct {
	observed chan struct{}
	executed chan struct{}
}

func (e *startupEffects) Observe(context.Context, bridge.FactoryItem) (bridge.FactoryObservation, error) {
	select {
	case e.observed <- struct{}{}:
	default:
	}
	return bridge.FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}, nil
}
func (e *startupEffects) Execute(_ context.Context, _ bridge.FactoryItem, fx bridge.FactoryEffect) (bridge.FactoryOutcome, error) {
	select {
	case e.executed <- struct{}{}:
	default:
	}
	return bridge.FactoryOutcome{EffectID: fx.ID, Confirmed: true}, nil
}

func TestFactoryStartupRecoveryBeforeDispatchAndParkedSkip(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	ws := bridge.NewWorkspace(filepath.Join(root, "workspace.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutAgent(bridge.Agent{ID: "parked", Project: root, FactoryItemID: "old-item", FactoryRole: "implementer", FactoryParked: true}); err != nil {
		t.Fatal(err)
	}
	fleet := bridge.NewFleet(ws, "nonexistent-fixture-binary", nil, state, bridge.Limits{}, "", nil, "")
	defer fleet.Close()
	store, err := bridge.OpenFactoryStore(filepath.Join(state, "factory", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	req := bridge.FactoryCreate{RequestID: "create", Title: "task", Plan: "plan", Config: bridge.FactoryConfig{RepoID: "repo", Native: true, NoRequiredCIAcknowledged: true}}
	item, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(item.ID, "approve", item.Version, []byte(`{"kind":"approve"}`), func(i *bridge.FactoryItem) error {
		i.State = "queued"
		i.BaseSHA = "base"
		i.ApprovedAt = time.Now()
		i.Approvals = []bridge.FactoryApproval{{Revision: 1, Plan: req.Plan, PlanHash: bridge.FactoryPlanHash(req.Plan, ""), Config: req.Config, ApprovedAt: i.ApprovedAt}}
		i.Grants = []bridge.FactoryGrant{{ID: "initial", AdditionalRounds: 5}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	effects := &startupEffects{observed: make(chan struct{}, 2), executed: make(chan struct{}, 2)}
	store, controller, err := openRecoveredFactory(context.Background(), fleet, state, effects)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	select {
	case <-effects.observed:
	default:
		t.Fatal("recovery did not observe persisted item")
	}
	select {
	case <-effects.executed:
		t.Fatal("work dispatched during recovery")
	default:
	}
	if errs := fleet.ReattachAll(context.Background()); len(errs) != 0 {
		t.Fatalf("parked factory worker reattached: %v", errs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	select {
	case <-effects.executed:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not dispatch after recovery")
	}
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}
