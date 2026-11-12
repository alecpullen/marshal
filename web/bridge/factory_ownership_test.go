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
