package session

import (
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestSDDProgressSetAndCopy(t *testing.T) {
	p := SDDProgress{
		Active:   true,
		PlanName: "feature-plan",
		Tasks: []SDDTaskStatus{
			{Name: "Task 1", Phase: SDDPhasePending},
			{Name: "Task 2", Phase: SDDPhasePending},
		},
	}
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(p)

	got := s.SDDProgress()
	got.Tasks[0].Phase = SDDPhaseDone
	if s.SDDProgress().Tasks[0].Phase != SDDPhasePending {
		t.Fatal("SDDProgress() must return a copy; caller mutation leaked into state")
	}
}

func TestUpdateSDDTask(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true, Tasks: []SDDTaskStatus{
		{Name: "Task 2", Phase: SDDPhasePending, Implementer: SDDPhasePending},
	}})
	s.UpdateSDDTask(0, func(ts *SDDTaskStatus) {
		ts.Phase = SDDPhaseActive
		ts.Implementer = SDDPhaseActive
		ts.Detail = "running 12s"
	})
	got := s.SDDProgress().Tasks[0]
	if got.Phase != SDDPhaseActive || got.Implementer != SDDPhaseActive || got.Detail != "running 12s" {
		t.Fatalf("UpdateSDDTask = %+v, want active", got)
	}
}

func TestClearSDDProgress(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true, Tasks: []SDDTaskStatus{{Name: "Task 1"}}})
	s.ClearSDDProgress()
	if s.SDDProgress().Active {
		t.Fatal("ClearSDDProgress should mark progress inactive")
	}
}

func TestSDDProgressConcurrentUpdates(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true, Tasks: []SDDTaskStatus{
		{Name: "Task 1", Phase: SDDPhasePending},
		{Name: "Task 2", Phase: SDDPhasePending},
	}})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.UpdateSDDTask(idx, func(ts *SDDTaskStatus) {
				ts.Phase = SDDPhaseDone
			})
		}(i)
	}
	wg.Wait()
	for _, task := range s.SDDProgress().Tasks {
		if task.Phase != SDDPhaseDone {
			t.Fatal("concurrent UpdateSDDTask lost an update")
		}
	}
}

func TestUpdateSDDBranchReview(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true})
	s.UpdateSDDBranchReview(SDDPhaseActive)
	if s.SDDProgress().BranchReview != SDDPhaseActive {
		t.Fatalf("BranchReview = %q, want active", s.SDDProgress().BranchReview)
	}
}

func TestUpdateSDDTokens(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true})
	s.UpdateSDDTokens(1500, 10000)
	got := s.SDDProgress()
	if got.TokensUsed != 1500 || got.TokensMax != 10000 {
		t.Fatalf("tokens = %d/%d, want 1500/10000", got.TokensUsed, got.TokensMax)
	}
}

func TestSDDProgressControllerState(t *testing.T) {
	ss := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	ss.SetSDDProgress(SDDProgress{Active: true, ControllerState: "DRAIN_ITERATION"})
	p := ss.SDDProgress()
	if p.ControllerState != "DRAIN_ITERATION" {
		t.Errorf("ControllerState = %q", p.ControllerState)
	}
}

func TestSDDTaskStatusAuditReviewPhases(t *testing.T) {
	ss := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	ss.SetSDDProgress(SDDProgress{Tasks: []SDDTaskStatus{{Name: "T1", Audit: SDDPhaseDone, Review: SDDPhaseActive}}})
	ss.UpdateSDDTask(0, func(ts *SDDTaskStatus) {
		if ts.Audit != SDDPhaseDone {
			t.Error("Audit phase not retained")
		}
	})
}
