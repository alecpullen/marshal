package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestFactoryOwnership(t *testing.T) {
	i := loopItem()
	i.Workers = []FactoryWorker{{AgentID: "owned"}}
	c := NewFactoryController(nil, nil, nil)
	if !factoryItemOwnsAgent(i, "owned") || !factoryItemOwnsBranch(i, i.RepoID, i.Branch) {
		t.Fatal("factory ownership lost")
	}
	if factoryItemOwnsBranch(i, "another", i.Branch) || factoryItemOwnsAgent(i, "other") {
		t.Fatal("factory ownership too broad")
	}
	_ = c
}

func TestFactoryOwnershipGuardsProjectDeletion(t *testing.T) {
	f := testFleet(t)
	root := t.TempDir()
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "project-owned", i.Version, json.RawMessage(`{"kind":"own"}`), func(dst *FactoryItem) error {
		dst.State = "recovering"
		dst.Workers = []FactoryWorker{{AgentID: "project-worker", SessionID: "session", Role: "implementer", SpawnEffectID: "spawn"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &recoveryEffects{}, time.Now))
	if err := f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.PutAgent(Agent{ID: "project-worker", Project: root, FactoryItemID: i.ID, FactoryRole: "implementer", FactorySpawnEffectID: "spawn", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := f.StopProject(root); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("stop project: %v", err)
	}
	if err := f.ws.RemoveProject(root); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("workspace remove project: %v", err)
	}
	server := NewServer(f, "", "")
	resp := doReq(t, server, http.MethodDelete, "/api/projects", map[string]string{"root": root}, nil)
	if resp.Code != http.StatusConflict {
		t.Fatalf("HTTP delete: %d %s", resp.Code, resp.Body.String())
	}
	if _, ok := f.ws.Agent("project-worker"); !ok {
		t.Fatal("worker identity lost")
	}
}

func TestFactoryOwnershipGuardsSharedEntryPoints(t *testing.T) {
	f := testFleet(t)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i, err := s.Create(factoryReq(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "own", i.Version, json.RawMessage(`{"kind":"own"}`), func(dst *FactoryItem) error {
		dst.State = "implementing"
		dst.Branch = "marshal/factory/" + dst.ID
		dst.Workers = []FactoryWorker{{AgentID: "owned", Role: "implementer", SessionID: "session", SpawnEffectID: "spawn"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &recoveryEffects{}, time.Now))
	if err := f.ws.PutAgent(Agent{ID: "owned", Project: t.TempDir(), FactoryItemID: i.ID, FactoryRole: "implementer", FactorySpawnEffectID: "spawn", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Merge(context.Background(), "owned", ""); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("merge: %v", err)
	}
	if err := f.Discard(context.Background(), "owned"); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("discard: %v", err)
	}
	if _, err := f.Exit(context.Background(), "owned", ExitOptions{}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("exit: %v", err)
	}
	if err := f.StartRun(context.Background(), "owned", RunRequest{}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("run: %v", err)
	}
	if !f.factoryOwnsBranch(i.RepoID, i.Branch) {
		t.Fatal("branch unowned")
	}
}

// TestFactoryWorkerSubagentSettingsRecovery proves a recreated worker session
// carries the snapshot its approval froze, exactly as the lost session did, and
// that a runtime which cannot confirm those settings leaves the worker
// uncertain rather than recovering it on settings nobody approved.
func TestFactoryWorkerSubagentSettingsRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("a recreated session carries the worker's own snapshot", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		fx.reportSettings(t, true, FactorySubagentSettings{Enabled: true, Concurrency: 8})
		implementerSettings := FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
		reviewerSettings := FactorySubagentSettings{Enabled: false, Model: "test/cheap", Concurrency: 6}
		item := fx.item("i-1", fx.approval(implementerSettings, reviewerSettings))
		previous := fx.recoverableWorker(t, item, "reviewer", FactoryWorker{ApprovalRevision: 1, Subagents: reviewerSettings})
		worker, err := fx.f.ResumeFactoryWorkerFresh(ctx, item, previous)
		if err != nil {
			t.Fatal(err)
		}
		settings, ok := fx.sentSettings(t, worker.AgentID)
		if !ok {
			t.Fatal("the recreated session carried no subagentSettings")
		}
		wantSubagentSettings(t, settings, reviewerSettings)
		if worker.Subagents != reviewerSettings || worker.ApprovalRevision != 1 {
			t.Fatalf("recovered worker bound revision %d settings %+v", worker.ApprovalRevision, worker.Subagents)
		}
		if worker.SessionID == "" || worker.SessionID == previous.SessionID {
			t.Fatalf("recovered worker session = %q", worker.SessionID)
		}
	})

	t.Run("a runtime that cannot confirm the settings leaves the worker uncertain", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		fx.silenceSettings()
		settings := FactorySubagentSettings{Enabled: true, Model: "test/big", Concurrency: 2}
		item := fx.item("i-1", fx.approval(settings, FactorySubagentSettings{}))
		previous := fx.recoverableWorker(t, item, "implementer", FactoryWorker{ApprovalRevision: 1, Subagents: settings})
		if _, err := fx.f.ResumeFactoryWorkerFresh(ctx, item, previous); !errors.Is(err, ErrFactoryUncertain) {
			t.Fatalf("recovery err = %v, want ErrFactoryUncertain", err)
		}
		if _, err := fx.f.runtimeForAgent(previous.AgentID); err == nil {
			t.Fatal("a refused recovery left a runtime running")
		}
		if a, ok := fx.f.ws.Agent(previous.AgentID); !ok || a.SessionID != previous.SessionID {
			t.Fatalf("a refused recovery moved the saved session: %+v", a)
		}
		if n, _ := fx.f.slots.stats(); n != 0 {
			t.Fatalf("a refused recovery took %d slots", n)
		}
	})

	t.Run("an approval that froze nothing recovers without the capability", func(t *testing.T) {
		fx := newFactorySubagentWorkerFixture(t)
		fx.silenceSettings()
		item := fx.item("i-1", fx.approval(FactorySubagentSettings{}, FactorySubagentSettings{}))
		previous := fx.recoverableWorker(t, item, "implementer", FactoryWorker{})
		worker, err := fx.f.ResumeFactoryWorkerFresh(ctx, item, previous)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := fx.sentSettings(t, worker.AgentID); ok {
			t.Fatal("a legacy approval sent subagentSettings on recovery")
		}
	})
}
