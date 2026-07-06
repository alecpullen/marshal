package session

import (
	"sync"
	"testing"
)

func TestSwarmProgressSetAndCopy(t *testing.T) {
	s := newTestState()
	p := SwarmProgress{
		Goal:   "add a test",
		Active: true,
		Roles: []SwarmRole{
			{Name: "planner", Status: SwarmRolePending},
			{Name: "implementer", Status: SwarmRolePending},
		},
	}
	s.SetSwarmProgress(p)

	got := s.SwarmProgress()
	got.Roles[0].Status = SwarmRoleDone
	if s.SwarmProgress().Roles[0].Status != SwarmRolePending {
		t.Fatal("SwarmProgress() must return a copy; caller mutation leaked into state")
	}
}

func TestUpdateSwarmRole(t *testing.T) {
	s := newTestState()
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{
		{Name: "implementer", Status: SwarmRolePending},
	}})
	s.UpdateSwarmRole("implementer", SwarmRoleActive, "round 2/3")
	got := s.SwarmProgress().Roles[0]
	if got.Status != SwarmRoleActive || got.Detail != "round 2/3" {
		t.Fatalf("role = %+v, want active/round 2/3", got)
	}
}

func TestClearSwarmProgress(t *testing.T) {
	s := newTestState()
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{{Name: "planner"}}})
	s.ClearSwarmProgress()
	if s.SwarmProgress().Active {
		t.Fatal("ClearSwarmProgress should mark progress inactive")
	}
}

func TestSwarmProgressConcurrentUpdates(t *testing.T) {
	s := newTestState()
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{
		{Name: "scout-a", Status: SwarmRolePending},
		{Name: "scout-b", Status: SwarmRolePending},
	}})
	var wg sync.WaitGroup
	for _, name := range []string{"scout-a", "scout-b"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			s.UpdateSwarmRole(n, SwarmRoleDone, "")
		}(name)
	}
	wg.Wait()
	for _, r := range s.SwarmProgress().Roles {
		if r.Status != SwarmRoleDone {
			t.Fatalf("role %s not marked done", r.Name)
		}
	}
}
